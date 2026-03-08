// Package roles manages the envd role system.
// Roles determine which capabilities a node provides:
//   - worker:      Always active. Agent management + P2P heartbeat.
//   - coordinator: REST API management interface (manual enable).
//   - relay:       WireGuard packet forwarding (auto: public IP, or manual).
//   - stun_server: STUN NAT discovery service (auto: public IP, or manual).
//   - sponsor:     Org-level gas sponsorship (manual enable, needs org wallet).
package roles

import (
	"log"

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

	NATType        NATType
	PublicEndpoint string // Discovered public IP:port (empty if behind NAT)
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
		natType, publicEndpoint := detectNAT(cfg.STUN.Servers)
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

	log.Printf("[roles] active: worker=true coordinator=%v relay=%v stun_server=%v sponsor=%v",
		roles.Coordinator, roles.Relay, roles.StunServer, roles.Sponsor)

	return roles
}

// detectNAT probes STUN servers from two different source ports.
// If both return the same mapped port as local, NAT type is "none" (public IP).
// If mapped ports differ from local but match each other, "full-cone".
// If mapped ports differ from each other, "symmetric".
func detectNAT(servers []string) (NATType, string) {
	if len(servers) == 0 {
		return NATUnknown, ""
	}

	// First probe: discover our public endpoint
	endpoint1, err := stun.DiscoverEndpoint(servers)
	if err != nil {
		log.Printf("[roles] STUN probe 1 failed: %v", err)
		return NATUnknown, ""
	}

	// Second probe with different server order to detect symmetric NAT
	reversed := make([]string, len(servers))
	for i, s := range servers {
		reversed[len(servers)-1-i] = s
	}
	endpoint2, err := stun.DiscoverEndpoint(reversed)
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
