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
