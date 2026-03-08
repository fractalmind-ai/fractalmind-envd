package relay

import (
	"net"
	"testing"
	"time"

	stunlib "github.com/pion/stun/v3"
)

func TestNewServer(t *testing.T) {
	cfg := Config{
		ListenPort:     0, // random port
		PublicIP:       "127.0.0.1",
		MaxConnections: 50,
	}
	s := NewServer(cfg)

	if s.listenPort != 0 {
		t.Errorf("listenPort = %d, want 0", s.listenPort)
	}
	if s.publicIP != "127.0.0.1" {
		t.Errorf("publicIP = %q, want %q", s.publicIP, "127.0.0.1")
	}
	if s.capacity != 50 {
		t.Errorf("capacity = %d, want 50", s.capacity)
	}
}

func TestServer_StartClose(t *testing.T) {
	cfg := Config{
		ListenPort:     0, // OS picks a free port
		PublicIP:       "127.0.0.1",
		MaxConnections: 10,
	}
	s := NewServer(cfg)

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Close()

	// Verify load info defaults
	info := s.GetLoadInfo()
	if info.CurrentLoad != 0 {
		t.Errorf("CurrentLoad = %d, want 0", info.CurrentLoad)
	}
	if info.Capacity != 10 {
		t.Errorf("Capacity = %d, want 10", info.Capacity)
	}
	if info.AvgLatencyMs != 0 {
		t.Errorf("AvgLatencyMs = %d, want 0", info.AvgLatencyMs)
	}

	// Close
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestServer_InvalidPublicIP(t *testing.T) {
	cfg := Config{
		ListenPort:     0,
		PublicIP:       "not-an-ip",
		MaxConnections: 10,
	}
	s := NewServer(cfg)

	err := s.Start()
	if err == nil {
		s.Close()
		t.Fatal("Start() should fail with invalid public IP")
	}
}

func TestServer_UpdateLoadAndLatency(t *testing.T) {
	cfg := Config{
		ListenPort:     0,
		PublicIP:       "127.0.0.1",
		MaxConnections: 100,
	}
	s := NewServer(cfg)

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Close()

	s.UpdateLoad(42)
	s.UpdateLatency(15)

	info := s.GetLoadInfo()
	if info.CurrentLoad != 42 {
		t.Errorf("CurrentLoad = %d, want 42", info.CurrentLoad)
	}
	if info.AvgLatencyMs != 15 {
		t.Errorf("AvgLatencyMs = %d, want 15", info.AvgLatencyMs)
	}
	if info.Capacity != 100 {
		t.Errorf("Capacity = %d, want 100", info.Capacity)
	}
}

func TestStunOnlyServer_StartClose(t *testing.T) {
	s := NewStunOnlyServer(0) // random port

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Give the goroutine time to start
	time.Sleep(50 * time.Millisecond)

	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestStunOnlyServer_BindingRequest(t *testing.T) {
	s := NewStunOnlyServer(0) // random port
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Close()

	// Get the actual listen address
	serverAddr := s.conn.LocalAddr().(*net.UDPAddr)

	// Send a STUN Binding Request
	conn, err := net.DialUDP("udp4", nil, serverAddr)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn.Close()

	// Build STUN Binding Request
	req := stunlib.New()
	req.SetType(stunlib.BindingRequest)
	req.NewTransactionID()
	req.Encode()

	if _, err := conn.Write(req.Raw); err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Read response
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	// Parse STUN response
	resp := stunlib.New()
	resp.Raw = buf[:n]
	if err := resp.Decode(); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp.Type != stunlib.BindingSuccess {
		t.Errorf("response type = %v, want BindingSuccess", resp.Type)
	}
	if resp.TransactionID != req.TransactionID {
		t.Errorf("transaction ID mismatch")
	}
}

func TestServer_CloseIdempotent(t *testing.T) {
	cfg := Config{
		ListenPort:     0,
		PublicIP:       "127.0.0.1",
		MaxConnections: 10,
	}
	s := NewServer(cfg)

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}

	// Second close should be safe (turnServer already nil)
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}
}
