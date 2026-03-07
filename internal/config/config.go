package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the sentinel.yaml configuration.
type Config struct {
	Gateway   GatewayConfig   `yaml:"gateway"`
	Identity  IdentityConfig  `yaml:"identity"`
	Agents    AgentsConfig    `yaml:"agents"`
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`
	SUI       SUIConfig       `yaml:"sui"`
	WireGuard WireGuardConfig `yaml:"wireguard"`
	STUN      STUNConfig      `yaml:"stun"`
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
	Enabled      bool          `yaml:"enabled"`
	RPC          string        `yaml:"rpc"`
	KeypairPath  string        `yaml:"keypair_path"`
	PackageID    string        `yaml:"package_id"`
	RegistryID   string        `yaml:"registry_id"`
	OrgID        string        `yaml:"org_id"`
	CertID       string        `yaml:"cert_id"`
	PollInterval string        `yaml:"poll_interval"`
	Sponsor      SponsorConfig `yaml:"sponsor"`
}

type SponsorConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
}

type WireGuardConfig struct {
	Enabled       bool   `yaml:"enabled"`
	InterfaceName string `yaml:"interface_name"`
	ListenPort    int    `yaml:"listen_port"`
	KeypairPath   string `yaml:"keypair_path"`
	Address       string `yaml:"address"`
}

type STUNConfig struct {
	Enabled bool     `yaml:"enabled"`
	Servers []string `yaml:"servers"`
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
