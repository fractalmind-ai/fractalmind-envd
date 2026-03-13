// Package roles manages the envd role system.
// Roles determine which capabilities a node provides:
//   - worker:      Always active. Agent management + P2P heartbeat.
//   - coordinator: Embedded REST + WebSocket control plane (manual enable).
//   - relay:       WireGuard packet forwarding (auto: public IP, or manual).
//   - stun_server: STUN NAT discovery service (auto: public IP, or manual).
//   - sponsor:     Org-level gas sponsorship (manual enable, needs org wallet).
package roles

import (
	"log"
	"net"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/config"
	"github.com/fractalmind-ai/fractalmind-envd/internal/stun"
)

// NATType describes the NAT environment of this node.
type NATType int

const (
	NATNone      NATType = iota // Public IP, no NAT
	NATFull                     // Full-cone NAT (good for P2P)
	NATSymmetric                // Symmetric NAT (P2P difficult, needs relay)
	NATUnknown                  // Detection failed
)

func (n NATType) String() string {
	switch n {
	case NATNone:
		return "none (public IP)"
	case NATFull:
		return "full-cone"
	case NATSymmetric:
		return "symmetric"
	default:
		return "unknown"
	}
}

// ActiveRoles holds the resolved roles for this node.
type ActiveRoles struct {
	Worker      bool
	Coordinator bool
	Relay       bool
	StunServer  bool
	Sponsor     bool

	NATType           NATType
	PublicEndpoint    string // Discovered public IP:port (empty if behind NAT)
	TCPFallbackActive bool   // True when UDP is completely blocked and WSS relay is needed
}

// Resolve determines which roles are active based on config and NAT detection.
func Resolve(cfg *config.Config) *ActiveRoles {
	roles := &ActiveRoles{
		Worker: true, // always on
	}

	// Manual roles from config
	roles.Coordinator = cfg.Roles.Coordinator
	roles.Sponsor = cfg.Roles.Sponsor

	// NAT detection for auto roles (relay + stun server)
	if cfg.STUN.Enabled {
		natType, publicEndpoint := detectNAT(cfg.STUN.Servers, cfg.STUN.BindAddress)
		roles.NATType = natType
		roles.PublicEndpoint = publicEndpoint

		log.Printf("[roles] NAT type: %s", natType)
		if publicEndpoint != "" {
			log.Printf("[roles] public endpoint: %s", publicEndpoint)
		}
	} else {
		roles.NATType = NATUnknown
	}

	// Resolve relay role
	if cfg.Roles.Relay != nil {
		roles.Relay = *cfg.Roles.Relay // manual override
	} else {
		roles.Relay = roles.NATType == NATNone // auto: public IP → relay
	}

	// Resolve stun server role
	if cfg.Roles.StunServer != nil {
		roles.StunServer = *cfg.Roles.StunServer // manual override
	} else {
		roles.StunServer = roles.NATType == NATNone // auto: public IP → stun server
	}

	// Validate sponsor requires org wallet
	if roles.Sponsor && cfg.Sponsor.OrgWalletPath == "" {
		log.Printf("[roles] WARNING: sponsor role enabled but no org_wallet_path configured, disabling")
		roles.Sponsor = false
	}

	// TCP fallback detection: when STUN fails AND tcp_fallback is configured
	// relay_url is no longer required — the node can auto-discover relays from SUI chain
	if roles.NATType == NATUnknown && cfg.Relay.TCPFallback {
		// STUN failed — probe UDP connectivity to confirm UDP is blocked
		if !probeUDP(cfg.STUN.Servers) {
			roles.TCPFallbackActive = true
			log.Printf("[roles] UDP completely blocked — WSS relay fallback activated")
		} else {
			log.Printf("[roles] STUN failed but UDP reachable — skipping WSS fallback")
		}
	}

	log.Printf("[roles] active: worker=true coordinator=%v relay=%v stun_server=%v sponsor=%v",
		roles.Coordinator, roles.Relay, roles.StunServer, roles.Sponsor)

	return roles
}

// discoverEndpointFunc wraps stun.DiscoverEndpoint for testability.
var discoverEndpointFunc = stun.DiscoverEndpoint

// detectNAT probes STUN servers from two different source ports.
// If both return the same mapped port as local, NAT type is "none" (public IP).
// If mapped ports differ from local but match each other, "full-cone".
// If mapped ports differ from each other, "symmetric".
func detectNAT(servers []string, bindAddr string) (NATType, string) {
	if len(servers) == 0 {
		return NATUnknown, ""
	}

	// First probe: discover our public endpoint
	endpoint1, err := discoverEndpointFunc(servers, bindAddr)
	if err != nil {
		log.Printf("[roles] STUN probe 1 failed: %v", err)
		return NATUnknown, ""
	}

	// Second probe with different server order to detect symmetric NAT
	reversed := make([]string, len(servers))
	for i, s := range servers {
		reversed[len(servers)-1-i] = s
	}
	endpoint2, err := discoverEndpointFunc(reversed, bindAddr)
	if err != nil {
		// Single successful probe — assume non-symmetric
		log.Printf("[roles] STUN probe 2 failed, assuming full-cone: %v", err)
		return NATFull, endpoint1
	}

	// Compare endpoints
	if endpoint1 == endpoint2 {
		// Same mapped address from different servers — could be public IP or full-cone
		// Check if the mapped port matches our WG listen port (heuristic for public IP)
		// For now, treat consistent mapping as NATNone (public IP)
		return NATNone, endpoint1
	}

	// Different mapped addresses → symmetric NAT
	return NATSymmetric, endpoint1
}

// probeUDP sends a single UDP packet to known hosts to check if outbound UDP works.
// Returns true if at least one UDP probe gets a response.
func probeUDP(stunServers []string) bool {
	for _, server := range stunServers {
		// Strip "stun:" prefix if present
		addr := server
		if len(addr) > 5 && addr[:5] == "stun:" {
			addr = addr[5:]
		}

		conn, err := net.DialTimeout("udp", addr, 3*time.Second)
		if err != nil {
			continue
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		// Send a minimal STUN binding request (just needs any response)
		conn.Write([]byte{0x00, 0x01, 0x00, 0x00}) // STUN method + length
		buf := make([]byte, 64)
		_, err = conn.Read(buf)
		conn.Close()
		if err == nil {
			return true // Got a UDP response
		}
	}
	return false
}
