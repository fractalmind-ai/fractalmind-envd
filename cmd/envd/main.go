package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/agent"
	"github.com/fractalmind-ai/fractalmind-envd/internal/config"
	"github.com/fractalmind-ai/fractalmind-envd/internal/heartbeat"
	"github.com/fractalmind-ai/fractalmind-envd/internal/relay"
	"github.com/fractalmind-ai/fractalmind-envd/internal/roles"
	"github.com/fractalmind-ai/fractalmind-envd/internal/sui"
	"github.com/fractalmind-ai/fractalmind-envd/internal/wg"
	"github.com/fractalmind-ai/fractalmind-envd/internal/ws"
	wgctrl "golang.zx2c4.com/wireguard/wgctrl"
)

var (
	version   = "dev"
	startedAt = time.Now()
)

func main() {
	configPath := flag.String("config", "sentinel.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "show version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("fractalmind-envd %s\n", version)
		os.Exit(0)
	}

	log.SetPrefix("[envd] ")
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	log.Printf("starting fractalmind-envd %s (host=%s)", version, cfg.Identity.Hostname)

	// Parse durations
	reconnectWait, _ := time.ParseDuration(cfg.Gateway.ReconnectInterval)
	if reconnectWait == 0 {
		reconnectWait = 5 * time.Second
	}
	heartbeatInterval, _ := time.ParseDuration(cfg.Heartbeat.Interval)
	if heartbeatInterval == 0 {
		heartbeatInterval = 30 * time.Second
	}
	scanInterval, _ := time.ParseDuration(cfg.Agents.ScanInterval)
	if scanInterval == 0 {
		scanInterval = 10 * time.Second
	}

	// ======= v3: Resolve active roles =======
	activeRoles := roles.Resolve(cfg)

	// Initialize components
	scanner := agent.NewScanner(cfg.Agents.ScanMethod)
	wsClient := ws.NewClient(cfg.Gateway.URL, reconnectWait)

	// Track agents and restart counts
	restartCounts := make(map[string]int)
	var lastAgents []agent.Agent

	// --- SUI + WireGuard integration (gated behind config flags) ---
	var suiClient *sui.Client
	var wgManager *wg.Manager
	var suiPollTicker *time.Ticker
	var eventCursor string

	if cfg.SUI.Enabled && cfg.WireGuard.Enabled {
		log.Printf("SUI + WireGuard integration enabled")

		// 1. Init WireGuard manager
		wgClient, err := wgctrl.New()
		if err != nil {
			log.Fatalf("failed to create wgctrl client: %v", err)
		}

		wgManager, err = wg.NewManager(cfg.WireGuard, wgClient)
		if err != nil {
			log.Fatalf("failed to create wg manager: %v", err)
		}

		if err := wgManager.Setup(); err != nil {
			log.Fatalf("failed to setup wg interface: %v", err)
		}

		// 2. Build endpoints list (use NAT detection result from role resolution)
		var endpoints []string
		if activeRoles.PublicEndpoint != "" {
			endpoints = append(endpoints, activeRoles.PublicEndpoint)
		}
		if cfg.WireGuard.Address != "" {
			endpoints = append(endpoints, cfg.WireGuard.Address)
		}
		if len(endpoints) == 0 {
			endpoints = append(endpoints, fmt.Sprintf("0.0.0.0:%d", cfg.WireGuard.ListenPort))
		}

		// 3. Init SUI client
		suiClient, err = sui.NewClient(cfg.SUI)
		if err != nil {
			log.Fatalf("failed to create sui client: %v", err)
		}

		// 4. Register peer on-chain (with relay info if applicable)
		ctx := context.Background()
		if err := suiClient.RegisterPeer(ctx, wgManager.PublicKey(), endpoints, cfg.Identity.Hostname); err != nil {
			log.Printf("[sui] peer registration failed: %v", err)
		}

		// 5. Query existing peers and sync WireGuard
		peers, err := suiClient.QueryPeers(ctx)
		if err != nil {
			log.Printf("[sui] failed to query peers: %v", err)
		} else if len(peers) > 0 {
			if err := wgManager.SyncPeers(peers); err != nil {
				log.Printf("[wg] failed to sync peers: %v", err)
			}
		}

		// Start SUI event poll ticker
		pollInterval, _ := time.ParseDuration(cfg.SUI.PollInterval)
		if pollInterval == 0 {
			pollInterval = 30 * time.Second
		}
		suiPollTicker = time.NewTicker(pollInterval)
	}

	// ======= v3: Start role-specific services =======
	var relayServer *relay.Server
	var stunOnlyServer *relay.StunOnlyServer

	if activeRoles.Relay {
		// Combined STUN + Relay on shared UDP port
		relayServer = relay.NewServer(relay.Config{
			ListenPort:     cfg.Relay.ListenPort,
			PublicIP:       activeRoles.PublicEndpoint,
			MaxConnections: cfg.Relay.MaxConnections,
		})
		if err := relayServer.Start(); err != nil {
			log.Fatalf("[relay] failed to start: %v", err)
		}
		log.Printf("[relay] relay server enabled on :%d (region=%s, isp=%s)",
			cfg.Relay.ListenPort, cfg.Relay.Region, cfg.Relay.ISP)
	} else if activeRoles.StunServer {
		// STUN-only server (no relay capability)
		stunOnlyServer = relay.NewStunOnlyServer(cfg.Relay.ListenPort)
		if err := stunOnlyServer.Start(); err != nil {
			log.Fatalf("[stun-server] failed to start: %v", err)
		}
		log.Printf("[stun-server] STUN server enabled on :%d", cfg.Relay.ListenPort)
	}

	if activeRoles.Sponsor {
		log.Printf("[sponsor] sponsor role enabled (wallet=%s)", cfg.Sponsor.OrgWalletPath)
		// TODO KR3: Start built-in sponsor service
	}

	if activeRoles.Coordinator {
		log.Printf("[coordinator] REST API enabled")
		// REST API already exists in current codebase
	}

	// Handle commands from Gateway
	wsClient.OnCommand(func(cmd ws.CommandPayload) {
		log.Printf("[cmd] received: %s agent=%s", cmd.Command, cmd.AgentID)
		result := handleCommand(cmd, scanner, cfg)
		wsClient.Send("command_result", map[string]interface{}{
			"request_id": cmd.RequestID,
			"result":     result,
		})
	})

	// Start WebSocket connection in background
	go wsClient.Connect()

	// Register with Gateway
	go func() {
		time.Sleep(2 * time.Second) // Wait for connection
		wsClient.Send("register", map[string]string{
			"host_id":  cfg.Identity.HostID,
			"hostname": cfg.Identity.Hostname,
			"version":  version,
		})
	}()

	// Heartbeat + scan loop
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	scanTicker := time.NewTicker(scanInterval)
	defer heartbeatTicker.Stop()
	defer scanTicker.Stop()
	if suiPollTicker != nil {
		defer suiPollTicker.Stop()
	}

	// Initial scan
	if agents, err := scanner.Scan(); err == nil {
		lastAgents = agents
		log.Printf("initial scan: %d agents found", len(agents))
	}

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Helper to get SUI poll channel (nil-safe)
	suiPollCh := func() <-chan time.Time {
		if suiPollTicker != nil {
			return suiPollTicker.C
		}
		return nil
	}

	for {
		select {
		case <-scanTicker.C:
			agents, err := scanner.Scan()
			if err != nil {
				log.Printf("[scan] error: %v", err)
				continue
			}

			// Detect crashed agents (was running, now missing)
			if cfg.Agents.AutoRestart {
				detectAndRestart(lastAgents, agents, scanner, restartCounts, cfg.Agents.MaxRestartAttempts, wsClient)
			}

			lastAgents = agents

		case <-heartbeatTicker.C:
			payload := heartbeat.NewPayload(
				cfg.Identity.HostID,
				cfg.Identity.Hostname,
				lastAgents,
				startedAt,
			)

			// v3: Attach relay load info if this node is a relay
			if activeRoles.Relay && relayServer != nil {
				info := relayServer.GetLoadInfo()
				payload.WithRelayLoad(info.CurrentLoad, info.Capacity, info.AvgLatencyMs)
			}

			if err := wsClient.Send("heartbeat", payload); err != nil {
				log.Printf("[heartbeat] send failed: %v", err)
			}

		case <-suiPollCh():
			// Poll SUI for new peer events
			if suiClient != nil && wgManager != nil {
				newPeers, newCursor, err := suiClient.PollNewEvents(context.Background(), eventCursor)
				if err != nil {
					log.Printf("[sui] event poll failed: %v", err)
				} else {
					eventCursor = newCursor
					if len(newPeers) > 0 {
						log.Printf("[sui] %d peer updates from poll", len(newPeers))
						if err := wgManager.SyncPeers(newPeers); err != nil {
							log.Printf("[wg] failed to sync peers: %v", err)
						}
					}
				}
			}

		case sig := <-sigCh:
			log.Printf("received signal %s, shutting down", sig)

			// Graceful relay/STUN shutdown
			if relayServer != nil {
				if err := relayServer.Close(); err != nil {
					log.Printf("[relay] close failed: %v", err)
				}
			}
			if stunOnlyServer != nil {
				if err := stunOnlyServer.Close(); err != nil {
					log.Printf("[stun-server] close failed: %v", err)
				}
			}

			// Graceful SUI + WireGuard shutdown
			if suiClient != nil {
				if err := suiClient.GoOffline(context.Background()); err != nil {
					log.Printf("[sui] go offline failed: %v", err)
				}
			}
			if wgManager != nil {
				if err := wgManager.Close(); err != nil {
					log.Printf("[wg] close failed: %v", err)
				}
			}

			wsClient.Close()
			os.Exit(0)
		}
	}
}

// detectAndRestart checks for crashed agents and restarts them.
func detectAndRestart(prev, curr []agent.Agent, scanner *agent.Scanner, restartCounts map[string]int, maxAttempts int, wsClient *ws.Client) {
	currentSet := make(map[string]bool)
	for _, a := range curr {
		currentSet[a.Session] = true
	}

	for _, a := range prev {
		if a.Status == "running" && !currentSet[a.Session] {
			count := restartCounts[a.Session]
			if count >= maxAttempts {
				log.Printf("[auto-restart] %s: max attempts (%d) reached, alerting", a.Session, maxAttempts)
				wsClient.Send("alert", map[string]string{
					"type":    "agent_crash",
					"agent":   a.ID,
					"session": a.Session,
					"message": fmt.Sprintf("agent %s crashed, restart failed after %d attempts", a.ID, maxAttempts),
				})
				continue
			}

			log.Printf("[auto-restart] %s crashed, attempt %d/%d", a.Session, count+1, maxAttempts)
			if err := scanner.RestartAgent(a.Session); err != nil {
				log.Printf("[auto-restart] %s restart failed: %v", a.Session, err)
			}
			restartCounts[a.Session] = count + 1
		}
	}
}

// handleCommand processes a command from Gateway.
func handleCommand(cmd ws.CommandPayload, scanner *agent.Scanner, cfg *config.Config) map[string]interface{} {
	result := map[string]interface{}{
		"success": true,
	}

	switch cmd.Command {
	case "status":
		agents, err := scanner.Scan()
		if err != nil {
			result["success"] = false
			result["error"] = err.Error()
			return result
		}
		result["agents"] = agents

	case "restart":
		if cmd.AgentID == "" {
			result["success"] = false
			result["error"] = "agent_id required"
			return result
		}
		if err := scanner.RestartAgent(cmd.AgentID); err != nil {
			result["success"] = false
			result["error"] = err.Error()
			return result
		}
		result["message"] = fmt.Sprintf("agent %s restarted", cmd.AgentID)

	case "logs":
		if cmd.AgentID == "" {
			result["success"] = false
			result["error"] = "agent_id required"
			return result
		}
		lines := "100"
		if cmd.Args != "" {
			lines = cmd.Args
		}
		out, err := exec.Command("tmux", "capture-pane", "-t", cmd.AgentID, "-p", "-S", "-"+lines).Output()
		if err != nil {
			result["success"] = false
			result["error"] = err.Error()
			return result
		}
		result["logs"] = string(out)

	case "kill":
		if cmd.AgentID == "" {
			result["success"] = false
			result["error"] = "agent_id required"
			return result
		}
		if err := exec.Command("tmux", "kill-session", "-t", cmd.AgentID).Run(); err != nil {
			result["success"] = false
			result["error"] = err.Error()
			return result
		}
		result["message"] = fmt.Sprintf("agent %s killed", cmd.AgentID)

	case "shell":
		if cmd.Args == "" {
			result["success"] = false
			result["error"] = "args required (shell command)"
			return result
		}
		out, err := exec.Command("bash", "-c", cmd.Args).CombinedOutput()
		if err != nil {
			result["success"] = false
			result["error"] = err.Error()
			result["output"] = string(out)
			return result
		}
		result["output"] = string(out)

	default:
		result["success"] = false
		result["error"] = fmt.Sprintf("unknown command: %s", cmd.Command)
	}

	log.Printf("[cmd] %s result: success=%v", cmd.Command, result["success"])
	return result
}
