package coordinator

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/agent"
	"github.com/fractalmind-ai/fractalmind-envd/internal/heartbeat"
	"github.com/fractalmind-ai/fractalmind-envd/internal/ws"
	"github.com/gorilla/websocket"
)

type nodeSnapshot struct {
	ID            string                   `json:"id"`
	HostID        string                   `json:"host_id"`
	Hostname      string                   `json:"hostname"`
	Version       string                   `json:"version"`
	ConnectedAt   time.Time                `json:"connected_at"`
	LastHeartbeat *time.Time               `json:"last_heartbeat"`
	Agents        []agent.Agent            `json:"agents"`
	System        *heartbeat.SystemInfo    `json:"system"`
	UptimeSeconds int64                    `json:"uptime_seconds"`
	RelayLoad     *heartbeat.RelayLoadInfo `json:"relay_load,omitempty"`
}

type registerPayload struct {
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
}

type commandResultPayload struct {
	RequestID string                 `json:"request_id"`
	Result    map[string]interface{} `json:"result"`
}

type alertPayload struct {
	Type    string `json:"type"`
	Agent   string `json:"agent"`
	Session string `json:"session"`
	Message string `json:"message"`
}

type pendingCommand struct {
	resultCh chan map[string]interface{}
}

type nodeConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
}

func (c *nodeConn) WriteJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteJSON(v)
}

func (c *nodeConn) Close() error {
	return c.ws.Close()
}

type connectedNode struct {
	nodeSnapshot
	conn *nodeConn
}

// Manager tracks connected envd workers and proxies REST commands to them.
type Manager struct {
	mu              sync.RWMutex
	nodes           map[string]*connectedNode
	pendingCommands map[string]*pendingCommand
	commandTimeout  time.Duration
	nextTempID      uint64
	nextCommandID   uint64
}

func NewManager(commandTimeout time.Duration) *Manager {
	if commandTimeout <= 0 {
		commandTimeout = 30 * time.Second
	}

	return &Manager{
		nodes:           make(map[string]*connectedNode),
		pendingCommands: make(map[string]*pendingCommand),
		commandTimeout:  commandTimeout,
	}
}

func (m *Manager) tempID() string {
	return fmt.Sprintf("temp-%d", atomic.AddUint64(&m.nextTempID, 1))
}

func (m *Manager) commandID() string {
	return fmt.Sprintf("cmd-%d", atomic.AddUint64(&m.nextCommandID, 1))
}

func (m *Manager) HandleConnection(conn *websocket.Conn) {
	client := &nodeConn{ws: conn}
	nodeID := m.tempID()

	log.Printf("[coordinator] new worker connection: %s", nodeID)

	for {
		var msg ws.Message
		if err := conn.ReadJSON(&msg); err != nil {
			log.Printf("[coordinator] worker %s disconnected: %v", nodeID, err)
			m.removeNode(nodeID)
			return
		}

		nextID, err := m.handleMessage(nodeID, client, msg)
		if err != nil {
			log.Printf("[coordinator] invalid message from %s: %v", nodeID, err)
			continue
		}
		nodeID = nextID
	}
}

func (m *Manager) handleMessage(currentID string, conn *nodeConn, msg ws.Message) (string, error) {
	switch msg.Type {
	case "register":
		var payload registerPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return currentID, fmt.Errorf("decode register payload: %w", err)
		}

		nodeID := payload.HostID
		if nodeID == "" {
			nodeID = payload.Hostname
		}
		if nodeID == "" {
			nodeID = currentID
		}

		now := time.Now()
		node := &connectedNode{
			nodeSnapshot: nodeSnapshot{
				ID:          nodeID,
				HostID:      payload.HostID,
				Hostname:    payload.Hostname,
				Version:     payload.Version,
				ConnectedAt: now,
				Agents:      []agent.Agent{},
			},
			conn: conn,
		}

		m.mu.Lock()
		if currentID != nodeID {
			delete(m.nodes, currentID)
		}
		m.nodes[nodeID] = node
		m.mu.Unlock()

		log.Printf("[coordinator] worker registered: %s (%s, v%s)", nodeID, payload.Hostname, payload.Version)
		return nodeID, nil

	case "heartbeat":
		var payload heartbeat.Payload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return currentID, fmt.Errorf("decode heartbeat payload: %w", err)
		}

		now := time.Now()

		m.mu.Lock()
		if node, ok := m.nodes[currentID]; ok {
			node.LastHeartbeat = &now
			node.Agents = append([]agent.Agent(nil), payload.Agents...)
			system := payload.System
			node.System = &system
			node.UptimeSeconds = payload.Uptime
			if payload.RelayLoad != nil {
				relayLoad := *payload.RelayLoad
				node.RelayLoad = &relayLoad
			} else {
				node.RelayLoad = nil
			}
		}
		m.mu.Unlock()

		return currentID, nil

	case "command_result":
		var payload commandResultPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return currentID, fmt.Errorf("decode command_result payload: %w", err)
		}

		m.mu.Lock()
		pending, ok := m.pendingCommands[payload.RequestID]
		if ok {
			delete(m.pendingCommands, payload.RequestID)
		}
		m.mu.Unlock()

		if ok {
			pending.resultCh <- payload.Result
		}

		return currentID, nil

	case "alert":
		var payload alertPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return currentID, fmt.Errorf("decode alert payload: %w", err)
		}
		log.Printf("[coordinator] alert from %s: %s (%s)", currentID, payload.Type, payload.Message)
		return currentID, nil

	case "pong":
		return currentID, nil

	default:
		log.Printf("[coordinator] unknown message type from %s: %s", currentID, msg.Type)
		return currentID, nil
	}
}

func (m *Manager) PingAll() {
	nodes := m.connections()
	for _, conn := range nodes {
		if err := conn.WriteJSON(ws.Message{Type: "ping"}); err != nil {
			log.Printf("[coordinator] ping failed: %v", err)
		}
	}
}

func (m *Manager) ListNodes() []nodeSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	nodes := make([]nodeSnapshot, 0, len(m.nodes))
	for _, node := range m.nodes {
		nodes = append(nodes, cloneSnapshot(node.nodeSnapshot))
	}

	return nodes
}

func (m *Manager) FindNode(id string) (nodeSnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if node, ok := m.nodes[id]; ok {
		return cloneSnapshot(node.nodeSnapshot), true
	}

	for _, node := range m.nodes {
		if node.Hostname == id {
			return cloneSnapshot(node.nodeSnapshot), true
		}
	}

	return nodeSnapshot{}, false
}

func (m *Manager) SendCommand(nodeID, command, agentID, args string) (map[string]interface{}, error) {
	m.mu.RLock()
	node, ok := m.nodes[nodeID]
	if !ok {
		for _, candidate := range m.nodes {
			if candidate.Hostname == nodeID {
				node = candidate
				ok = true
				break
			}
		}
	}
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("worker %s not found", nodeID)
	}

	requestID := m.commandID()
	pending := &pendingCommand{resultCh: make(chan map[string]interface{}, 1)}

	m.mu.Lock()
	m.pendingCommands[requestID] = pending
	m.mu.Unlock()

	envelope := ws.Message{
		Type: "command",
		Payload: mustRawJSON(ws.CommandPayload{
			Command:   command,
			AgentID:   agentID,
			Args:      args,
			RequestID: requestID,
		}),
	}

	if err := node.conn.WriteJSON(envelope); err != nil {
		m.mu.Lock()
		delete(m.pendingCommands, requestID)
		m.mu.Unlock()
		return nil, fmt.Errorf("send command: %w", err)
	}

	select {
	case result := <-pending.resultCh:
		return result, nil
	case <-time.After(m.commandTimeout):
		m.mu.Lock()
		delete(m.pendingCommands, requestID)
		m.mu.Unlock()
		return nil, fmt.Errorf("command timed out after %s", m.commandTimeout)
	}
}

func (m *Manager) Close() {
	for _, conn := range m.connections() {
		_ = conn.Close()
	}
}

func (m *Manager) connections() []*nodeConn {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conns := make([]*nodeConn, 0, len(m.nodes))
	for _, node := range m.nodes {
		conns = append(conns, node.conn)
	}
	return conns
}

func (m *Manager) removeNode(nodeID string) {
	m.mu.Lock()
	delete(m.nodes, nodeID)
	m.mu.Unlock()
}

func cloneSnapshot(node nodeSnapshot) nodeSnapshot {
	cloned := node
	cloned.Agents = append([]agent.Agent(nil), node.Agents...)
	if node.System != nil {
		system := *node.System
		cloned.System = &system
	}
	if node.RelayLoad != nil {
		relayLoad := *node.RelayLoad
		cloned.RelayLoad = &relayLoad
	}
	return cloned
}

func mustRawJSON(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
