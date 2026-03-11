package wg

import (
	"net/netip"
	"testing"

	"github.com/fractalmind-ai/fractalmind-envd/internal/config"
	"github.com/fractalmind-ai/fractalmind-envd/internal/sui"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// mockWG implements WGConfigurator for testing.
type mockWG struct {
	configs []wgtypes.Config
	closed  bool
}

func (m *mockWG) ConfigureDevice(_ string, cfg wgtypes.Config) error {
	m.configs = append(m.configs, cfg)
	return nil
}

func (m *mockWG) Device(_ string) (*wgtypes.Device, error) {
	return &wgtypes.Device{}, nil
}

func (m *mockWG) Close() error {
	m.closed = true
	return nil
}

func newTestManager(t *testing.T, mock *mockWG) *Manager {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{
		cfg: config.WireGuardConfig{
			InterfaceName: "wg-test",
			ListenPort:    51820,
		},
		wg:                  mock,
		privateKey:          key,
		publicKey:           key.PublicKey(),
		ensureInterface:     func(string) error { return nil },
		assignInterfaceAddr: func(string, string) error { return nil },
		peers:               make(map[string]wgtypes.Key),
	}
}

func TestSetup(t *testing.T) {
	mock := &mockWG{}
	mgr := newTestManager(t, mock)

	if err := mgr.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if len(mock.configs) != 1 {
		t.Fatalf("expected 1 config call, got %d", len(mock.configs))
	}

	cfg := mock.configs[0]
	if cfg.PrivateKey == nil {
		t.Error("private key not set")
	}
	if cfg.ListenPort == nil || *cfg.ListenPort != 51820 {
		t.Error("listen port not set correctly")
	}
}

func TestPublicKey(t *testing.T) {
	mock := &mockWG{}
	mgr := newTestManager(t, mock)

	pk := mgr.PublicKey()
	if len(pk) != 32 {
		t.Errorf("public key should be 32 bytes, got %d", len(pk))
	}
}

func TestAddRemovePeer(t *testing.T) {
	mock := &mockWG{}
	mgr := newTestManager(t, mock)

	addr := "0xdeadbeef1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	pubkey := make([]byte, 32)
	pubkey[0] = 0x42

	// Add peer
	if err := mgr.AddPeer(addr, pubkey, []string{"1.2.3.4:51820"}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	if _, ok := mgr.peers[addr]; !ok {
		t.Error("peer should be tracked after add")
	}

	// Remove peer
	if err := mgr.RemovePeer(addr); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	if _, ok := mgr.peers[addr]; ok {
		t.Error("peer should not be tracked after remove")
	}
}

func TestSyncPeers(t *testing.T) {
	mock := &mockWG{}
	mgr := newTestManager(t, mock)

	addr1 := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	addr2 := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// Add initial peer (non-zero key)
	key1 := make([]byte, 32)
	key1[0] = 0x01
	if err := mgr.AddPeer(addr1, key1, []string{"1.1.1.1:51820"}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	// Sync with new set (addr2 new, addr1 should be removed)
	key2 := make([]byte, 32)
	key2[0] = 0x02
	newPeers := []sui.PeerInfo{
		{
			Address:         addr2,
			WireGuardPubKey: key2,
			Endpoints:       []string{"2.2.2.2:51820"},
		},
	}

	if err := mgr.SyncPeers(newPeers); err != nil {
		t.Fatalf("SyncPeers: %v", err)
	}

	if _, ok := mgr.peers[addr1]; ok {
		t.Error("addr1 should have been removed")
	}
	if _, ok := mgr.peers[addr2]; !ok {
		t.Error("addr2 should have been added")
	}
}

func TestSyncPeersSkipsZeroKey(t *testing.T) {
	mock := &mockWG{}
	mgr := newTestManager(t, mock)

	addr := "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	// Peer with all-zero WG key should be skipped
	peers := []sui.PeerInfo{
		{
			Address:         addr,
			WireGuardPubKey: make([]byte, 32), // all zeros
			Endpoints:       []string{"3.3.3.3:51820"},
		},
	}

	if err := mgr.SyncPeers(peers); err != nil {
		t.Fatalf("SyncPeers: %v", err)
	}

	if _, ok := mgr.peers[addr]; ok {
		t.Error("peer with zero WG key should not have been added")
	}
}

func TestVPNAddress(t *testing.T) {
	addr := "0xdeadbeef1234567890abcdef1234567890abcdef1234567890abcdef12345678"

	vpnAddr := VPNAddress(addr)

	// Should be in 10.87.0.0/16 range
	if !vpnAddr.Is4() {
		t.Error("VPN address should be IPv4")
	}

	octets := vpnAddr.As4()
	if octets[0] != 10 || octets[1] != 87 {
		t.Errorf("VPN address should start with 10.87, got %v", vpnAddr)
	}

	// Deterministic — same input should produce same output
	vpnAddr2 := VPNAddress(addr)
	if vpnAddr != vpnAddr2 {
		t.Error("VPN address should be deterministic")
	}

	// Different address should produce different VPN IP (overwhelmingly likely)
	differentAddr := "0x1111111111111111111111111111111111111111111111111111111111111111"
	vpnAddr3 := VPNAddress(differentAddr)
	if vpnAddr == vpnAddr3 {
		t.Error("different SUI addresses should produce different VPN IPs")
	}
}

func TestVPNAddressAvoidsBoundary(t *testing.T) {
	// Test that we never get 0 for X or Y octets
	// This is a statistical test — we test many addresses
	for i := 0; i < 1000; i++ {
		addr := netip.AddrFrom4([4]byte{10, 87, byte(i / 256), byte(i % 256)}).String()
		vpn := VPNAddress(addr)
		octets := vpn.As4()
		if octets[2] == 0 || octets[3] == 0 {
			t.Errorf("VPN address %v has zero octet for input %s", vpn, addr)
		}
	}
}

func TestAssignIP(t *testing.T) {
	mock := &mockWG{}
	var assignedName, assignedCIDR string
	mgr := newTestManager(t, mock)
	mgr.assignInterfaceAddr = func(name, cidr string) error {
		assignedName = name
		assignedCIDR = cidr
		return nil
	}

	suiAddr := "0xdeadbeef1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	if err := mgr.AssignIP(suiAddr); err != nil {
		t.Fatalf("AssignIP: %v", err)
	}

	if assignedName != "wg-test" {
		t.Errorf("expected interface name wg-test, got %s", assignedName)
	}

	expectedIP := VPNAddress(suiAddr).String() + "/16"
	if assignedCIDR != expectedIP {
		t.Errorf("expected CIDR %s, got %s", expectedIP, assignedCIDR)
	}

	// Verify the assigned IP is in the 10.87.0.0/16 range
	vpn := VPNAddress(suiAddr)
	octets := vpn.As4()
	if octets[0] != 10 || octets[1] != 87 {
		t.Errorf("assigned IP should be in 10.87.0.0/16, got %v", vpn)
	}
}

func TestClose(t *testing.T) {
	mock := &mockWG{}
	mgr := newTestManager(t, mock)

	addr := "0xdeadbeef1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	if err := mgr.AddPeer(addr, make([]byte, 32), nil); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !mock.closed {
		t.Error("WG client should be closed")
	}

	if len(mgr.peers) != 0 {
		t.Error("all peers should be removed after close")
	}
}
