package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/heartbeat"
	"github.com/gorilla/websocket"
)

type commandRequest struct {
	Command string `json:"command"`
	AgentID string `json:"agent_id"`
	Args    string `json:"args"`
}

type sentinelSummary struct {
	ID            string                `json:"id"`
	HostID        string                `json:"host_id"`
	Hostname      string                `json:"hostname"`
	Version       string                `json:"version"`
	ConnectedAt   time.Time             `json:"connected_at"`
	LastHeartbeat *time.Time            `json:"last_heartbeat"`
	AgentCount    int                   `json:"agent_count"`
	UptimeSeconds int64                 `json:"uptime_seconds"`
	System        *heartbeat.SystemInfo `json:"system"`
}

// Server exposes the embedded coordinator REST and WebSocket API.
type Server struct {
	addr         string
	manager      *Manager
	upgrader     websocket.Upgrader
	httpServer   *http.Server
	listener     net.Listener
	pingInterval time.Duration
	done         chan struct{}
}

func NewServer(addr string, commandTimeout time.Duration) *Server {
	if addr == "" {
		addr = ":8080"
	}

	return &Server{
		addr:    addr,
		manager: NewManager(commandTimeout),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
		pingInterval: 30 * time.Second,
		done:         make(chan struct{}),
	}
}

func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}

	s.listener = listener
	s.httpServer = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[coordinator] http server error: %v", err)
		}
	}()

	go s.pingLoop()

	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/sentinels", s.handleListSentinels)
	mux.HandleFunc("GET /api/sentinels/{id}", s.handleGetSentinel)
	mux.HandleFunc("GET /api/sentinels/{id}/agents", s.handleGetAgents)
	mux.HandleFunc("POST /api/sentinels/{id}/command", s.handleCommand)
	mux.HandleFunc("GET /ws", s.handleWebSocket)
	return mux
}

func (s *Server) Shutdown(ctx context.Context) error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}

	s.manager.Close()

	if s.httpServer == nil {
		return nil
	}

	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	nodes := s.manager.ListNodes()
	totalAgents := 0
	for _, node := range nodes {
		totalAgents += len(node.Agents)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":       "ok",
		"sentinels":    len(nodes),
		"total_agents": totalAgents,
	})
}

func (s *Server) handleListSentinels(w http.ResponseWriter, _ *http.Request) {
	nodes := s.manager.ListNodes()
	summaries := make([]sentinelSummary, 0, len(nodes))
	for _, node := range nodes {
		summaries = append(summaries, sentinelSummary{
			ID:            node.ID,
			HostID:        node.HostID,
			Hostname:      node.Hostname,
			Version:       node.Version,
			ConnectedAt:   node.ConnectedAt,
			LastHeartbeat: node.LastHeartbeat,
			AgentCount:    len(node.Agents),
			UptimeSeconds: node.UptimeSeconds,
			System:        node.System,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sentinels": summaries,
		"count":     len(summaries),
	})
}

func (s *Server) handleGetSentinel(w http.ResponseWriter, r *http.Request) {
	node, ok := s.manager.FindNode(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sentinel not found"})
		return
	}

	writeJSON(w, http.StatusOK, node)
}

func (s *Server) handleGetAgents(w http.ResponseWriter, r *http.Request) {
	node, ok := s.manager.FindNode(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sentinel not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agents": node.Agents,
		"count":  len(node.Agents),
	})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required"})
		return
	}

	result, err := s.manager.SendCommand(r.PathValue("id"), req.Command, req.AgentID, req.Args)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[coordinator] websocket upgrade failed: %v", err)
		return
	}

	s.manager.HandleConnection(conn)
}

func (s *Server) pingLoop() {
	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.manager.PingAll()
		case <-s.done:
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[coordinator] write response failed: %v", err)
	}
}
