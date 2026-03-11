package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/agent"
	"github.com/fractalmind-ai/fractalmind-envd/internal/config"
	"github.com/fractalmind-ai/fractalmind-envd/internal/heartbeat"
	"github.com/fractalmind-ai/fractalmind-envd/internal/relay"
	"github.com/fractalmind-ai/fractalmind-envd/internal/relaypicker"
	"github.com/fractalmind-ai/fractalmind-envd/internal/roles"
	"github.com/fractalmind-ai/fractalmind-envd/internal/sponsor"
	"github.com/fractalmind-ai/fractalmind-envd/internal/sui"
	"github.com/fractalmind-ai/fractalmind-envd/internal/wg"
	"github.com/fractalmind-ai/fractalmind-envd/internal/ws"
	wgctrl "golang.zx2c4.com/wireguard/wgctrl"
)

var (
	version   = "dev"
	startedAt = time.Now()

	// relayPeers tracks SUI address → relay peer ID for WSS fallback cleanup.
	relayPeers = make(map[string]uint16)
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

	// Initialize sponsor service early (needed for SUI self-sponsorship)
	var sponsorSvc *sponsor.Service
	if activeRoles.Sponsor {
		sponsorSvc, err = sponsor.NewService(sponsor.Config{
			SUI_RPC:         cfg.SUI.RPC,
			OrgWalletPath:   cfg.Sponsor.OrgWalletPath,
			AllowedPackages: cfg.Sponsor.AllowedPackages,
			MaxGasPerTx:     cfg.Sponsor.MaxGasPerTx,
			DailyGasLimit:   cfg.Sponsor.DailyGasLimit,
		})
		if err != nil {
			log.Fatalf("[sponsor] failed to start: %v", err)
		}
		log.Printf("[sponsor] sponsor role enabled (wallet=%s, address=%s)", cfg.Sponsor.OrgWalletPath, sponsorSvc.Address())
	}

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
	var eventCursor interface{}

	if cfg.SUI.Enabled {
		// --- WireGuard init (optional, graceful degradation) ---
		if cfg.WireGuard.Enabled {
			log.Printf("SUI + WireGuard integration enabled")

			wgClient, err := wgctrl.New()
			if err != nil {
				log.Printf("[wg] WARNING: failed to create wgctrl client: %v (continuing without WireGuard)", err)
			} else {
				wgManager, err = wg.NewManager(cfg.WireGuard, wgClient)
				if err != nil {
					log.Printf("[wg] WARNING: failed to create wg manager: %v (continuing without WireGuard)", err)
				} else if err := wgManager.Setup(); err != nil {
					log.Printf("[wg] WARNING: failed to setup wg interface: %v (continuing without WireGuard)", err)
					wgManager = nil
				}
			}
		} else {
			log.Printf("SUI enabled (WireGuard disabled)")
		}

		// Build endpoints list (use NAT detection result from role resolution)
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

		// Init SUI client (works independently of WireGuard)
		suiClient, err = sui.NewClient(cfg.SUI)
		if err != nil {
			log.Fatalf("failed to create sui client: %v", err)
		}

		// Assign deterministic VPN IP to WireGuard interface
		if wgManager != nil {
			if err := wgManager.AssignIP(suiClient.Address()); err != nil {
				log.Printf("[wg] WARNING: failed to assign VPN IP: %v", err)
			}
		}

		// Gas top-up: sponsor wallet funds envd node for direct SUI execution
		if sponsorSvc != nil {
			ctx := context.Background()
			if err := sponsorSvc.TransferGas(ctx, suiClient.Address(), 50_000_000); err != nil {
				log.Printf("[sui] gas top-up failed: %v (will retry with direct gas)", err)
			}
		}

		// Register peer on-chain
		ctx := context.Background()
		var wgPubKey []byte
		if wgManager != nil {
			wgPubKey = wgManager.PublicKey()
		}
		if len(wgPubKey) != 32 {
			log.Printf("[wg] WARNING: WireGuard public key missing or invalid (%d bytes), using zero-filled key", len(wgPubKey))
			wgPubKey = make([]byte, 32)
		}
		if err := suiClient.RegisterPeer(ctx, wgPubKey, endpoints, cfg.Identity.Hostname); err != nil {
			log.Printf("[sui] peer registration failed: %v", err)
		}

		// NOTE: Initial peer sync is deferred until after WSS client setup,
		// so peers can be routed through the WSS relay when fallback is active.

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
	var wssHandler *relay.WSSHandler
	var wssClient *relay.WSSClient

	if activeRoles.Relay {
		// Extract bare IP from "ip:port" endpoint
		relayIP := activeRoles.PublicEndpoint
		if host, _, err := net.SplitHostPort(relayIP); err == nil {
			relayIP = host
		}

		// Combined STUN + Relay on shared UDP port
		relayServer = relay.NewServer(relay.Config{
			ListenPort:     cfg.Relay.ListenPort,
			PublicIP:       relayIP,
			MaxConnections: cfg.Relay.MaxConnections,
		})
		if err := relayServer.Start(); err != nil {
			log.Fatalf("[relay] failed to start: %v", err)
		}
		log.Printf("[relay] relay server enabled on :%d (region=%s, isp=%s)",
			cfg.Relay.ListenPort, cfg.Relay.Region, cfg.Relay.ISP)

		// Start WSS relay handler if tcp_fallback config is present
		if cfg.Relay.TCPFallback && activeRoles.PublicEndpoint != "" {
			wssHandler = relay.NewWSSHandler(relayIP, cfg.Relay.WSSPortMin, cfg.Relay.WSSPortMax)

			// Wire relay-WG bridge callbacks: when a WSS client connects,
			// add its WG peer entry on this relay node so mesh traffic flows.
			if wgManager != nil {
				wssHandler.OnPeerConnected = func(suiAddr string, wgPubKey []byte, allocatedPort int) {
					endpoint := fmt.Sprintf("127.0.0.1:%d", allocatedPort)
					if err := wgManager.AddPeer(suiAddr, wgPubKey, []string{endpoint}); err != nil {
						log.Printf("[wss-relay] failed to add WG peer for %s: %v", truncAddr(suiAddr), err)
					} else {
						log.Printf("[wss-relay] added WG peer for %s (endpoint=%s)", truncAddr(suiAddr), endpoint)
					}
				}
				wssHandler.OnPeerDisconnected = func(suiAddr string) {
					if err := wgManager.RemovePeer(suiAddr); err != nil {
						log.Printf("[wss-relay] failed to remove WG peer for %s: %v", truncAddr(suiAddr), err)
					} else {
						log.Printf("[wss-relay] removed WG peer for %s", truncAddr(suiAddr))
					}
				}
			}

			mux := http.NewServeMux()
			mux.Handle("/wg-relay", wssHandler)
			wssAddr := fmt.Sprintf(":%d", cfg.Relay.WSSListenPort)
			go func() {
				if cfg.Relay.WSSCertFile != "" && cfg.Relay.WSSKeyFile != "" {
					log.Printf("[wss-relay] WSS relay server listening on %s (TLS)", wssAddr)
					if err := http.ListenAndServeTLS(wssAddr, cfg.Relay.WSSCertFile, cfg.Relay.WSSKeyFile, mux); err != nil {
						log.Printf("[wss-relay] server error: %v", err)
					}
				} else {
					log.Printf("[wss-relay] WARNING: starting without TLS — use wss_cert_file/wss_key_file or terminate TLS at reverse proxy")
					log.Printf("[wss-relay] WSS relay server listening on %s (plain HTTP)", wssAddr)
					if err := http.ListenAndServe(wssAddr, mux); err != nil {
						log.Printf("[wss-relay] server error: %v", err)
					}
				}
			}()
		}

		// Register as relay on SUI chain so clients can auto-discover this relay
		if suiClient != nil {
			wssPort := cfg.Relay.WSSListenPort
			if cfg.Relay.WSSExternalPort > 0 {
				wssPort = cfg.Relay.WSSExternalPort
			}
			relayAddr := fmt.Sprintf("%s:%d", relayIP, wssPort)
			capacity := uint64(cfg.Relay.MaxConnections)
			if err := suiClient.RegisterRelay(context.Background(), relayAddr, cfg.Relay.Region, cfg.Relay.ISP, capacity); err != nil {
				log.Printf("[sui] relay registration failed: %v", err)
			}
		}

		// Enable IP forwarding on the relay node so mesh traffic between
		// WSS clients can be routed through the WireGuard interface.
		wg.EnableIPForward(cfg.WireGuard.InterfaceName)
	} else if activeRoles.StunServer {
		// STUN-only server (no relay capability)
		stunOnlyServer = relay.NewStunOnlyServer(cfg.Relay.ListenPort)
		if err := stunOnlyServer.Start(); err != nil {
			log.Fatalf("[stun-server] failed to start: %v", err)
		}
		log.Printf("[stun-server] STUN server enabled on :%d", cfg.Relay.ListenPort)
	}

	if activeRoles.Coordinator {
		log.Printf("[coordinator] REST API enabled")
		// REST API already exists in current codebase
	}

	// ======= WSS relay client (for UDP-restricted nodes) =======
	if activeRoles.TCPFallbackActive && suiClient != nil {
		relayURL := cfg.Relay.RelayURL

		// Auto-discover relay from SUI chain when no relay_url configured
		if relayURL == "" {
			ctx := context.Background()
			peers, err := suiClient.QueryPeers(ctx)
			if err != nil {
				log.Printf("[wss-client] failed to query peers for relay discovery: %v", err)
			} else {
				picker := relaypicker.NewPicker(cfg.SUI.OrgID, cfg.Relay.Region, cfg.Relay.ISP, relaypicker.NewRelayLoadCache())
				candidates := picker.SelectBest(peers, 1)
				if len(candidates) > 0 && candidates[0].Peer.RelayAddr != "" {
					relayURL = fmt.Sprintf("wss://%s/wg-relay", candidates[0].Peer.RelayAddr)
					log.Printf("[wss-client] auto-discovered relay: %s (score=%d)", relayURL, candidates[0].Score)
				} else {
					log.Printf("[wss-client] no suitable relay found via SUI chain")
				}
			}
		} else {
			log.Printf("[wss-client] using configured relay_url: %s", relayURL)
		}

		if relayURL != "" {
			wssClient = relay.NewWSSClient(
				relayURL,
				suiClient.Address(),
				suiClient.Keypair(),
				fmt.Sprintf("127.0.0.1:%d", cfg.WireGuard.ListenPort),
			)
			if wgManager != nil {
				wssClient.SetWGPublicKey(wgManager.PublicKey())
			}
			ctx := context.Background()
			relayEndpoint, err := wssClient.Connect(ctx)
			if err != nil {
				log.Printf("[wss-client] failed to connect to relay: %v", err)
				wssClient = nil
			} else {
				log.Printf("[wss-client] connected via WSS relay, endpoint: %s", relayEndpoint)
				// Update SUI registration with the relay endpoint
				if err := suiClient.UpdateEndpoints(ctx, []string{relayEndpoint}); err != nil {
					log.Printf("[wss-client] failed to update SUI endpoint: %v", err)
				}
			}
		}
	}

	// ======= Initial peer sync (after WSS client setup) =======
	if suiClient != nil && wgManager != nil {
		ctx := context.Background()
		peers, err := suiClient.QueryPeers(ctx)
		if err != nil {
			log.Printf("[sui] failed to query peers: %v", err)
		} else if len(peers) > 0 {
			syncPeers(wgManager, wssClient, peers)
		}
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
						syncPeers(wgManager, wssClient, newPeers)
					}
				}
			}

		case sig := <-sigCh:
			log.Printf("received signal %s, shutting down", sig)

			// Graceful WSS relay shutdown
			if wssClient != nil {
				if err := wssClient.Close(); err != nil {
					log.Printf("[wss-client] close failed: %v", err)
				}
			}

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

// syncPeers syncs WireGuard peers, routing through WSS relay when active.
// When wssClient is nil, peers are synced directly via WireGuard.
// When wssClient is active (TCP fallback), each peer gets a local UDP proxy
// and its WG endpoint is rewritten to the local proxy address.
func syncPeers(wgManager *wg.Manager, wssClient *relay.WSSClient, peers []sui.PeerInfo) {
	if wssClient == nil {
		// Direct mode: WireGuard peers use their SUI-registered endpoints
		if err := wgManager.SyncPeers(peers); err != nil {
			log.Printf("[wg] failed to sync peers: %v", err)
		}
		return
	}

	// WSS fallback mode: reconcile relay routes
	ctx := context.Background()

	// Build desired set from incoming peers
	desired := make(map[string]struct{})
	for _, p := range peers {
		if len(p.Endpoints) > 0 && p.Status == sui.PeerStatusOnline {
			desired[p.Address] = struct{}{}
		}
	}

	// Remove relay routes for peers no longer in the desired set
	for addr, peerID := range relayPeers {
		if _, ok := desired[addr]; !ok {
			if err := wssClient.RemovePeer(ctx, peerID); err != nil {
				log.Printf("[wss-client] failed to remove stale peer %s: %v", truncAddr(addr), err)
			} else {
				log.Printf("[wss-client] removed stale relay route for %s", truncAddr(addr))
			}
			delete(relayPeers, addr)
		}
	}

	// Add relay routes for new peers, skip already-routed ones
	for i, p := range peers {
		if len(p.Endpoints) == 0 || p.Status != sui.PeerStatusOnline {
			continue
		}
		if _, routed := relayPeers[p.Address]; routed {
			continue // already has an active relay route
		}
		localAddr, peerID, err := wssClient.AddPeer(ctx, p.Endpoints[0])
		if err != nil {
			log.Printf("[wss-client] failed to add peer %s via relay: %v", truncAddr(p.Address), err)
			continue
		}
		relayPeers[p.Address] = peerID
		// Rewrite endpoint to the local proxy address
		peers[i].Endpoints = []string{localAddr}
		log.Printf("[wss-client] peer %s routed via local proxy %s", truncAddr(p.Address), localAddr)
	}

	if err := wgManager.SyncPeers(peers); err != nil {
		log.Printf("[wg] failed to sync peers: %v", err)
	}
}

// truncAddr safely truncates an address for logging.
func truncAddr(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}
