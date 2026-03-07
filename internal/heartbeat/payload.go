package heartbeat

import (
	"runtime"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/agent"
)

// Payload is the heartbeat message sent to Gateway.
type Payload struct {
	HostID    string         `json:"host_id"`
	Hostname  string         `json:"hostname"`
	Timestamp time.Time      `json:"timestamp"`
	Agents    []agent.Agent  `json:"agents"`
	System    SystemInfo     `json:"system"`
	Uptime    int64          `json:"uptime_seconds"`
}

// SystemInfo contains basic system metrics.
type SystemInfo struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	NumCPU int    `json:"num_cpu"`
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
