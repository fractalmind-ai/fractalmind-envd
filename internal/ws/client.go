package ws

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message is the envelope for all WebSocket messages.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// CommandPayload is a command from Gateway.
type CommandPayload struct {
	Command   string `json:"command"`    // status, restart, kill, logs
	AgentID   string `json:"agent_id"`   // target agent (optional)
	Args      string `json:"args"`       // additional arguments
	RequestID string `json:"request_id"` // for response correlation
}

// Client manages the WebSocket connection to Gateway.
type Client struct {
	url           string
	reconnectWait time.Duration
	conn          *websocket.Conn
	mu            sync.Mutex
	done          chan struct{}
	onCommand     func(CommandPayload)
}

// NewClient creates a WebSocket client.
func NewClient(url string, reconnectWait time.Duration) *Client {
	return &Client{
		url:           url,
		reconnectWait: reconnectWait,
		done:          make(chan struct{}),
	}
}

// OnCommand sets the handler for incoming commands.
func (c *Client) OnCommand(handler func(CommandPayload)) {
	c.onCommand = handler
}

// Connect establishes and maintains the WebSocket connection.
// It blocks until the done channel is closed.
func (c *Client) Connect() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		log.Printf("[ws] connecting to %s ...", c.url)

		conn, _, err := websocket.DefaultDialer.Dial(c.url, nil)
		if err != nil {
			log.Printf("[ws] connect failed: %v, retrying in %s", err, c.reconnectWait)
			time.Sleep(c.reconnectWait)
			continue
		}

		log.Printf("[ws] connected to %s", c.url)

		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()

		c.readLoop(conn)

		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()

		log.Printf("[ws] disconnected, reconnecting in %s", c.reconnectWait)
		time.Sleep(c.reconnectWait)
	}
}

// Send sends a message to Gateway.
func (c *Client) Send(msgType string, payload interface{}) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	msg := Message{
		Type:    msgType,
		Payload: data,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return conn.WriteJSON(msg)
}

// Close shuts down the client.
func (c *Client) Close() {
	close(c.done)
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.mu.Unlock()
}

func (c *Client) readLoop(conn *websocket.Conn) {
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[ws] read error: %v", err)
			}
			return
		}

		switch msg.Type {
		case "command":
			if c.onCommand != nil {
				var cmd CommandPayload
				if err := json.Unmarshal(msg.Payload, &cmd); err != nil {
					log.Printf("[ws] invalid command payload: %v", err)
					continue
				}
				c.onCommand(cmd)
			}
		case "ping":
			c.Send("pong", nil)
		default:
			log.Printf("[ws] unknown message type: %s", msg.Type)
		}
	}
}
