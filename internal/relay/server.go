// Package relay implements the built-in STUN Server and Relay (TURN) Server.
// On public IP nodes, both auto-enable on the same UDP port (:3478).
// STUN handles NAT discovery requests; Relay forwards WireGuard packets
// when P2P fails (double symmetric NAT).
package relay

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"

	stunlib "github.com/pion/stun/v3"
	"github.com/pion/turn/v4"
)

// Server manages the combined STUN + Relay server on a single UDP port.
type Server struct {
	mu            sync.Mutex
	listenPort    int
	publicIP      string
	maxConns      int
	turnServer    *turn.Server
	conn          net.PacketConn
	currentLoad   atomic.Uint64
	capacity      uint64
	avgLatencyMs  atomic.Uint64
	authHandler   func(username, realm string, srcAddr net.Addr) ([]byte, bool)
}

// Config holds relay server configuration.
type Config struct {
	ListenPort     int
	PublicIP       string // Discovered public IP
	MaxConnections int
	AuthHandler    func(username, realm string, srcAddr net.Addr) ([]byte, bool)
}

// LoadInfo returns current relay load metrics for P2P heartbeat broadcast.
type LoadInfo struct {
	CurrentLoad  uint64
	Capacity     uint64
	AvgLatencyMs uint64
}

// NewServer creates a new combined STUN + Relay server.
func NewServer(cfg Config) *Server {
	return &Server{
		listenPort:  cfg.ListenPort,
		publicIP:    cfg.PublicIP,
		maxConns:    cfg.MaxConnections,
		capacity:    uint64(cfg.MaxConnections),
		authHandler: cfg.AuthHandler,
	}
}

// Start begins listening for STUN and TURN requests on the configured UDP port.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	addr := fmt.Sprintf("0.0.0.0:%d", s.listenPort)
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return fmt.Errorf("listen udp %s: %w", addr, err)
	}
	s.conn = conn

	// Parse public IP for relay address
	relayIP := net.ParseIP(s.publicIP)
	if relayIP == nil {
		conn.Close()
		return fmt.Errorf("invalid public IP: %s", s.publicIP)
	}

	// Configure TURN server with built-in STUN handling.
	// pion/turn handles STUN Binding Requests automatically when
	// they arrive on the same UDP listener.
	turnServer, err := turn.NewServer(turn.ServerConfig{
		Realm: "fractalmind",
		AuthHandler: func(username, realm string, srcAddr net.Addr) ([]byte, bool) {
			if s.authHandler != nil {
				return s.authHandler(username, realm, srcAddr)
			}
			// Default: accept all org members (actual auth will be added in KR5)
			return turn.GenerateAuthKey(username, realm, username), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: conn,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: relayIP,
					Address:      "0.0.0.0",
				},
			},
		},
	})
	if err != nil {
		conn.Close()
		return fmt.Errorf("create turn server: %w", err)
	}

	s.turnServer = turnServer
	log.Printf("[relay] STUN + Relay server started on %s (public IP: %s, max conns: %d)",
		addr, s.publicIP, s.maxConns)

	return nil
}

// Close gracefully shuts down the server.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.turnServer != nil {
		if err := s.turnServer.Close(); err != nil {
			return fmt.Errorf("close turn server: %w", err)
		}
		s.turnServer = nil
	}
	log.Printf("[relay] server stopped")
	return nil
}

// GetLoadInfo returns current relay metrics for P2P heartbeat broadcast.
func (s *Server) GetLoadInfo() LoadInfo {
	return LoadInfo{
		CurrentLoad:  s.currentLoad.Load(),
		Capacity:     s.capacity,
		AvgLatencyMs: s.avgLatencyMs.Load(),
	}
}

// UpdateLoad updates the current load metric (called from connection tracking).
func (s *Server) UpdateLoad(load uint64) {
	s.currentLoad.Store(load)
}

// UpdateLatency updates the average latency metric.
func (s *Server) UpdateLatency(latencyMs uint64) {
	s.avgLatencyMs.Store(latencyMs)
}

// StunOnlyServer is a lightweight STUN-only server for nodes that
// need STUN but not full relay capability.
type StunOnlyServer struct {
	conn       net.PacketConn
	listenPort int
	done       chan struct{}
}

// NewStunOnlyServer creates a STUN-only server (no relay).
func NewStunOnlyServer(listenPort int) *StunOnlyServer {
	return &StunOnlyServer{
		listenPort: listenPort,
		done:       make(chan struct{}),
	}
}

// Start begins handling STUN Binding Requests.
func (s *StunOnlyServer) Start() error {
	addr := fmt.Sprintf("0.0.0.0:%d", s.listenPort)
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return fmt.Errorf("listen udp %s: %w", addr, err)
	}
	s.conn = conn

	go s.serve()

	log.Printf("[stun-server] STUN server started on %s", addr)
	return nil
}

func (s *StunOnlyServer) serve() {
	buf := make([]byte, 1500)
	for {
		select {
		case <-s.done:
			return
		default:
		}

		n, addr, err := s.conn.ReadFrom(buf)
		if err != nil {
			continue
		}

		// Parse STUN message
		msg := stunlib.New()
		msg.Raw = buf[:n]
		if err := msg.Decode(); err != nil {
			continue // Not a STUN message, ignore
		}

		if msg.Type != stunlib.BindingRequest {
			continue
		}

		// Build STUN Binding Response
		resp := stunlib.New()
		resp.SetType(stunlib.BindingSuccess)
		resp.TransactionID = msg.TransactionID

		// Set XOR-Mapped-Address from the source address
		udpAddr, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		xorAddr := stunlib.XORMappedAddress{
			IP:   udpAddr.IP,
			Port: udpAddr.Port,
		}
		xorAddr.AddTo(resp)
		resp.Encode()

		s.conn.WriteTo(resp.Raw, addr)
	}
}

// Close shuts down the STUN server.
func (s *StunOnlyServer) Close() error {
	close(s.done)
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
