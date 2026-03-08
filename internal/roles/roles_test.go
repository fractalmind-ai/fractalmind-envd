package roles

import (
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
