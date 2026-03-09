package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// WSSHandler accepts WebSocket connections from UDP-restricted clients
// and bridges their WireGuard traffic over allocated UDP ports.
type WSSHandler struct {
	mu       sync.Mutex
	publicIP string
	portMin  int
	portMax  int
	usedPort map[int]bool                // port → in-use
	clients  map[string]*relayAllocation // SUI address → allocation
}

// relayAllocation tracks one restricted client's relay state.
type relayAllocation struct {
	suiAddr  string
	udpPort  int
	udpConn  net.PacketConn
	wsConn   *websocket.Conn
	peers    map[uint16]*relayPeer // peer_id → peer info
	addrMap  map[string]uint16     // "ip:port" → peer_id (reverse lookup for incoming UDP)
	done     chan struct{}
}

// relayPeer tracks a target peer for a relayed client.
type relayPeer struct {
	id       uint16
	target   *net.UDPAddr
	lastSeen time.Time
}

// NewWSSHandler creates a WebSocket relay handler.
func NewWSSHandler(publicIP string, portMin, portMax int) *WSSHandler {
	return &WSSHandler{
		publicIP: publicIP,
		portMin:  portMin,
		portMax:  portMax,
		usedPort: make(map[int]bool),
		clients:  make(map[string]*relayAllocation),
	}
}

// ServeHTTP implements http.Handler for the WSS relay endpoint.
func (h *WSSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled, // WG packets are encrypted, incompressible
	})
	if err != nil {
		log.Printf("[wss-relay] accept failed: %v", err)
		return
	}
	conn.SetReadLimit(65536) // generous for WG packets

	ctx := r.Context()
	h.handleClient(ctx, conn)
}

func (h *WSSHandler) handleClient(ctx context.Context, conn *websocket.Conn) {
	defer conn.CloseNow()

	// Wait for auth message
	authMsg, err := h.readControl(ctx, conn)
	if err != nil {
		log.Printf("[wss-relay] auth read failed: %v", err)
		return
	}
	if authMsg.Type != MsgTypeAuth || authMsg.SUiAddress == "" {
		h.writeControl(ctx, conn, ControlMsg{Type: MsgTypeError, Endpoint: "auth required"})
		return
	}

	suiAddr := authMsg.SUiAddress
	log.Printf("[wss-relay] client connected: %s", suiAddr[:16])

	// Allocate a UDP port for this client
	alloc, err := h.allocate(suiAddr, conn)
	if err != nil {
		log.Printf("[wss-relay] allocate failed for %s: %v", suiAddr[:16], err)
		h.writeControl(ctx, conn, ControlMsg{Type: MsgTypeError, Endpoint: err.Error()})
		return
	}
	defer h.release(suiAddr)

	// Send allocated endpoint back to client
	endpoint := fmt.Sprintf("%s:%d", h.publicIP, alloc.udpPort)
	h.writeControl(ctx, conn, ControlMsg{Type: MsgTypeAllocated, Endpoint: endpoint})
	log.Printf("[wss-relay] allocated %s for %s", endpoint, suiAddr[:16])

	// Start UDP → WSS forwarder
	go h.udpToWSS(alloc)

	// Main loop: read WSS messages from client
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("[wss-relay] client %s read error: %v", suiAddr[:16], err)
			return
		}

		switch typ {
		case websocket.MessageBinary:
			h.handleData(alloc, data)
		case websocket.MessageText:
			h.handleControl(ctx, conn, alloc, data)
		}
	}
}

// handleData forwards a WireGuard packet from WSS to the target peer's UDP endpoint.
func (h *WSSHandler) handleData(alloc *relayAllocation, data []byte) {
	peerID, wgPacket, err := DecodeDataMsg(data)
	if err != nil {
		return
	}

	peer, ok := alloc.peers[peerID]
	if !ok {
		return
	}

	_, err = alloc.udpConn.WriteTo(wgPacket, peer.target)
	if err != nil {
		log.Printf("[wss-relay] udp write to peer %d failed: %v", peerID, err)
	}
}

// handleControl processes a control message from the client.
func (h *WSSHandler) handleControl(ctx context.Context, conn *websocket.Conn, alloc *relayAllocation, data []byte) {
	var msg ControlMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	switch msg.Type {
	case MsgTypeAddPeer:
		addr, err := net.ResolveUDPAddr("udp", msg.Target)
		if err != nil {
			h.writeControl(ctx, conn, ControlMsg{Type: MsgTypeError, Endpoint: fmt.Sprintf("bad target: %v", err)})
			return
		}
		alloc.peers[msg.PeerID] = &relayPeer{
			id:       msg.PeerID,
			target:   addr,
			lastSeen: time.Now(),
		}
		alloc.addrMap[msg.Target] = msg.PeerID
		log.Printf("[wss-relay] peer %d → %s added for %s", msg.PeerID, msg.Target, alloc.suiAddr[:16])

	case MsgTypeRemovePeer:
		if peer, ok := alloc.peers[msg.PeerID]; ok {
			delete(alloc.addrMap, peer.target.String())
		}
		delete(alloc.peers, msg.PeerID)
		log.Printf("[wss-relay] peer %d removed for %s", msg.PeerID, alloc.suiAddr[:16])

	case MsgTypePing:
		h.writeControl(ctx, conn, ControlMsg{Type: MsgTypePong})
	}
}

// udpToWSS reads UDP packets on the allocated port and forwards them to the WSS client.
func (h *WSSHandler) udpToWSS(alloc *relayAllocation) {
	buf := make([]byte, 65536)
	for {
		select {
		case <-alloc.done:
			return
		default:
		}

		alloc.udpConn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, addr, err := alloc.udpConn.ReadFrom(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		// Look up which peer this UDP packet came from
		peerID, ok := alloc.addrMap[addr.String()]
		if !ok {
			// Unknown source — could be a new peer or roaming. Try to auto-discover.
			// For now, assign peer_id 0 as a catch-all.
			peerID = 0
		}

		msg := EncodeDataMsg(peerID, buf[:n])
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = alloc.wsConn.Write(ctx, websocket.MessageBinary, msg)
		cancel()
		if err != nil {
			return
		}
	}
}

// allocate assigns a UDP port from the pool for a restricted client.
func (h *WSSHandler) allocate(suiAddr string, conn *websocket.Conn) (*relayAllocation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Check if client already has an allocation (reconnect)
	if existing, ok := h.clients[suiAddr]; ok {
		close(existing.done)
		existing.udpConn.Close()
		existing.wsConn.CloseNow()
		port := existing.udpPort
		delete(h.clients, suiAddr)
		h.usedPort[port] = false
	}

	// Find a free port
	port := 0
	for p := h.portMin; p <= h.portMax; p++ {
		if !h.usedPort[p] {
			port = p
			break
		}
	}
	if port == 0 {
		return nil, fmt.Errorf("no free relay ports in range %d-%d", h.portMin, h.portMax)
	}

	// Listen on the allocated port
	udpConn, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen udp port %d: %w", port, err)
	}

	alloc := &relayAllocation{
		suiAddr: suiAddr,
		udpPort: port,
		udpConn: udpConn,
		wsConn:  conn,
		peers:   make(map[uint16]*relayPeer),
		addrMap: make(map[string]uint16),
		done:    make(chan struct{}),
	}

	h.usedPort[port] = true
	h.clients[suiAddr] = alloc
	return alloc, nil
}

// release frees a client's allocation.
func (h *WSSHandler) release(suiAddr string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	alloc, ok := h.clients[suiAddr]
	if !ok {
		return
	}

	close(alloc.done)
	alloc.udpConn.Close()
	h.usedPort[alloc.udpPort] = false
	delete(h.clients, suiAddr)
	log.Printf("[wss-relay] released port %d for %s", alloc.udpPort, suiAddr[:16])
}

func (h *WSSHandler) readControl(ctx context.Context, conn *websocket.Conn) (ControlMsg, error) {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return ControlMsg{}, err
	}
	if typ != websocket.MessageText {
		return ControlMsg{}, fmt.Errorf("expected text message, got binary")
	}
	var msg ControlMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return ControlMsg{}, fmt.Errorf("unmarshal control: %w", err)
	}
	return msg, nil
}

func (h *WSSHandler) writeControl(ctx context.Context, conn *websocket.Conn, msg ControlMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

// Close shuts down all active client connections.
func (h *WSSHandler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for addr, alloc := range h.clients {
		close(alloc.done)
		alloc.udpConn.Close()
		alloc.wsConn.CloseNow()
		h.usedPort[alloc.udpPort] = false
		delete(h.clients, addr)
	}
}
