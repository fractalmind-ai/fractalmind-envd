package main

import (
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
	"github.com/fractalmind-ai/fractalmind-envd/internal/ws"
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

	// Initialize components
	scanner := agent.NewScanner(cfg.Agents.ScanMethod)
	wsClient := ws.NewClient(cfg.Gateway.URL, reconnectWait)

	// Track agents and restart counts
	restartCounts := make(map[string]int)
	var lastAgents []agent.Agent

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

	// Initial scan
	if agents, err := scanner.Scan(); err == nil {
		lastAgents = agents
		log.Printf("initial scan: %d agents found", len(agents))
	}

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

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
			if err := wsClient.Send("heartbeat", payload); err != nil {
				log.Printf("[heartbeat] send failed: %v", err)
			}

		case sig := <-sigCh:
			log.Printf("received signal %s, shutting down", sig)
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
