package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the sentinel.yaml configuration.
type Config struct {
	Identity  IdentityConfig  `yaml:"identity"`
	Roles     RolesConfig     `yaml:"roles"`
	Agents    AgentsConfig    `yaml:"agents"`
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`
	SUI       SUIConfig       `yaml:"sui"`
	WireGuard WireGuardConfig `yaml:"wireguard"`
	STUN      STUNConfig      `yaml:"stun"`
	Sponsor   SponsorConfig   `yaml:"sponsor"`
	Relay     RelayConfig     `yaml:"relay"`

	// Legacy: Gateway config kept for backwards-compatibility but not used in v3
	Gateway GatewayConfig `yaml:"gateway"`
}

// RolesConfig controls which roles this envd node enables.
// relay and stun are auto-detected (public IP) unless explicitly set.
type RolesConfig struct {
	Coordinator bool  `yaml:"coordinator"`
	Sponsor     bool  `yaml:"sponsor"`
	Relay       *bool `yaml:"relay"`       // nil = auto-detect, true/false = override
	StunServer  *bool `yaml:"stun_server"` // nil = auto-detect, true/false = override
}

type GatewayConfig struct {
	URL               string `yaml:"url"`
	ReconnectInterval string `yaml:"reconnect_interval"`
}

type IdentityConfig struct {
	HostID   string `yaml:"host_id"`
	Hostname string `yaml:"hostname"`
}

type AgentsConfig struct {
	ScanMethod         string `yaml:"scan_method"`
	ScanInterval       string `yaml:"scan_interval"`
	AutoRestart        bool   `yaml:"auto_restart"`
	MaxRestartAttempts int    `yaml:"max_restart_attempts"`
}

type HeartbeatConfig struct {
	Interval string `yaml:"interval"`
}

type SUIConfig struct {
	Enabled           bool   `yaml:"enabled"`
	RPC               string `yaml:"rpc"`
	KeypairPath       string `yaml:"keypair_path"`
	PackageID         string `yaml:"package_id"`
	ProtocolPackageID string `yaml:"protocol_package_id"`
	RegistryID        string `yaml:"registry_id"`
	OrgID             string `yaml:"org_id"`
	CertID            string `yaml:"cert_id"`
	PollInterval      string `yaml:"poll_interval"`
}

// SponsorConfig configures the built-in gas sponsorship role.
// Only used when roles.sponsor=true.
type SponsorConfig struct {
	OrgWalletPath   string   `yaml:"org_wallet_path"`
	MaxGasPerTx     uint64   `yaml:"max_gas_per_tx"`
	DailyGasLimit   uint64   `yaml:"daily_gas_limit"`
	AllowedPackages []string `yaml:"allowed_packages"`
}

// RelayConfig controls the relay/STUN server behavior.
// Relay and STUN server auto-enable when a public IP is detected.
type RelayConfig struct {
	ListenPort     int    `yaml:"listen_port"`
	MaxConnections int    `yaml:"max_connections"`
	BandwidthLimit string `yaml:"bandwidth_limit"`
	Region         string `yaml:"region"`
	ISP            string `yaml:"isp"`
}

type WireGuardConfig struct {
	Enabled       bool   `yaml:"enabled"`
	InterfaceName string `yaml:"interface_name"`
	ListenPort    int    `yaml:"listen_port"`
	KeypairPath   string `yaml:"keypair_path"`
	Address       string `yaml:"address"`
}

type STUNConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Servers     []string `yaml:"servers"`
	BindAddress string   `yaml:"bind_address"` // optional: bind STUN probes to this local address (useful when VPN is active)
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() *Config {
	hostname, _ := os.Hostname()
	return &Config{
		Gateway: GatewayConfig{
			URL:               "ws://localhost:8080/ws",
			ReconnectInterval: "5s",
		},
		Identity: IdentityConfig{
			HostID:   "",
			Hostname: hostname,
		},
		Roles: RolesConfig{
			Coordinator: false,
			Sponsor:     false,
			Relay:       nil, // auto-detect
			StunServer:  nil, // auto-detect
		},
		Agents: AgentsConfig{
			ScanMethod:         "tmux",
			ScanInterval:       "10s",
			AutoRestart:        true,
			MaxRestartAttempts: 3,
		},
		Heartbeat: HeartbeatConfig{
			Interval: "30s",
		},
		SUI: SUIConfig{
			Enabled:      false,
			RPC:          "https://fullnode.testnet.sui.io:443",
			KeypairPath:  "~/.sui/envd.key",
			PollInterval: "30s",
		},
		WireGuard: WireGuardConfig{
			Enabled:       false,
			InterfaceName: "wg0",
			ListenPort:    51820,
			KeypairPath:   "~/.wireguard/envd.key",
		},
		STUN: STUNConfig{
			Enabled: false,
			Servers: []string{
				"stun:stun.l.google.com:19302",
				"stun:stun1.l.google.com:19302",
			},
		},
		Sponsor: SponsorConfig{
			MaxGasPerTx:   10_000_000,  // 0.01 SUI
			DailyGasLimit: 100_000_000, // 0.1 SUI
		},
		Relay: RelayConfig{
			ListenPort:     3478,
			MaxConnections: 100,
		},
	}
}

// Load reads a YAML config file and merges with defaults.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
