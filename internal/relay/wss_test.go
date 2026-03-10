package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"
	"nhooyr.io/websocket"
)

// testSigner implements Signer for tests using a generated Ed25519 keypair.
type testSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &testSigner{priv: priv, pub: pub}
}

func (s *testSigner) Sign(data []byte) []byte {
	return ed25519.Sign(s.priv, data)
}

func (s *testSigner) PublicKeyBytes() []byte {
	return []byte(s.pub)
}

// suiAddress derives the SUI address from the test signer's public key.
func (s *testSigner) suiAddress() string {
	payload := make([]byte, 1+len(s.pub))
	payload[0] = 0x00
	copy(payload[1:], s.pub)
	hash := blake2b.Sum256(payload)
	return "0x" + hex.EncodeToString(hash[:])
}

// doAuth performs the challenge-response auth handshake on a raw WebSocket connection.
func doAuth(t *testing.T, ctx context.Context, conn *websocket.Conn, signer *testSigner) {
	t.Helper()

	// Read challenge
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text, got %v", typ)
	}
	var challenge ControlMsg
	if err := json.Unmarshal(data, &challenge); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}
	if challenge.Type != MsgTypeChallenge {
		t.Fatalf("expected challenge, got %q", challenge.Type)
	}

	// Sign and respond
	nonceBytes, _ := hex.DecodeString(challenge.Nonce)
	sig := signer.Sign(nonceBytes)
	authMsg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: signer.suiAddress(),
		PublicKey:  hex.EncodeToString(signer.PublicKeyBytes()),
		Signature:  hex.EncodeToString(sig),
	}
	authData, _ := json.Marshal(authMsg)
	if err := conn.Write(ctx, websocket.MessageText, authData); err != nil {
		t.Fatalf("write auth: %v", err)
	}
}

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
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/wg-relay"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn.CloseNow()

	signer := newTestSigner(t)
	doAuth(t, ctx, conn, signer)

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

func TestWSSHandler_AuthRequired_EmptyFields(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

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

	// Read challenge
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text, got %v", typ)
	}
	var challenge ControlMsg
	json.Unmarshal(data, &challenge)
	if challenge.Type != MsgTypeChallenge {
		t.Fatalf("expected challenge, got %q", challenge.Type)
	}

	// Send auth with missing fields
	badAuth := ControlMsg{Type: MsgTypeAuth}
	authData, _ := json.Marshal(badAuth)
	if err := conn.Write(ctx, websocket.MessageText, authData); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Should get error response
	_, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var resp ControlMsg
	json.Unmarshal(respData, &resp)
	if resp.Type != MsgTypeError {
		t.Errorf("expected error, got %q", resp.Type)
	}
}

func TestWSSHandler_AuthRequired_BadSignature(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

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

	// Read challenge
	_, data, _ := conn.Read(ctx)
	var challenge ControlMsg
	json.Unmarshal(data, &challenge)

	signer := newTestSigner(t)

	// Send auth with bad signature (sign wrong data)
	badSig := signer.Sign([]byte("wrong data"))
	authMsg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: signer.suiAddress(),
		PublicKey:  hex.EncodeToString(signer.PublicKeyBytes()),
		Signature:  hex.EncodeToString(badSig),
	}
	authData, _ := json.Marshal(authMsg)
	conn.Write(ctx, websocket.MessageText, authData)

	// Should get error
	_, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp ControlMsg
	json.Unmarshal(respData, &resp)
	if resp.Type != MsgTypeError {
		t.Errorf("expected error for bad signature, got %q", resp.Type)
	}
}

func TestWSSHandler_AuthRequired_AddressMismatch(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

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

	// Read challenge
	_, data, _ := conn.Read(ctx)
	var challenge ControlMsg
	json.Unmarshal(data, &challenge)
	nonceBytes, _ := hex.DecodeString(challenge.Nonce)

	signer := newTestSigner(t)

	// Send auth with correct signature but wrong SUI address
	sig := signer.Sign(nonceBytes)
	authMsg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: "0x0000000000000000000000000000000000000000000000000000000000000000", // wrong
		PublicKey:  hex.EncodeToString(signer.PublicKeyBytes()),
		Signature:  hex.EncodeToString(sig),
	}
	authData, _ := json.Marshal(authMsg)
	conn.Write(ctx, websocket.MessageText, authData)

	// Should get error
	_, respData, _ := conn.Read(ctx)
	var resp ControlMsg
	json.Unmarshal(respData, &resp)
	if resp.Type != MsgTypeError {
		t.Errorf("expected error for address mismatch, got %q", resp.Type)
	}
}

func TestWSSHandler_PingPong(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

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

	signer := newTestSigner(t)
	doAuth(t, ctx, conn, signer)

	// Read allocated
	conn.Read(ctx)

	// Send ping
	pingMsg := ControlMsg{Type: MsgTypePing}
	data, _ := json.Marshal(pingMsg)
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
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First client should succeed
	signer1 := newTestSigner(t)
	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn1.CloseNow()

	doAuth(t, ctx, conn1, signer1)

	_, respData, _ := conn1.Read(ctx)
	var resp1 ControlMsg
	json.Unmarshal(respData, &resp1)
	if resp1.Type != MsgTypeAllocated {
		t.Fatalf("first client: expected allocated, got %q", resp1.Type)
	}

	// Second client should fail (no ports left)
	signer2 := newTestSigner(t)
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn2.CloseNow()

	doAuth(t, ctx, conn2, signer2)

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
	signer := newTestSigner(t)
	c := NewWSSClient("wss://example.com/wg-relay", signer.suiAddress(), signer, "127.0.0.1:51820")
	if c.relayURL != "wss://example.com/wg-relay" {
		t.Errorf("relayURL = %q", c.relayURL)
	}
	if c.suiAddress != signer.suiAddress() {
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
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	signer := newTestSigner(t)
	client := NewWSSClient(wsURL, signer.suiAddress(), signer, "127.0.0.1:51820")
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
	signer := newTestSigner(t)
	client := NewWSSClient(wsURL, signer.suiAddress(), signer, "127.0.0.1:51820")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error: %v", err)
	}

	// Add a peer (use a non-loopback address)
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

func TestWSSHandler_Reconnect(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	mux := http.NewServeMux()
	mux.Handle("/wg-relay", h)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/wg-relay"
	signer := newTestSigner(t)

	// First connection
	client1 := NewWSSClient(wsURL, signer.suiAddress(), signer, "127.0.0.1:51820")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ep1, err := client1.Connect(ctx)
	cancel()
	if err != nil {
		t.Fatalf("first Connect() error: %v", err)
	}
	t.Logf("first endpoint: %s", ep1)

	client1.Close()
	time.Sleep(100 * time.Millisecond)

	// Second connection with same signer
	client2 := NewWSSClient(wsURL, signer.suiAddress(), signer, "127.0.0.1:51820")
	defer client2.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	ep2, err := client2.Connect(ctx2)
	if err != nil {
		t.Fatalf("second Connect() error: %v", err)
	}
	t.Logf("second endpoint: %s", ep2)
	if ep2 == "" {
		t.Error("second endpoint should not be empty")
	}
}

func TestWSSHandler_AddPeer_TargetValidation(t *testing.T) {
	freePort := findFreePort(t)
	h := NewWSSHandler("127.0.0.1", freePort, freePort)

	server := httptest.NewServer(h)
	defer server.Close()
	defer h.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	signer := newTestSigner(t)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	defer conn.CloseNow()

	doAuth(t, ctx, conn, signer)
	// Read allocated
	conn.Read(ctx)

	// Try to add peer with loopback target — should be rejected
	addMsg := ControlMsg{Type: MsgTypeAddPeer, PeerID: 1, Target: "127.0.0.1:51820"}
	data, _ := json.Marshal(addMsg)
	conn.Write(ctx, websocket.MessageText, data)

	_, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp ControlMsg
	json.Unmarshal(respData, &resp)
	if resp.Type != MsgTypeError {
		t.Errorf("expected error for loopback target, got %q", resp.Type)
	}

	// Try to add peer with unspecified target
	addMsg2 := ControlMsg{Type: MsgTypeAddPeer, PeerID: 2, Target: "0.0.0.0:51820"}
	data2, _ := json.Marshal(addMsg2)
	conn.Write(ctx, websocket.MessageText, data2)

	_, respData2, _ := conn.Read(ctx)
	var resp2 ControlMsg
	json.Unmarshal(respData2, &resp2)
	if resp2.Type != MsgTypeError {
		t.Errorf("expected error for unspecified target, got %q", resp2.Type)
	}
}

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		target  string
		wantErr bool
	}{
		{"10.0.0.1:51820", false},
		{"192.168.1.1:51820", false},
		{"8.8.8.8:443", false},
		{"127.0.0.1:51820", true},  // loopback
		{"0.0.0.0:51820", true},    // unspecified
		{"not-ip:1234", true},      // invalid IP
		{"badformat", true},        // no port
	}

	for _, tt := range tests {
		err := validateTarget(tt.target)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateTarget(%q) error=%v, wantErr=%v", tt.target, err, tt.wantErr)
		}
	}
}

func TestVerifyAuth_Valid(t *testing.T) {
	signer := newTestSigner(t)
	nonce := make([]byte, 32)
	rand.Read(nonce)

	sig := signer.Sign(nonce)
	msg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: signer.suiAddress(),
		PublicKey:  hex.EncodeToString(signer.PublicKeyBytes()),
		Signature:  hex.EncodeToString(sig),
	}

	if err := verifyAuth(msg, nonce); err != nil {
		t.Errorf("verifyAuth() should pass for valid auth: %v", err)
	}
}

func TestVerifyAuth_InvalidSignature(t *testing.T) {
	signer := newTestSigner(t)
	nonce := make([]byte, 32)
	rand.Read(nonce)

	msg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: signer.suiAddress(),
		PublicKey:  hex.EncodeToString(signer.PublicKeyBytes()),
		Signature:  hex.EncodeToString(make([]byte, 64)), // zero sig
	}

	if err := verifyAuth(msg, nonce); err == nil {
		t.Error("verifyAuth() should fail for invalid signature")
	}
}

func TestVerifyAuth_WrongAddress(t *testing.T) {
	signer := newTestSigner(t)
	nonce := make([]byte, 32)
	rand.Read(nonce)

	sig := signer.Sign(nonce)
	msg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: "0x0000000000000000000000000000000000000000000000000000000000000000",
		PublicKey:  hex.EncodeToString(signer.PublicKeyBytes()),
		Signature:  hex.EncodeToString(sig),
	}

	if err := verifyAuth(msg, nonce); err == nil {
		t.Error("verifyAuth() should fail for wrong address")
	}
}

func TestVerifyAuth_BadPublicKeyHex(t *testing.T) {
	nonce := make([]byte, 32)
	rand.Read(nonce)

	msg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: "0xabc",
		PublicKey:  "not-valid-hex",
		Signature:  "abcd",
	}

	if err := verifyAuth(msg, nonce); err == nil {
		t.Error("verifyAuth() should fail for bad hex")
	}
}

func TestVerifyAuth_BadPublicKeySize(t *testing.T) {
	nonce := make([]byte, 32)
	rand.Read(nonce)

	msg := ControlMsg{
		Type:       MsgTypeAuth,
		SUiAddress: "0xabc",
		PublicKey:  hex.EncodeToString([]byte("tooshort")),
		Signature:  hex.EncodeToString(make([]byte, 64)),
	}

	if err := verifyAuth(msg, nonce); err == nil {
		t.Error("verifyAuth() should fail for bad key size")
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
