package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the sentinel.yaml configuration.
type Config struct {
	Identity    IdentityConfig    `yaml:"identity"`
	Roles       RolesConfig       `yaml:"roles"`
	Coordinator CoordinatorConfig `yaml:"coordinator"`
	Agents      AgentsConfig      `yaml:"agents"`
	Heartbeat   HeartbeatConfig   `yaml:"heartbeat"`
	SUI         SUIConfig         `yaml:"sui"`
	WireGuard   WireGuardConfig   `yaml:"wireguard"`
	STUN        STUNConfig        `yaml:"stun"`
	Sponsor     SponsorConfig     `yaml:"sponsor"`
	Relay       RelayConfig       `yaml:"relay"`

	// Gateway config is the worker-side transport target.
	// Workers connect to the coordinator envd WebSocket at this URL.
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

type CoordinatorConfig struct {
	ListenAddr string `yaml:"listen_addr"`
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

	// TCP fallback: WSS relay for UDP-restricted networks (Issue #8)
	TCPFallback     bool   `yaml:"tcp_fallback"`      // Enable WSS fallback when UDP fails
	RelayURL        string `yaml:"relay_url"`         // WSS relay endpoint (e.g., "wss://relay.example.com/wg-relay")
	WSSListenPort   int    `yaml:"wss_listen_port"`   // WSS server port for relay nodes (default: 443)
	WSSExternalPort int    `yaml:"wss_external_port"` // External WSS port for relay_addr (e.g., 443 when iptables redirects 443→8443)
	WSSCertFile     string `yaml:"wss_cert_file"`     // TLS certificate file (if empty, plain HTTP — requires reverse proxy for TLS)
	WSSKeyFile      string `yaml:"wss_key_file"`      // TLS private key file
	WSSPortMin      int    `yaml:"wss_port_min"`      // UDP port pool start for WSS clients (default: 51900)
	WSSPortMax      int    `yaml:"wss_port_max"`      // UDP port pool end for WSS clients (default: 51999)
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
		Coordinator: CoordinatorConfig{
			ListenAddr: ":8080",
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
			WSSListenPort:  443,
			WSSPortMin:     51900,
			WSSPortMax:     51999,
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
