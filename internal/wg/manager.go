package wg

import (
	"crypto/sha256"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/config"
	"github.com/fractalmind-ai/fractalmind-envd/internal/sui"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// WGConfigurator abstracts wgctrl.Client for testability.
type WGConfigurator interface {
	ConfigureDevice(name string, cfg wgtypes.Config) error
	Device(name string) (*wgtypes.Device, error)
	Close() error
}

// Manager manages the WireGuard interface and peer configuration.
type Manager struct {
	mu                  sync.Mutex
	cfg                 config.WireGuardConfig
	wg                  WGConfigurator
	privateKey          wgtypes.Key
	publicKey           wgtypes.Key
	ensureInterface     func(name string) error
	assignInterfaceAddr func(name, cidr string) error
	// suiAddr → wg public key mapping for tracked peers
	peers map[string]wgtypes.Key
	// vpnIP → suiAddr for collision detection
	vpnIPs map[netip.Addr]string
}

// NewManager creates a WireGuard manager.
func NewManager(cfg config.WireGuardConfig, wg WGConfigurator) (*Manager, error) {
	key, err := loadOrGenerateWGKey(cfg.KeypairPath)
	if err != nil {
		return nil, fmt.Errorf("load wg keypair: %w", err)
	}

	pubKey := key.PublicKey()
	log.Printf("[wg] public key: %s", pubKey.String())

	return &Manager{
		cfg:                 cfg,
		wg:                  wg,
		privateKey:          key,
		publicKey:           pubKey,
		ensureInterface:     ensureInterface,
		assignInterfaceAddr: assignInterfaceAddr,
		peers:               make(map[string]wgtypes.Key),
		vpnIPs:              make(map[netip.Addr]string),
	}, nil
}

// PublicKey returns the 32-byte WireGuard public key for SUI registration.
func (m *Manager) PublicKey() []byte {
	key := m.publicKey
	return key[:]
}

// Setup creates the WireGuard interface (if absent) and configures it.
func (m *Manager) Setup() error {
	if err := m.ensureInterface(m.cfg.InterfaceName); err != nil {
		return fmt.Errorf("ensure interface: %w", err)
	}

	port := m.cfg.ListenPort
	err := m.wg.ConfigureDevice(m.cfg.InterfaceName, wgtypes.Config{
		PrivateKey: &m.privateKey,
		ListenPort: &port,
	})
	if err != nil {
		return fmt.Errorf("configure device: %w", err)
	}

	log.Printf("[wg] interface %s configured (port %d)", m.cfg.InterfaceName, port)
	return nil
}

// AddPeer adds a WireGuard peer configuration.
func (m *Manager) AddPeer(suiAddr string, pubkey []byte, endpoints []string) error {
	if len(pubkey) != 32 {
		return fmt.Errorf("invalid WG pubkey for %s: expected 32 bytes, got %d", suiAddr[:10], len(pubkey))
	}
	if isZeroKey(pubkey) {
		return fmt.Errorf("invalid WG pubkey for %s: all-zero key", suiAddr[:10])
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	vpnAddr := m.resolveVPNAddr(suiAddr)
	if !vpnAddr.IsValid() {
		return fmt.Errorf("cannot allocate VPN IP for %s: all slots collide", suiAddr[:10])
	}

	var peerKey wgtypes.Key
	copy(peerKey[:], pubkey)

	vpnNet := vpnAddrToIPNet(vpnAddr)
	peerCfg := wgtypes.PeerConfig{
		PublicKey:                   peerKey,
		ReplaceAllowedIPs:           true,
		AllowedIPs:                  []net.IPNet{vpnNet},
		PersistentKeepaliveInterval: ptrDuration(25 * time.Second),
	}

	// Use first endpoint if available
	if len(endpoints) > 0 {
		addr, err := net.ResolveUDPAddr("udp", endpoints[0])
		if err == nil {
			peerCfg.Endpoint = addr
		}
	}

	err := m.wg.ConfigureDevice(m.cfg.InterfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{peerCfg},
	})
	if err != nil {
		return fmt.Errorf("add peer %s: %w", suiAddr[:10], err)
	}

	m.peers[suiAddr] = peerKey
	m.vpnIPs[vpnAddr] = suiAddr
	log.Printf("[wg] added peer %s (endpoint=%v, vpn=%s)", suiAddr[:10], endpoints, vpnAddr)
	return nil
}

// RemovePeer removes a WireGuard peer.
func (m *Manager) RemovePeer(suiAddr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	peerKey, ok := m.peers[suiAddr]
	if !ok {
		return nil // not tracked
	}

	err := m.wg.ConfigureDevice(m.cfg.InterfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey: peerKey,
				Remove:    true,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("remove peer %s: %w", suiAddr[:10], err)
	}

	delete(m.peers, suiAddr)
	// Clean up VPN IP reservation
	for ip, addr := range m.vpnIPs {
		if addr == suiAddr {
			delete(m.vpnIPs, ip)
			break
		}
	}
	log.Printf("[wg] removed peer %s", suiAddr[:10])
	return nil
}

// UpdatePeerEndpoint updates a peer's endpoint.
func (m *Manager) UpdatePeerEndpoint(suiAddr string, endpoints []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	peerKey, ok := m.peers[suiAddr]
	if !ok {
		return fmt.Errorf("peer %s not tracked", suiAddr[:10])
	}

	if len(endpoints) == 0 {
		return nil
	}

	addr, err := net.ResolveUDPAddr("udp", endpoints[0])
	if err != nil {
		return fmt.Errorf("resolve endpoint %s: %w", endpoints[0], err)
	}

	err = m.wg.ConfigureDevice(m.cfg.InterfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey:         peerKey,
				UpdateOnly:        true,
				Endpoint:          addr,
				ReplaceAllowedIPs: true,
				AllowedIPs:        []net.IPNet{vpnAddrToIPNet(m.resolveVPNAddr(suiAddr))},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("update peer endpoint: %w", err)
	}

	log.Printf("[wg] updated peer %s endpoint=%s", suiAddr[:10], endpoints[0])
	return nil
}

// SyncPeers reconciles the WireGuard peer list with the SUI peer list.
// Adds missing peers, removes stale peers, and updates changed endpoints.
func (m *Manager) SyncPeers(peers []sui.PeerInfo) error {
	desired := make(map[string]sui.PeerInfo)
	for _, p := range peers {
		desired[p.Address] = p
	}

	// Remove stale peers (in WG but not in desired)
	for addr := range m.peers {
		if _, ok := desired[addr]; !ok {
			if err := m.RemovePeer(addr); err != nil {
				log.Printf("[wg] failed to remove stale peer %s: %v", addr[:10], err)
			}
		}
	}

	// Add missing or update changed peers
	for addr, p := range desired {
		if isZeroKey(p.WireGuardPubKey) {
			log.Printf("[wg] skipping peer %s: no WireGuard key", addr[:10])
			continue
		}
		if _, tracked := m.peers[addr]; !tracked {
			if err := m.AddPeer(addr, p.WireGuardPubKey, p.Endpoints); err != nil {
				log.Printf("[wg] failed to add peer %s: %v", addr[:10], err)
			}
		} else {
			if err := m.UpdatePeerEndpoint(addr, p.Endpoints); err != nil {
				log.Printf("[wg] failed to update peer %s: %v", addr[:10], err)
			}
		}
	}

	return nil
}

// Close removes all peers and closes the WireGuard client.
func (m *Manager) Close() error {
	m.mu.Lock()
	addrs := make([]string, 0, len(m.peers))
	for addr := range m.peers {
		addrs = append(addrs, addr)
	}
	m.mu.Unlock()

	for _, addr := range addrs {
		if err := m.RemovePeer(addr); err != nil {
			log.Printf("[wg] failed to remove peer on close: %v", err)
		}
	}

	return m.wg.Close()
}

// AssignIP assigns a deterministic VPN IP to the WireGuard interface based on
// the node's SUI address. The IP is derived via VPNAddress(suiAddr).
func (m *Manager) AssignIP(suiAddr string) error {
	m.mu.Lock()
	addr := m.resolveVPNAddr(suiAddr)
	m.mu.Unlock()
	if !addr.IsValid() {
		return fmt.Errorf("cannot allocate VPN IP for self: all slots collide")
	}
	cidr := addr.String() + "/16"
	if err := m.assignInterfaceAddr(m.cfg.InterfaceName, cidr); err != nil {
		return fmt.Errorf("assign IP %s: %w", cidr, err)
	}
	log.Printf("[wg] assigned %s to %s", cidr, m.cfg.InterfaceName)
	return nil
}

// VPNAddress returns the deterministic VPN IP for a SUI address.
// Uses SHA256(suiAddr) mapped to 10.87.X.Y/32.
// The round parameter enables deterministic rehashing on collision:
// round 0 uses hash[0:2], round 1 uses hash[2:4], etc.
func VPNAddress(suiAddr string, round int) netip.Addr {
	hash := sha256.Sum256([]byte(suiAddr))
	offset := round * 2
	if offset+1 >= len(hash) {
		// Exhausted hash bytes — do a secondary hash with the round as salt
		salt := fmt.Sprintf("%s:%d", suiAddr, round)
		hash = sha256.Sum256([]byte(salt))
		offset = 0
	}
	x := hash[offset]
	y := hash[offset+1]
	if x == 0 {
		x = 1
	}
	if y == 0 {
		y = 1
	}
	return netip.AddrFrom4([4]byte{10, 87, x, y})
}

// resolveVPNAddr finds a non-colliding VPN IP for suiAddr.
// Must be called with m.mu held.
func (m *Manager) resolveVPNAddr(suiAddr string) netip.Addr {
	// If this suiAddr already has an IP reserved, return it
	for ip, owner := range m.vpnIPs {
		if owner == suiAddr {
			return ip
		}
	}

	const maxRounds = 16
	for round := 0; round < maxRounds; round++ {
		addr := VPNAddress(suiAddr, round)
		owner, taken := m.vpnIPs[addr]
		if !taken || owner == suiAddr {
			m.vpnIPs[addr] = suiAddr
			if round > 0 {
				log.Printf("[wg] VPN IP collision for %s resolved at round %d → %s", suiAddr[:10], round, addr)
			}
			return addr
		}
		log.Printf("[wg] VPN IP collision: %s wanted by %s but owned by %s (round %d)", addr, suiAddr[:10], owner[:10], round)
	}
	return netip.Addr{} // invalid — caller must handle
}

func vpnAddrToIPNet(addr netip.Addr) net.IPNet {
	ip := addr.As4()
	return net.IPNet{
		IP:   net.IP(ip[:]),
		Mask: net.CIDRMask(32, 32),
	}
}

func ptrDuration(d time.Duration) *time.Duration {
	return &d
}

// isZeroKey returns true if the WireGuard public key is empty or all zeros.
func isZeroKey(key []byte) bool {
	if len(key) == 0 {
		return true
	}
	for _, b := range key {
		if b != 0 {
			return false
		}
	}
	return true
}

func loadOrGenerateWGKey(path string) (wgtypes.Key, error) {
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return wgtypes.Key{}, fmt.Errorf("expand home dir: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}

	data, err := os.ReadFile(path)
	if err == nil {
		return wgtypes.ParseKey(string(data))
	}

	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("generate wg key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return wgtypes.Key{}, fmt.Errorf("create key dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(key.String()), 0600); err != nil {
		return wgtypes.Key{}, fmt.Errorf("write wg key: %w", err)
	}

	return key, nil
}
