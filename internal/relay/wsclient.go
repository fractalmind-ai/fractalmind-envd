package relay

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// WSSClient connects to a WSS relay server and bridges WireGuard UDP
// traffic through the WebSocket tunnel. Used on UDP-restricted nodes.
type WSSClient struct {
	mu            sync.Mutex
	relayURL      string
	suiAddress    string
	signer        Signer
	wgListenAddr  string // WireGuard's local UDP address (e.g., "127.0.0.1:51820")
	conn          *websocket.Conn
	relayEndpoint string              // Assigned public endpoint from relay
	peers         map[uint16]*proxyPeer // peer_id → local UDP proxy
	nextPeerID    uint16
	done          chan struct{}
}

// proxyPeer tracks a local UDP proxy for one WireGuard peer.
type proxyPeer struct {
	id         uint16
	target     string       // Real endpoint of the peer (for relay routing)
	localConn  net.PacketConn // Local UDP socket (127.0.0.1:ephemeral)
	localAddr  string       // "127.0.0.1:PORT" — set as WG peer endpoint
	wgAddr     *net.UDPAddr // WireGuard's local address to send incoming packets to
}

// NewWSSClient creates a WSS relay client.
// signer is used for challenge-response authentication (implements relay.Signer).
func NewWSSClient(relayURL, suiAddress string, signer Signer, wgListenAddr string) *WSSClient {
	return &WSSClient{
		relayURL:     relayURL,
		suiAddress:   suiAddress,
		signer:       signer,
		wgListenAddr: wgListenAddr,
		peers:        make(map[uint16]*proxyPeer),
		nextPeerID:   1,
		done:         make(chan struct{}),
	}
}

// Connect establishes a WSS connection to the relay and authenticates.
// Returns the relay-assigned public endpoint that should be registered on SUI.
func (c *WSSClient) Connect(ctx context.Context) (string, error) {
	conn, _, err := websocket.Dial(ctx, c.relayURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return "", fmt.Errorf("dial relay: %w", err)
	}
	conn.SetReadLimit(65536)

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// Wait for challenge nonce from relay
	challengeMsg, err := c.readControl(ctx)
	if err != nil {
		conn.CloseNow()
		return "", fmt.Errorf("read challenge: %w", err)
	}
	if challengeMsg.Type != MsgTypeChallenge || challengeMsg.Nonce == "" {
		conn.CloseNow()
		return "", fmt.Errorf("expected challenge, got %s", challengeMsg.Type)
	}

	// Sign the challenge nonce
	nonceBytes, err := hex.DecodeString(challengeMsg.Nonce)
	if err != nil {
		conn.CloseNow()
		return "", fmt.Errorf("decode nonce: %w", err)
	}
	sig := c.signer.Sign(nonceBytes)
	pubKey := c.signer.PublicKeyBytes()

	// Send signed auth response
	if err := c.writeControl(ctx, ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: c.suiAddress,
		PublicKey:  hex.EncodeToString(pubKey),
		Signature:  hex.EncodeToString(sig),
	}); err != nil {
		conn.CloseNow()
		return "", fmt.Errorf("send auth: %w", err)
	}

	// Wait for allocated endpoint
	msg, err := c.readControl(ctx)
	if err != nil {
		conn.CloseNow()
		return "", fmt.Errorf("read allocated: %w", err)
	}
	if msg.Type == MsgTypeError {
		conn.CloseNow()
		return "", fmt.Errorf("relay error: %s", msg.Endpoint)
	}
	if msg.Type != MsgTypeAllocated || msg.Endpoint == "" {
		conn.CloseNow()
		return "", fmt.Errorf("unexpected message: %s", msg.Type)
	}

	c.mu.Lock()
	c.relayEndpoint = msg.Endpoint
	c.mu.Unlock()

	log.Printf("[wss-client] connected to relay, assigned endpoint: %s", msg.Endpoint)

	// Start WSS → local UDP forwarder in background
	go c.wssReadLoop()

	// Start keepalive
	go c.keepaliveLoop()

	return msg.Endpoint, nil
}

// AddPeer registers a peer route through the relay and creates a local UDP proxy.
// Returns the local address (127.0.0.1:PORT) to use as the WireGuard peer endpoint.
func (c *WSSClient) AddPeer(ctx context.Context, targetEndpoint string) (localAddr string, peerID uint16, err error) {
	c.mu.Lock()
	id := c.nextPeerID
	c.nextPeerID++
	c.mu.Unlock()

	// Tell relay about this peer
	if err := c.writeControl(ctx, ControlMsg{
		Type:   MsgTypeAddPeer,
		PeerID: id,
		Target: targetEndpoint,
	}); err != nil {
		return "", 0, fmt.Errorf("send add_peer: %w", err)
	}

	// Create local UDP proxy socket
	localConn, err := net.ListenPacket("udp4", "127.0.0.1:0") // ephemeral port
	if err != nil {
		return "", 0, fmt.Errorf("listen local proxy: %w", err)
	}

	wgAddr, err := net.ResolveUDPAddr("udp", c.wgListenAddr)
	if err != nil {
		localConn.Close()
		return "", 0, fmt.Errorf("resolve wg addr: %w", err)
	}

	peer := &proxyPeer{
		id:        id,
		target:    targetEndpoint,
		localConn: localConn,
		localAddr: localConn.LocalAddr().String(),
		wgAddr:    wgAddr,
	}

	c.mu.Lock()
	c.peers[id] = peer
	c.mu.Unlock()

	// Start local UDP → WSS forwarder for this peer
	go c.localUDPReadLoop(peer)

	log.Printf("[wss-client] peer %d → %s (local proxy: %s)", id, targetEndpoint, peer.localAddr)
	return peer.localAddr, id, nil
}

// RemovePeer removes a peer route and closes its local UDP proxy.
func (c *WSSClient) RemovePeer(ctx context.Context, peerID uint16) error {
	c.mu.Lock()
	peer, ok := c.peers[peerID]
	if ok {
		delete(c.peers, peerID)
	}
	c.mu.Unlock()

	if !ok {
		return nil
	}

	peer.localConn.Close()

	return c.writeControl(ctx, ControlMsg{
		Type:   MsgTypeRemovePeer,
		PeerID: peerID,
	})
}

// RelayEndpoint returns the relay-assigned public endpoint.
func (c *WSSClient) RelayEndpoint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relayEndpoint
}

// localUDPReadLoop reads WireGuard packets from a local proxy and sends them via WSS.
func (c *WSSClient) localUDPReadLoop(peer *proxyPeer) {
	buf := make([]byte, 65536)
	for {
		select {
		case <-c.done:
			return
		default:
		}

		peer.localConn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, _, err := peer.localConn.ReadFrom(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		msg := EncodeDataMsg(peer.id, buf[:n])
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = conn.Write(ctx, websocket.MessageBinary, msg)
		cancel()
		if err != nil {
			log.Printf("[wss-client] ws write failed for peer %d: %v", peer.id, err)
			return
		}
	}
}

// wssReadLoop reads messages from the relay and dispatches them.
func (c *WSSClient) wssReadLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}

		typ, data, err := conn.Read(context.Background())
		if err != nil {
			log.Printf("[wss-client] relay read error: %v", err)
			return
		}

		switch typ {
		case websocket.MessageBinary:
			c.handleRelayData(data)
		case websocket.MessageText:
			// Control message — handle ping/pong
			var msg ControlMsg
			if json.Unmarshal(data, &msg) == nil && msg.Type == MsgTypePing {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				c.writeControl(ctx, ControlMsg{Type: MsgTypePong})
				cancel()
			}
		}
	}
}

// handleRelayData forwards a WireGuard packet from the relay to the local WG interface.
func (c *WSSClient) handleRelayData(data []byte) {
	peerID, wgPacket, err := DecodeDataMsg(data)
	if err != nil {
		return
	}

	c.mu.Lock()
	peer, ok := c.peers[peerID]
	c.mu.Unlock()

	if !ok {
		// Peer ID 0 = catch-all for unknown sources. Try to deliver anyway.
		if peerID == 0 {
			c.mu.Lock()
			for _, p := range c.peers {
				peer = p
				break // Pick any peer for catch-all (best effort)
			}
			c.mu.Unlock()
		}
		if peer == nil {
			return
		}
	}

	// Send to WireGuard's UDP socket, from the local proxy address
	_, err = peer.localConn.WriteTo(wgPacket, peer.wgAddr)
	if err != nil {
		log.Printf("[wss-client] local write to wg failed for peer %d: %v", peerID, err)
	}
}

// keepaliveLoop sends periodic pings to keep the WebSocket connection alive
// through corporate proxies that drop idle connections.
func (c *WSSClient) keepaliveLoop() {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.writeControl(ctx, ControlMsg{Type: MsgTypePing})
			cancel()
			if err != nil {
				log.Printf("[wss-client] keepalive failed: %v", err)
				return
			}
		}
	}
}

// Close disconnects from the relay and cleans up all local proxies.
func (c *WSSClient) Close() error {
	select {
	case <-c.done:
		return nil // already closed
	default:
		close(c.done)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, peer := range c.peers {
		peer.localConn.Close()
	}
	c.peers = make(map[uint16]*proxyPeer)

	if c.conn != nil {
		c.conn.Close(websocket.StatusNormalClosure, "shutdown")
		c.conn = nil
	}

	log.Printf("[wss-client] disconnected from relay")
	return nil
}

func (c *WSSClient) readControl(ctx context.Context) (ControlMsg, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	typ, data, err := conn.Read(ctx)
	if err != nil {
		return ControlMsg{}, err
	}
	if typ != websocket.MessageText {
		return ControlMsg{}, fmt.Errorf("expected text, got binary")
	}
	var msg ControlMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return ControlMsg{}, err
	}
	return msg, nil
}

func (c *WSSClient) writeControl(ctx context.Context, msg ControlMsg) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}
