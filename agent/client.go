package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message types
const (
	MsgTypeRegister     = "register"
	MsgTypeTask         = "task"
	MsgTypeAck          = "ack"
	MsgTypeResult       = "result"
	MsgTypeStatus       = "status"
	MsgTypePing         = "ping"
	MsgTypePong         = "pong"
	MsgTypeError        = "error"
	MsgTypeListProjects = "list_projects"
	MsgTypeProjects     = "projects"
)

// Message represents a WebSocket message
type Message struct {
	Type string `json:"type"`

	// Register
	Name     string   `json:"name,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Password string   `json:"password,omitempty"`
	Projects []string `json:"projects,omitempty"`

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

		log.Printf("Connected to server")
		c.handleMessages()
		log.Printf("Disconnected, will reconnect...")
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
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		NetDial: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).Dial,
	}

	header := make(http.Header)
	if c.password != "" {
		header.Set("Authorization", "Bearer "+c.password)
	}
	conn, _, err := dialer.Dial(c.serverURL, header)
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
		Projects: listHomeProjects(),
	})
}

func (c *Client) send(msg Message) error {
	c.connLock.Lock()
	defer c.connLock.Unlock()

	if c.conn == nil {
		return nil
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(msg)
}

// closeConn closes the current connection (safe to call multiple times)
func (c *Client) closeConn() {
	c.connLock.Lock()
	defer c.connLock.Unlock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

func (c *Client) handleMessages() {
	stopPing := make(chan struct{})
	defer func() {
		close(stopPing)
		c.closeConn()
	}()

	// Set up pong handler to reset read deadline (handles WebSocket protocol pongs)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Start ping ticker - when it fails, it closes the connection to unblock ReadJSON
	go c.pingLoop(stopPing)

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

		// Reset read deadline on any application message
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		switch msg.Type {
		case MsgTypeTask:
			go c.handleTask(msg)
		case MsgTypeListProjects:
			c.send(Message{
				Type:     MsgTypeProjects,
				ID:       msg.ID,
				Projects: listHomeProjects(),
			})
		case MsgTypePing:
			c.send(Message{Type: MsgTypePong})
		}
	}
}

func (c *Client) pingLoop(stop chan struct{}) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-stop:
			return
		case <-ticker.C:
			c.connLock.Lock()
			if c.conn == nil {
				c.connLock.Unlock()
				return
			}
			// Send WebSocket protocol-level ping (keeps proxies alive)
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			c.connLock.Unlock()

			if err != nil {
				log.Printf("Ping failed: %v, closing connection", err)
				// Close connection to unblock ReadJSON in handleMessages
				c.closeConn()
				return
			}

			// Also send application-level ping
			if err := c.send(Message{Type: MsgTypePing}); err != nil {
				log.Printf("Application ping failed: %v, closing connection", err)
				c.closeConn()
				return
			}
		}
	}
}

func (c *Client) handleTask(task Message) {
	log.Printf("[Task %s] Received: %s", task.ID, truncate(task.Prompt, 100))

	// Determine working directory
	dir := c.workingDir
	if task.Dir != "" {
		dir = task.Dir
	}
	log.Printf("[Task %s] Working dir: %s", task.ID, dir)

	// Start claude (non-blocking) with 55 min timeout
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	start := time.Now()

	cmd, stdout, stderr, err := c.executor.Start(ctx, task.Prompt, dir)
	if err != nil {
		cancel()
		// Send ACK with error - claude failed to start
		log.Printf("[Task %s] Failed to start: %v", task.ID, err)
		c.send(Message{
			Type:  MsgTypeAck,
			ID:    task.ID,
			Error: err.Error(),
		})
		return
	}

	// Send ACK - claude started successfully
	log.Printf("[Task %s] Claude started, sending ACK", task.ID)
	if err := c.send(Message{
		Type: MsgTypeAck,
		ID:   task.ID,
	}); err != nil {
		log.Printf("[Task %s] WARNING: Failed to send ACK: %v", task.ID, err)
	}

	// Wait for completion in background, then send result
	go func() {
		defer cancel()
		result := c.executor.Wait(cmd, stdout, stderr, start)

		msg := Message{
			Type:     MsgTypeResult,
			ID:       task.ID,
			Output:   result.Output,
			ExitCode: result.ExitCode,
			Duration: result.DurationMs,
		}

		log.Printf("[Task %s] Completed: exit=%d, output=%d bytes, duration=%dms",
			task.ID, result.ExitCode, len(result.Output), result.DurationMs)

		if err := c.send(msg); err != nil {
			log.Printf("[Task %s] Failed to send result: %v", task.ID, err)
		} else {
			log.Printf("[Task %s] Result sent successfully", task.ID)
		}
	}()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// listHomeProjects returns all directories in the user's home folder
func listHomeProjects() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}

	var projects []string
	for _, entry := range entries {
		// Skip hidden directories and files
		if entry.Name()[0] == '.' {
			continue
		}
		if entry.IsDir() {
			projects = append(projects, filepath.Join(home, entry.Name()))
		}
	}
	return projects
}
