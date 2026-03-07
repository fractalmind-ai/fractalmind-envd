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

```bash
# Build
make build

# Configure
cp sentinel.yaml.example sentinel.yaml
# Edit gateway URL, identity, etc.

# Run
./bin/envd --config sentinel.yaml
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
