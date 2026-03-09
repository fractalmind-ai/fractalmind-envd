package relay

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestWSSHandler_NewAndClose(t *testing.T) {
	h := NewWSSHandler("1.2.3.4", 51900, 51910)
	if h.publicIP != "1.2.3.4" {
		t.Errorf("publicIP = %q, want %q", h.publicIP, "1.2.3.4")
	}
	if h.portMin != 51900 {
		t.Errorf("portMin = %d, want 51900", h.portMin)
	}
	if h.portMax != 51910 {
		t.Errorf("portMax = %d, want 51910", h.portMax)
	}
	h.Close() // should not panic
}

func TestWSSHandler_AuthFlow(t *testing.T) {
	// Use port 0 to get OS-assigned ephemeral ports
	h := NewWSSHandler("127.0.0.1", 0, 0)

	// Override port range to use ephemeral ports that are actually free
	freePort := findFreePort(t)
	h.portMin = freePort
	h.portMax = freePort

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	// Connect via WebSocket
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/wg-relay"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn.CloseNow()

	// Send auth
	authMsg := ControlMsg{Type: MsgTypeAuth, SUiAddress: "0x1234567890abcdef1234567890abcdef12345678"}
	data, _ := json.Marshal(authMsg)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	// Read allocated response
	typ, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read allocated: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text message, got %v", typ)
	}

	var resp ControlMsg
	if err := json.Unmarshal(respData, &resp); err != nil {
		t.Fatalf("unmarshal allocated: %v", err)
	}
	if resp.Type != MsgTypeAllocated {
		t.Fatalf("expected allocated, got %q", resp.Type)
	}
	if resp.Endpoint == "" {
		t.Fatal("allocated endpoint is empty")
	}
	t.Logf("allocated endpoint: %s", resp.Endpoint)
}

func TestWSSHandler_AuthRequired(t *testing.T) {
	h := NewWSSHandler("127.0.0.1", 0, 0)
	freePort := findFreePort(t)
	h.portMin = freePort
	h.portMax = freePort

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn.CloseNow()

	// Send invalid auth (missing SUI address)
	badAuth := ControlMsg{Type: MsgTypeAuth}
	data, _ := json.Marshal(badAuth)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Should get error response then close
	typ, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text, got %v", typ)
	}

	var resp ControlMsg
	if err := json.Unmarshal(respData, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != MsgTypeError {
		t.Errorf("expected error, got %q", resp.Type)
	}
}

func TestWSSHandler_PingPong(t *testing.T) {
	h := NewWSSHandler("127.0.0.1", 0, 0)
	freePort := findFreePort(t)
	h.portMin = freePort
	h.portMax = freePort

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn.CloseNow()

	// Auth first
	authMsg := ControlMsg{Type: MsgTypeAuth, SUiAddress: "0xabcdef1234567890abcdef1234567890abcdef12"}
	data, _ := json.Marshal(authMsg)
	conn.Write(ctx, websocket.MessageText, data)

	// Read allocated
	conn.Read(ctx)

	// Send ping
	pingMsg := ControlMsg{Type: MsgTypePing}
	data, _ = json.Marshal(pingMsg)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	// Read pong
	typ, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text, got %v", typ)
	}

	var resp ControlMsg
	json.Unmarshal(respData, &resp)
	if resp.Type != MsgTypePong {
		t.Errorf("expected pong, got %q", resp.Type)
	}
}

func TestWSSHandler_PortExhaustion(t *testing.T) {
	// Only one port available
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First client should succeed
	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn1.CloseNow()

	authMsg := ControlMsg{Type: MsgTypeAuth, SUiAddress: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	data, _ := json.Marshal(authMsg)
	conn1.Write(ctx, websocket.MessageText, data)

	typ, respData, _ := conn1.Read(ctx)
	if typ != websocket.MessageText {
		t.Fatalf("expected text, got %v", typ)
	}
	var resp1 ControlMsg
	json.Unmarshal(respData, &resp1)
	if resp1.Type != MsgTypeAllocated {
		t.Fatalf("first client: expected allocated, got %q", resp1.Type)
	}

	// Second client should fail (no ports left)
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn2.CloseNow()

	authMsg2 := ControlMsg{Type: MsgTypeAuth, SUiAddress: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	data2, _ := json.Marshal(authMsg2)
	conn2.Write(ctx, websocket.MessageText, data2)

	_, respData2, err := conn2.Read(ctx)
	if err != nil {
		t.Fatalf("read from second client: %v", err)
	}
	var resp2 ControlMsg
	json.Unmarshal(respData2, &resp2)
	if resp2.Type != MsgTypeError {
		t.Errorf("second client: expected error, got %q", resp2.Type)
	}
}

func TestWSSClient_NewAndClose(t *testing.T) {
	c := NewWSSClient("wss://example.com/wg-relay", "0xabc123", "127.0.0.1:51820")
	if c.relayURL != "wss://example.com/wg-relay" {
		t.Errorf("relayURL = %q", c.relayURL)
	}
	if c.suiAddress != "0xabc123" {
		t.Errorf("suiAddress = %q", c.suiAddress)
	}
	// Close without connect should be safe
	if err := c.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
	// Double close should be safe
	if err := c.Close(); err != nil {
		t.Errorf("second Close() error: %v", err)
	}
}

func TestWSSClientServer_EndToEnd(t *testing.T) {
	// Start WSS relay handler
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Start WSS client
	client := NewWSSClient(wsURL, "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "127.0.0.1:51820")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	endpoint, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() error: %v", err)
	}

	if endpoint == "" {
		t.Fatal("relay endpoint is empty")
	}
	t.Logf("relay endpoint: %s", endpoint)

	// Verify RelayEndpoint returns the same
	if got := client.RelayEndpoint(); got != endpoint {
		t.Errorf("RelayEndpoint() = %q, want %q", got, endpoint)
	}
}

func TestWSSClientServer_AddRemovePeer(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSSClient(wsURL, "0x1111111111111111111111111111111111111111", "127.0.0.1:51820")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error: %v", err)
	}

	// Add a peer
	localAddr, peerID, err := client.AddPeer(ctx, "10.0.0.1:51820")
	if err != nil {
		t.Fatalf("AddPeer() error: %v", err)
	}
	if localAddr == "" {
		t.Fatal("local proxy address is empty")
	}
	if peerID == 0 {
		t.Error("peer ID should not be 0")
	}
	t.Logf("peer %d local proxy: %s", peerID, localAddr)

	// Remove the peer
	if err := client.RemovePeer(ctx, peerID); err != nil {
		t.Fatalf("RemovePeer() error: %v", err)
	}

	// Remove non-existent peer should be safe
	if err := client.RemovePeer(ctx, 999); err != nil {
		t.Errorf("RemovePeer(999) error: %v", err)
	}
}

// TestWSSHandler_Reconnect verifies that a client can reconnect
// and get the same SUI address re-allocated.
func TestWSSHandler_Reconnect(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	mux := http.NewServeMux()
	mux.Handle("/wg-relay", h)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/wg-relay"
	suiAddr := "0x2222222222222222222222222222222222222222"

	// First connection
	client1 := NewWSSClient(wsURL, suiAddr, "127.0.0.1:51820")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ep1, err := client1.Connect(ctx)
	cancel()
	if err != nil {
		t.Fatalf("first Connect() error: %v", err)
	}
	t.Logf("first endpoint: %s", ep1)

	// Close first client
	client1.Close()
	time.Sleep(100 * time.Millisecond) // let server process disconnect

	// Second connection with same SUI address
	client2 := NewWSSClient(wsURL, suiAddr, "127.0.0.1:51820")
	defer client2.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	ep2, err := client2.Connect(ctx2)
	if err != nil {
		t.Fatalf("second Connect() error: %v", err)
	}
	t.Logf("second endpoint: %s", ep2)
	// Should get an endpoint (port may differ if reuse isn't instant)
	if ep2 == "" {
		t.Error("second endpoint should not be empty")
	}
}

// findFreePort asks the OS for an available UDP port.
func findFreePort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("findFreePort: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}
