package coordinator

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fractalmind-ai/fractalmind-envd/internal/agent"
	"github.com/fractalmind-ai/fractalmind-envd/internal/heartbeat"
	"github.com/fractalmind-ai/fractalmind-envd/internal/ws"
	"github.com/gorilla/websocket"
)

func TestCoordinatorListsRegisteredWorkers(t *testing.T) {
	server := NewServer(":0", time.Second)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	conn := dialTestWebSocket(t, testServer.URL)
	defer conn.Close()

	sendWSMessage(t, conn, ws.Message{
		Type: "register",
		Payload: mustRawJSON(registerPayload{
			HostID:   "node-1",
			Hostname: "worker-a",
			Version:  "1.2.3",
		}),
	})
	sendWSMessage(t, conn, ws.Message{
		Type: "heartbeat",
		Payload: mustRawJSON(heartbeat.Payload{
			HostID:    "node-1",
			Hostname:  "worker-a",
			Timestamp: time.Now(),
			Agents: []agent.Agent{
				{ID: "EMP_0001", Session: "EMP_0001", Status: "running"},
			},
			System: heartbeat.SystemInfo{
				OS:     "linux",
				Arch:   "amd64",
				NumCPU: 8,
			},
			Uptime: 42,
		}),
	})

	body := httpGet(t, testServer.URL+"/api/sentinels")

	var resp struct {
		Sentinels []nodeSnapshot `json:"sentinels"`
		Count     int            `json:"count"`
	}
	decodeJSON(t, body, &resp)

	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}
	if len(resp.Sentinels) != 1 {
		t.Fatalf("len(sentinels) = %d, want 1", len(resp.Sentinels))
	}
	if resp.Sentinels[0].Hostname != "worker-a" {
		t.Fatalf("hostname = %q, want worker-a", resp.Sentinels[0].Hostname)
	}
	if len(resp.Sentinels[0].Agents) != 1 {
		t.Fatalf("len(agents) = %d, want 1", len(resp.Sentinels[0].Agents))
	}
}

func TestCoordinatorCommandProxy(t *testing.T) {
	server := NewServer(":0", 2*time.Second)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	conn := dialTestWebSocket(t, testServer.URL)
	defer conn.Close()

	sendWSMessage(t, conn, ws.Message{
		Type: "register",
		Payload: mustRawJSON(registerPayload{
			HostID:   "node-2",
			Hostname: "worker-b",
			Version:  "dev",
		}),
	})

	commandDone := make(chan struct{})
	go func() {
		defer close(commandDone)

		var msg ws.Message
		if err := conn.ReadJSON(&msg); err != nil {
			t.Errorf("read command: %v", err)
			return
		}
		if msg.Type != "command" {
			t.Errorf("message type = %q, want command", msg.Type)
			return
		}

		var payload ws.CommandPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			t.Errorf("decode command payload: %v", err)
			return
		}
		if payload.Command != "status" {
			t.Errorf("command = %q, want status", payload.Command)
			return
		}

		sendWSMessage(t, conn, ws.Message{
			Type: "command_result",
			Payload: mustRawJSON(commandResultPayload{
				RequestID: payload.RequestID,
				Result: map[string]interface{}{
					"success": true,
					"agents": []agent.Agent{
						{ID: "EMP_0002", Session: "EMP_0002", Status: "running"},
					},
				},
			}),
		})
	}()

	body := httpPostJSON(t, testServer.URL+"/api/sentinels/node-2/command", commandRequest{
		Command: "status",
	})
	<-commandDone

	var resp struct {
		Success bool          `json:"success"`
		Agents  []agent.Agent `json:"agents"`
	}
	decodeJSON(t, body, &resp)

	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if len(resp.Agents) != 1 || resp.Agents[0].ID != "EMP_0002" {
		t.Fatalf("unexpected agents payload: %+v", resp.Agents)
	}
}

func dialTestWebSocket(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func sendWSMessage(t *testing.T, conn *websocket.Conn, msg ws.Message) {
	t.Helper()
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write websocket message: %v", err)
	}
}

func httpGet(t *testing.T, url string) []byte {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status = %d, body=%s", url, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET response: %v", err)
	}
	return body
}

func httpPostJSON(t *testing.T, url string, payload interface{}) []byte {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s status = %d, body=%s", url, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read POST response: %v", err)
	}
	return body
}

func decodeJSON(t *testing.T, body []byte, out interface{}) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}
