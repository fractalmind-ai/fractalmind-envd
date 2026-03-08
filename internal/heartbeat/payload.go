package heartbeat

import (
	"runtime"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/agent"
)

// Payload is the heartbeat message sent via P2P (WireGuard).
type Payload struct {
	HostID    string         `json:"host_id"`
	Hostname  string         `json:"hostname"`
	Timestamp time.Time      `json:"timestamp"`
	Agents    []agent.Agent  `json:"agents"`
	System    SystemInfo     `json:"system"`
	Uptime    int64          `json:"uptime_seconds"`
	RelayLoad *RelayLoadInfo `json:"relay_load,omitempty"` // Only present on relay nodes
}

// SystemInfo contains basic system metrics.
type SystemInfo struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	NumCPU int    `json:"num_cpu"`
}

// RelayLoadInfo is broadcast via P2P heartbeat by relay nodes.
// This data is NOT stored on-chain (saves ~83% gas).
// Receiving peers cache it locally for relay selection scoring.
type RelayLoadInfo struct {
	CurrentLoad  uint64 `json:"current_load"`
	Capacity     uint64 `json:"capacity"`
	AvgLatencyMs uint64 `json:"avg_latency_ms"`
}

// NewPayload creates a heartbeat payload.
func NewPayload(hostID, hostname string, agents []agent.Agent, startedAt time.Time) *Payload {
	return &Payload{
		HostID:    hostID,
		Hostname:  hostname,
		Timestamp: time.Now(),
		Agents:    agents,
		System: SystemInfo{
			OS:     runtime.GOOS,
			Arch:   runtime.GOARCH,
			NumCPU: runtime.NumCPU(),
		},
		Uptime: int64(time.Since(startedAt).Seconds()),
	}
}

// WithRelayLoad attaches relay load info to the heartbeat payload.
// Only called when this node is acting as a relay.
func (p *Payload) WithRelayLoad(currentLoad, capacity, avgLatencyMs uint64) *Payload {
	p.RelayLoad = &RelayLoadInfo{
		CurrentLoad:  currentLoad,
		Capacity:     capacity,
		AvgLatencyMs: avgLatencyMs,
	}
	return p
}
