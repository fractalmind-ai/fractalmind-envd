package roles

import (
	"fmt"
	"net"
	"testing"

	"github.com/fractalmind-ai/fractalmind-envd/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func TestResolve_DefaultRoles(t *testing.T) {
	cfg := config.DefaultConfig()
	// STUN disabled → NATUnknown → relay=false, stun_server=false
	r := Resolve(cfg)

	if !r.Worker {
		t.Error("worker should always be true")
	}
	if r.Coordinator {
		t.Error("coordinator should default to false")
	}
	if r.Sponsor {
		t.Error("sponsor should default to false")
	}
	if r.Relay {
		t.Error("relay should be false when STUN disabled (NATUnknown)")
	}
	if r.StunServer {
		t.Error("stun_server should be false when STUN disabled (NATUnknown)")
	}
	if r.NATType != NATUnknown {
		t.Errorf("NATType should be Unknown, got %v", r.NATType)
	}
	if r.TCPFallbackActive {
		t.Error("TCPFallbackActive should be false by default")
	}
}

func TestResolve_ManualOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Roles.Coordinator = true
	cfg.Roles.Relay = boolPtr(true)       // manual enable
	cfg.Roles.StunServer = boolPtr(false) // manual disable

	r := Resolve(cfg)

	if !r.Coordinator {
		t.Error("coordinator should be true when manually set")
	}
	if !r.Relay {
		t.Error("relay should be true when manually overridden to true")
	}
	if r.StunServer {
		t.Error("stun_server should be false when manually overridden to false")
	}
}

func TestResolve_SponsorRequiresWallet(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Roles.Sponsor = true
	cfg.Sponsor.OrgWalletPath = "" // no wallet

	r := Resolve(cfg)

	if r.Sponsor {
		t.Error("sponsor should be disabled when org_wallet_path is empty")
	}
}

func TestResolve_SponsorWithWallet(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Roles.Sponsor = true
	cfg.Sponsor.OrgWalletPath = "/tmp/test-wallet.key"

	r := Resolve(cfg)

	if !r.Sponsor {
		t.Error("sponsor should be enabled when wallet path is set")
	}
}

func TestNATType_String(t *testing.T) {
	cases := []struct {
		nat  NATType
		want string
	}{
		{NATNone, "none (public IP)"},
		{NATFull, "full-cone"},
		{NATSymmetric, "symmetric"},
		{NATUnknown, "unknown"},
	}

	for _, c := range cases {
		if got := c.nat.String(); got != c.want {
			t.Errorf("NATType(%d).String() = %q, want %q", c.nat, got, c.want)
		}
	}
}

func TestResolve_TCPFallback_Activated(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.STUN.Enabled = false       // → NATUnknown
	cfg.Relay.TCPFallback = true
	cfg.Relay.RelayURL = "wss://relay.example.com/wg-relay"
	cfg.STUN.Servers = []string{}  // empty → probeUDP returns false

	r := Resolve(cfg)

	if !r.TCPFallbackActive {
		t.Error("TCPFallbackActive should be true when STUN disabled + tcp_fallback + relay_url + probeUDP fails")
	}
	if r.NATType != NATUnknown {
		t.Errorf("NATType = %v, want NATUnknown", r.NATType)
	}
}

func TestResolve_TCPFallback_NotActivated_NoRelayURL(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.STUN.Enabled = false
	cfg.Relay.TCPFallback = true
	cfg.Relay.RelayURL = "" // empty relay URL

	r := Resolve(cfg)

	if r.TCPFallbackActive {
		t.Error("TCPFallbackActive should be false when relay_url is empty")
	}
}

func TestResolve_TCPFallback_NotActivated_Disabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.STUN.Enabled = false
	cfg.Relay.TCPFallback = false
	cfg.Relay.RelayURL = "wss://relay.example.com/wg-relay"

	r := Resolve(cfg)

	if r.TCPFallbackActive {
		t.Error("TCPFallbackActive should be false when tcp_fallback is disabled")
	}
}

func TestResolve_TCPFallback_NotActivated_UDPReachable(t *testing.T) {
	// Start a local UDP "server" that echoes back
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	addr := conn.LocalAddr().(*net.UDPAddr)

	// Echo goroutine
	go func() {
		buf := make([]byte, 64)
		for {
			n, raddr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			conn.WriteTo(buf[:n], raddr)
		}
	}()

	cfg := config.DefaultConfig()
	cfg.STUN.Enabled = false
	cfg.Relay.TCPFallback = true
	cfg.Relay.RelayURL = "wss://relay.example.com/wg-relay"
	cfg.STUN.Servers = []string{addr.String()} // reachable UDP server

	r := Resolve(cfg)

	if r.TCPFallbackActive {
		t.Error("TCPFallbackActive should be false when UDP is reachable")
	}
}

func TestDetectNAT_EmptyServers(t *testing.T) {
	natType, endpoint := detectNAT(nil, "")
	if natType != NATUnknown {
		t.Errorf("detectNAT(nil) = %v, want NATUnknown", natType)
	}
	if endpoint != "" {
		t.Errorf("endpoint should be empty, got %q", endpoint)
	}
}

func TestDetectNAT_EmptyServersList(t *testing.T) {
	natType, endpoint := detectNAT([]string{}, "")
	if natType != NATUnknown {
		t.Errorf("detectNAT([]) = %v, want NATUnknown", natType)
	}
	if endpoint != "" {
		t.Errorf("endpoint should be empty, got %q", endpoint)
	}
}

func TestProbeUDP_NoServers(t *testing.T) {
	result := probeUDP(nil)
	if result {
		t.Error("probeUDP(nil) should return false")
	}
}

func TestProbeUDP_EmptyServers(t *testing.T) {
	result := probeUDP([]string{})
	if result {
		t.Error("probeUDP([]) should return false")
	}
}

func TestProbeUDP_UnreachableServer(t *testing.T) {
	// Use a port that's not listening — should fail quickly on Linux (ICMP unreachable)
	result := probeUDP([]string{"127.0.0.1:1"})
	if result {
		t.Error("probeUDP with unreachable server should return false")
	}
}

func TestProbeUDP_StunPrefixStripping(t *testing.T) {
	// With stun: prefix pointing to unreachable server
	result := probeUDP([]string{"stun:127.0.0.1:1"})
	if result {
		t.Error("probeUDP with stun: prefix should strip prefix and probe")
	}
}

func TestProbeUDP_ReachableServer(t *testing.T) {
	// Start a UDP server that echoes back
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	addr := conn.LocalAddr().(*net.UDPAddr)

	go func() {
		buf := make([]byte, 64)
		for {
			n, raddr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			conn.WriteTo(buf[:n], raddr)
		}
	}()

	result := probeUDP([]string{addr.String()})
	if !result {
		t.Error("probeUDP should return true when server echoes back")
	}
}

func TestResolve_RelayAutoDetect_NotRelay(t *testing.T) {
	cfg := config.DefaultConfig()
	// STUN disabled → NATUnknown → relay auto-detect should be false (not NATNone)
	cfg.Roles.Relay = nil // auto-detect

	r := Resolve(cfg)

	if r.Relay {
		t.Error("relay should be false when NAT type is unknown (auto-detect)")
	}
}

func TestResolve_StunServerAutoDetect_NotEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Roles.StunServer = nil // auto-detect

	r := Resolve(cfg)

	if r.StunServer {
		t.Error("stun_server should be false when NAT type is unknown (auto-detect)")
	}
}

func TestResolve_AllRolesManualEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Roles.Coordinator = true
	cfg.Roles.Sponsor = true
	cfg.Sponsor.OrgWalletPath = "/tmp/wallet.key"
	cfg.Roles.Relay = boolPtr(true)
	cfg.Roles.StunServer = boolPtr(true)

	r := Resolve(cfg)

	if !r.Worker {
		t.Error("worker should always be true")
	}
	if !r.Coordinator {
		t.Error("coordinator should be true")
	}
	if !r.Sponsor {
		t.Error("sponsor should be true with wallet")
	}
	if !r.Relay {
		t.Error("relay should be true when manually enabled")
	}
	if !r.StunServer {
		t.Error("stun_server should be true when manually enabled")
	}
}

// mockDiscoverEndpoint creates a mock STUN discovery function for testing.
func mockDiscoverEndpoint(responses []struct {
	endpoint string
	err      error
}) func([]string, string) (string, error) {
	call := 0
	return func(servers []string, bindAddr string) (string, error) {
		if call >= len(responses) {
			return "", fmt.Errorf("no more mock responses")
		}
		r := responses[call]
		call++
		return r.endpoint, r.err
	}
}

func TestDetectNAT_BothProbesMatch_NATNone(t *testing.T) {
	orig := discoverEndpointFunc
	defer func() { discoverEndpointFunc = orig }()

	discoverEndpointFunc = mockDiscoverEndpoint([]struct {
		endpoint string
		err      error
	}{
		{"1.2.3.4:51820", nil},
		{"1.2.3.4:51820", nil}, // same endpoint
	})

	natType, ep := detectNAT([]string{"stun:s1:3478", "stun:s2:3478"}, "")
	if natType != NATNone {
		t.Errorf("expected NATNone, got %v", natType)
	}
	if ep != "1.2.3.4:51820" {
		t.Errorf("endpoint = %q, want %q", ep, "1.2.3.4:51820")
	}
}

func TestDetectNAT_ProbesDiffer_Symmetric(t *testing.T) {
	orig := discoverEndpointFunc
	defer func() { discoverEndpointFunc = orig }()

	discoverEndpointFunc = mockDiscoverEndpoint([]struct {
		endpoint string
		err      error
	}{
		{"1.2.3.4:51820", nil},
		{"1.2.3.4:52000", nil}, // different port → symmetric
	})

	natType, ep := detectNAT([]string{"stun:s1:3478", "stun:s2:3478"}, "")
	if natType != NATSymmetric {
		t.Errorf("expected NATSymmetric, got %v", natType)
	}
	if ep != "1.2.3.4:51820" {
		t.Errorf("endpoint = %q, want %q", ep, "1.2.3.4:51820")
	}
}

func TestDetectNAT_FirstProbeFails(t *testing.T) {
	orig := discoverEndpointFunc
	defer func() { discoverEndpointFunc = orig }()

	discoverEndpointFunc = mockDiscoverEndpoint([]struct {
		endpoint string
		err      error
	}{
		{"", fmt.Errorf("stun timeout")},
	})

	natType, ep := detectNAT([]string{"stun:s1:3478"}, "")
	if natType != NATUnknown {
		t.Errorf("expected NATUnknown, got %v", natType)
	}
	if ep != "" {
		t.Errorf("endpoint should be empty, got %q", ep)
	}
}

func TestDetectNAT_SecondProbeFails_FullCone(t *testing.T) {
	orig := discoverEndpointFunc
	defer func() { discoverEndpointFunc = orig }()

	discoverEndpointFunc = mockDiscoverEndpoint([]struct {
		endpoint string
		err      error
	}{
		{"5.6.7.8:51820", nil},
		{"", fmt.Errorf("stun timeout")}, // second fails
	})

	natType, ep := detectNAT([]string{"stun:s1:3478", "stun:s2:3478"}, "")
	if natType != NATFull {
		t.Errorf("expected NATFull, got %v", natType)
	}
	if ep != "5.6.7.8:51820" {
		t.Errorf("endpoint = %q, want %q", ep, "5.6.7.8:51820")
	}
}

func TestResolve_STUNEnabled_PublicIP(t *testing.T) {
	orig := discoverEndpointFunc
	defer func() { discoverEndpointFunc = orig }()

	discoverEndpointFunc = mockDiscoverEndpoint([]struct {
		endpoint string
		err      error
	}{
		{"1.2.3.4:51820", nil},
		{"1.2.3.4:51820", nil},
	})

	cfg := config.DefaultConfig()
	cfg.STUN.Enabled = true
	cfg.STUN.Servers = []string{"stun:s1:3478", "stun:s2:3478"}

	r := Resolve(cfg)

	if r.NATType != NATNone {
		t.Errorf("NATType = %v, want NATNone", r.NATType)
	}
	if r.PublicEndpoint != "1.2.3.4:51820" {
		t.Errorf("PublicEndpoint = %q, want %q", r.PublicEndpoint, "1.2.3.4:51820")
	}
	// With auto-detect, public IP → relay + stun_server
	if !r.Relay {
		t.Error("relay should auto-enable with public IP")
	}
	if !r.StunServer {
		t.Error("stun_server should auto-enable with public IP")
	}
}

func TestResolve_STUNEnabled_SymmetricNAT(t *testing.T) {
	orig := discoverEndpointFunc
	defer func() { discoverEndpointFunc = orig }()

	discoverEndpointFunc = mockDiscoverEndpoint([]struct {
		endpoint string
		err      error
	}{
		{"1.2.3.4:51820", nil},
		{"1.2.3.4:52000", nil}, // different → symmetric
	})

	cfg := config.DefaultConfig()
	cfg.STUN.Enabled = true
	cfg.STUN.Servers = []string{"stun:s1:3478", "stun:s2:3478"}

	r := Resolve(cfg)

	if r.NATType != NATSymmetric {
		t.Errorf("NATType = %v, want NATSymmetric", r.NATType)
	}
	if r.Relay {
		t.Error("relay should not auto-enable with symmetric NAT")
	}
}
