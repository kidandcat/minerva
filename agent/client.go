package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message types
const (
	MsgTypeRegister = "register"
	MsgTypeTask     = "task"
	MsgTypeResult   = "result"
	MsgTypeStatus   = "status"
	MsgTypePing     = "ping"
	MsgTypePong     = "pong"
	MsgTypeError    = "error"
)

// Message represents a WebSocket message
type Message struct {
	Type string `json:"type"`

	// Register
	Name     string `json:"name,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Password string `json:"password,omitempty"`

	// Task
	ID     string `json:"id,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Dir    string `json:"dir,omitempty"` // Optional override for working dir

	// Result
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
}

// Client handles the connection to Minerva
type Client struct {
	serverURL  string
	agentName  string
	workingDir string
	password   string

	conn     *websocket.Conn
	connLock sync.Mutex

	executor *Executor

	done chan struct{}
}

// NewClient creates a new agent client
func NewClient(serverURL, agentName, workingDir, password string) *Client {
	return &Client{
		serverURL:  serverURL,
		agentName:  agentName,
		workingDir: workingDir,
		password:   password,
		executor:   NewExecutor(),
		done:       make(chan struct{}),
	}
}

// Run connects to the server and handles messages
func (c *Client) Run() error {
	for {
		select {
		case <-c.done:
			return nil
		default:
		}

		if err := c.connect(); err != nil {
			log.Printf("Connection failed: %v, retrying in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		c.handleMessages()
	}
}

// Close shuts down the client
func (c *Client) Close() {
	close(c.done)
	c.connLock.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.connLock.Unlock()
}

func (c *Client) connect() error {
	conn, _, err := websocket.DefaultDialer.Dial(c.serverURL, nil)
	if err != nil {
		return err
	}

	c.connLock.Lock()
	c.conn = conn
	c.connLock.Unlock()

	// Register with the server
	return c.send(Message{
		Type:     MsgTypeRegister,
		Name:     c.agentName,
		Cwd:      c.workingDir,
		Password: c.password,
	})
}

func (c *Client) send(msg Message) error {
	c.connLock.Lock()
	defer c.connLock.Unlock()

	if c.conn == nil {
		return nil
	}
	return c.conn.WriteJSON(msg)
}

func (c *Client) handleMessages() {
	defer func() {
		c.connLock.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.connLock.Unlock()
	}()

	// Start ping ticker
	go c.pingLoop()

	for {
		select {
		case <-c.done:
			return
		default:
		}

		var msg Message
		if err := c.conn.ReadJSON(&msg); err != nil {
			log.Printf("Read error: %v", err)
			return
		}

		switch msg.Type {
		case MsgTypeTask:
			go c.handleTask(msg)
		case MsgTypePing:
			c.send(Message{Type: MsgTypePong})
		}
	}
}

func (c *Client) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := c.send(Message{Type: MsgTypePing}); err != nil {
				return
			}
		}
	}
}

func (c *Client) handleTask(task Message) {
	log.Printf("Received task %s: %s", task.ID, truncate(task.Prompt, 50))

	// Determine working directory
	dir := c.workingDir
	if task.Dir != "" {
		dir = task.Dir
	}

	// Execute claude
	result, err := c.executor.Run(task.Prompt, dir)

	// Send result
	msg := Message{
		Type: MsgTypeResult,
		ID:   task.ID,
	}

	if err != nil {
		msg.Error = err.Error()
		msg.ExitCode = 1
	} else {
		msg.Output = result.Output
		msg.ExitCode = result.ExitCode
		msg.Duration = result.DurationMs
	}

	if err := c.send(msg); err != nil {
		log.Printf("Failed to send result: %v", err)
	}

	// Log for debugging
	data, _ := json.MarshalIndent(msg, "", "  ")
	log.Printf("Task %s completed:\n%s", task.ID, string(data))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
