<div align="center">

# fractalmind-envd

**Lightweight daemon for remote AI Agent management on SUI.**

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8)](https://go.dev/)
[![SUI](https://img.shields.io/badge/SUI-Identity-4DA2FF)](https://sui.io/)
[![MIT License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

</div>

---

## What is envd?

`envd` (environment daemon) runs on each machine hosting AI Agents. It:

1. **Discovers** local AI agents (tmux sessions)
2. **Reports** status via heartbeat to a Gateway
3. **Executes** remote commands (restart, logs, kill, shell)
4. **Self-heals** — auto-restarts crashed agents within 60 seconds

Unlike traditional remote control tools (TeamViewer, Tailscale), envd uses **SUI blockchain** for identity and authorization — no central server can revoke your access.

## Architecture

```
SUI Blockchain          Identity + Authorization
       │
   Gateway              WebSocket server + REST API
       │
  ┌────┴────┐
  │         │
 envd     envd          Go daemon (this repo)
host-A   host-B         Heartbeat + Agent discovery + Self-heal
  │         │
tmux      tmux          AI Agent processes
```

## Quick Start

### Linux

```bash
# Build
make build

# Configure
cp sentinel.yaml.example sentinel.yaml
# Edit gateway URL, identity, etc.

# Run
./bin/envd --config sentinel.yaml
```

### macOS

envd supports macOS with graceful degradation — WireGuard is optional, and SUI + agent scanning work independently.

```bash
# Install dependencies
brew install wireguard-tools go git

# Build (produces bin/envd-darwin-arm64)
make build-darwin
# Or: CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/envd ./cmd/envd/

# Configure
cp sentinel.yaml.example sentinel.yaml
```

**macOS-specific config notes:**

```yaml
wireguard:
  enabled: true
  interface_name: "utun99"    # macOS requires utun[0-9]* names (not wg0)
  listen_port: 51820
  keypair_path: "~/.wireguard/envd.key"

stun:
  enabled: true
  bind_address: ""            # Set to your physical IP if VPN causes STUN timeouts
  servers:
    - stun:stun.l.google.com:19302
```

- **Interface name:** macOS uses `utun*` format. Set `interface_name: "utun99"` (or any unused utun number).
- **WireGuard requires root:** `wireguard-go` needs root/sudo to create TUN devices. Run with `sudo` or use a launchd plist.
- **VPN conflict:** If a VPN is active, STUN may bind to the VPN tunnel address and time out. Set `stun.bind_address` to your physical interface IP (e.g., `192.168.1.100`).
- **Graceful degradation:** If WireGuard fails (no root, interface creation error), envd continues with SUI registration + agent scanning. WireGuard features are disabled but everything else works.

**Running with launchd (recommended for macOS):**

```bash
sudo tee /Library/LaunchDaemons/ai.fractalmind.envd.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>ai.fractalmind.envd</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/envd</string>
        <string>--config</string>
        <string>/etc/envd/sentinel.yaml</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/envd.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/envd.log</string>
</dict>
</plist>
EOF

sudo cp bin/envd /usr/local/bin/envd
sudo mkdir -p /etc/envd && sudo cp sentinel.yaml /etc/envd/sentinel.yaml
sudo launchctl load /Library/LaunchDaemons/ai.fractalmind.envd.plist
```

## Configuration

See [`sentinel.yaml.example`](sentinel.yaml.example) for all options.

| Setting | Default | Description |
|---------|---------|-------------|
| `gateway.url` | `ws://localhost:8080/ws` | Gateway WebSocket URL |
| `agents.scan_method` | `tmux` | Agent discovery method |
| `agents.auto_restart` | `true` | Auto-restart crashed agents |
| `heartbeat.interval` | `30s` | Heartbeat frequency |

## Remote Commands

| Command | Description |
|---------|-------------|
| `status` | List all agents and their status |
| `restart <agent>` | Restart a specific agent |
| `kill <agent>` | Stop an agent |
| `logs <agent>` | Get recent agent logs |
| `shell <cmd>` | Execute a shell command |

## Research

See [`docs/research.md`](docs/research.md) for competitive analysis (Oray/ToDesk/Tailscale) and our SUI-based differentiation.

## Part of FractalMind AI

```
fractalmind-protocol    ← On-chain identity (SUI)
fractalmind-envd        ← This repo: remote agent management
agent-manager-skill     ← Local agent management
fractalbot              ← Multi-channel messaging
```

## License

MIT
