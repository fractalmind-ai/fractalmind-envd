package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Agent represents a discovered AI agent process.
type Agent struct {
	ID        string    `json:"id"`
	Session   string    `json:"session"`
	Status    string    `json:"status"` // "running", "dead", "missing"
	StartedAt time.Time `json:"started_at,omitempty"`
}

// Scanner discovers AI agents running on the local machine.
type Scanner struct {
	method string
}

// NewScanner creates a scanner with the given method (currently only "tmux").
func NewScanner(method string) *Scanner {
	return &Scanner{method: method}
}

// Scan discovers all agent sessions.
func (s *Scanner) Scan() ([]Agent, error) {
	switch s.method {
	case "tmux":
		return s.scanTmux()
	default:
		return nil, fmt.Errorf("unsupported scan method: %s", s.method)
	}
}

// scanTmux lists tmux sessions and identifies agent sessions.
func (s *Scanner) scanTmux() ([]Agent, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}:#{session_created}:#{session_attached}").Output()
	if err != nil {
		// tmux not running or no sessions
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w", err)
	}

	var agents []Agent
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 1 {
			continue
		}

		sessionName := parts[0]

		// Only track sessions that look like agent sessions (EMP_XXXX pattern)
		if !isAgentSession(sessionName) {
			continue
		}

		agent := Agent{
			ID:      extractAgentID(sessionName),
			Session: sessionName,
			Status:  "running",
		}

		if len(parts) >= 2 {
			if ts, err := parseUnixTimestamp(parts[1]); err == nil {
				agent.StartedAt = ts
			}
		}

		agents = append(agents, agent)
	}

	return agents, nil
}

// RestartAgent attempts to restart a dead agent session.
func (s *Scanner) RestartAgent(sessionName string) error {
	// Check if session exists
	err := exec.Command("tmux", "has-session", "-t", sessionName).Run()
	if err == nil {
		// Session exists, send respawn command
		return exec.Command("tmux", "respawn-pane", "-t", sessionName, "-k").Run()
	}
	return fmt.Errorf("session %s not found, cannot restart", sessionName)
}

// isAgentSession checks if a tmux session name matches agent naming convention.
func isAgentSession(name string) bool {
	// Match EMP_XXXX or agent-related session names
	return strings.HasPrefix(name, "EMP_") ||
		strings.HasPrefix(name, "agent-") ||
		strings.HasPrefix(name, "team-")
}

// extractAgentID extracts the agent ID from a session name.
func extractAgentID(sessionName string) string {
	// For EMP_XXXX sessions, the session name IS the ID
	if strings.HasPrefix(sessionName, "EMP_") {
		return sessionName
	}
	return sessionName
}

func parseUnixTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	var ts int64
	_, err := fmt.Sscanf(s, "%d", &ts)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(ts, 0), nil
}
