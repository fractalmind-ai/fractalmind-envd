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
	mu         sync.Mutex
	cfg        config.WireGuardConfig
	wg         WGConfigurator
	privateKey wgtypes.Key
	publicKey  wgtypes.Key
	// suiAddr → wg public key mapping for tracked peers
	peers map[string]wgtypes.Key
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
		cfg:        cfg,
		wg:         wg,
		privateKey: key,
		publicKey:  pubKey,
		peers:      make(map[string]wgtypes.Key),
	}, nil
}

// PublicKey returns the 32-byte WireGuard public key for SUI registration.
func (m *Manager) PublicKey() []byte {
	key := m.publicKey
	return key[:]
}

// Setup configures the WireGuard interface with the private key and listen port.
func (m *Manager) Setup() error {
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
	m.mu.Lock()
	defer m.mu.Unlock()

	var peerKey wgtypes.Key
	copy(peerKey[:], pubkey)

	peerCfg := wgtypes.PeerConfig{
		PublicKey:                   peerKey,
		ReplaceAllowedIPs:          true,
		AllowedIPs:                 []net.IPNet{vpnIPNet(suiAddr)},
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
	log.Printf("[wg] added peer %s (endpoint=%v)", suiAddr[:10], endpoints)
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
				Remove:   true,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("remove peer %s: %w", suiAddr[:10], err)
	}

	delete(m.peers, suiAddr)
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
				AllowedIPs:        []net.IPNet{vpnIPNet(suiAddr)},
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

// VPNAddress returns the deterministic VPN IP for a SUI address.
// Uses SHA256(suiAddr) mapped to 10.100.X.Y/32.
func VPNAddress(suiAddr string) netip.Addr {
	hash := sha256.Sum256([]byte(suiAddr))
	x := hash[0]
	y := hash[1]
	if x == 0 {
		x = 1
	}
	if y == 0 {
		y = 1
	}
	return netip.AddrFrom4([4]byte{10, 100, x, y})
}

func vpnIPNet(suiAddr string) net.IPNet {
	addr := VPNAddress(suiAddr)
	ip := addr.As4()
	return net.IPNet{
		IP:   net.IP(ip[:]),
		Mask: net.CIDRMask(32, 32),
	}
}

func ptrDuration(d time.Duration) *time.Duration {
	return &d
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
