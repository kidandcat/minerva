package main

import (
	"context"
	"encoding/base64"
	"fmt"
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
	MsgTypeHeartbeat    = "heartbeat"
	MsgTypeError        = "error"
	MsgTypeListProjects = "list_projects"
	MsgTypeProjects     = "projects"
	MsgTypeKill         = "kill"
	MsgTypeKilled       = "killed"
	MsgTypeFileUpload   = "file_upload"
)

var errConnNil = fmt.Errorf("connection is nil")

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

	// File upload
	FileName string `json:"file_name,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	FileData string `json:"file_data,omitempty"` // base64 encoded
}

// RunningTask represents a task that is currently executing
type RunningTask struct {
	cancel context.CancelFunc
	cmd    *os.Process
}

// Client handles the connection to Minerva
type Client struct {
	serverURL  string
	agentName  string
	workingDir string
	password   string

	conn     *websocket.Conn
	connLock sync.Mutex

	executor     *Executor
	runningTasks sync.Map // taskID -> *RunningTask

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
		log.Printf("Disconnected, will reconnect in 5s...")
		time.Sleep(5 * time.Second)
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
		return errConnNil
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(msg)
}

// sendWithRetry attempts to send a message with exponential backoff.
// Used for result messages that must not be lost.
func (c *Client) sendWithRetry(msg Message, maxRetries int) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * 5 * time.Second // 5s, 10s, 20s
			log.Printf("[Task %s] Retry %d/%d in %v...", msg.ID, attempt, maxRetries, delay)
			time.Sleep(delay)
		}

		lastErr = c.send(msg)
		if lastErr == nil {
			return nil
		}
		log.Printf("[Task %s] Send attempt %d failed: %v", msg.ID, attempt+1, lastErr)
	}
	return fmt.Errorf("all %d send attempts failed, last error: %w", maxRetries+1, lastErr)
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

	// Set up handlers to reset read deadline on any WebSocket activity
	c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	c.conn.SetPingHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		c.connLock.Lock()
		defer c.connLock.Unlock()
		if c.conn != nil {
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			return c.conn.WriteMessage(websocket.PongMessage, nil)
		}
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
		c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		switch msg.Type {
		case MsgTypeTask:
			go c.handleTask(msg)
		case MsgTypeKill:
			c.handleKill(msg)
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

	cmd, stdout, stderr, err := c.executor.Start(ctx, task.ID, task.Prompt, dir)
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

	// Register the running task for kill functionality
	c.runningTasks.Store(task.ID, &RunningTask{
		cancel: cancel,
		cmd:    cmd.Process,
	})

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
		defer func() {
			cancel()
			c.runningTasks.Delete(task.ID)
		}()

		// Start heartbeat goroutine - sends periodic heartbeats while task is running
		heartbeatDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatDone:
					return
				case <-ticker.C:
					if err := c.send(Message{
						Type: MsgTypeHeartbeat,
						ID:   task.ID,
					}); err != nil {
						log.Printf("[Task %s] Heartbeat send failed: %v", task.ID, err)
					} else {
						log.Printf("[Task %s] Heartbeat sent (running %v)", task.ID, time.Since(start).Round(time.Second))
					}
				}
			}
		}()

		result := c.executor.Wait(cmd, stdout, stderr, start)
		close(heartbeatDone)

		// Check if the task was killed (context cancelled)
		if ctx.Err() == context.Canceled {
			// Task was killed, don't send result (killed message already sent)
			log.Printf("[Task %s] Task was killed, not sending result", task.ID)
			return
		}

		// Collect and send output files before sending the result
		if outputDir := c.executor.GetOutputDir(task.ID); outputDir != "" {
			c.collectAndSendFiles(task.ID, outputDir)
		}

		msg := Message{
			Type:     MsgTypeResult,
			ID:       task.ID,
			Output:   result.Output,
			ExitCode: result.ExitCode,
			Duration: result.DurationMs,
		}

		log.Printf("[Task %s] Completed: exit=%d, output=%d bytes, duration=%dms",
			task.ID, result.ExitCode, len(result.Output), result.DurationMs)

		// Use retry logic for result delivery - this is critical data
		if err := c.sendWithRetry(msg, 3); err != nil {
			log.Printf("[Task %s] CRITICAL: Failed to deliver result after retries: %v", task.ID, err)
			log.Printf("[Task %s] Lost output (%d bytes): %s", task.ID, len(result.Output), truncate(result.Output, 500))
		} else {
			log.Printf("[Task %s] Result sent successfully", task.ID)
		}
	}()
}

func (c *Client) handleKill(msg Message) {
	log.Printf("[Task %s] Kill signal received", msg.ID)

	val, ok := c.runningTasks.Load(msg.ID)
	if !ok {
		log.Printf("[Task %s] Task not found or already completed", msg.ID)
		return
	}

	runningTask := val.(*RunningTask)

	// Kill the process
	if runningTask.cmd != nil {
		log.Printf("[Task %s] Killing process PID %d", msg.ID, runningTask.cmd.Pid)
		runningTask.cmd.Kill()
	}

	// Cancel the context
	runningTask.cancel()

	// Remove from running tasks
	c.runningTasks.Delete(msg.ID)

	// Send killed confirmation
	c.send(Message{
		Type: MsgTypeKilled,
		ID:   msg.ID,
	})

	log.Printf("[Task %s] Task killed successfully", msg.ID)
}

// collectAndSendFiles scans the output directory for files and sends them to the server
func (c *Client) collectAndSendFiles(taskID, outputDir string) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[Task %s] Failed to read output dir %s: %v", taskID, outputDir, err)
		}
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(outputDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			log.Printf("[Task %s] Failed to stat file %s: %v", taskID, filePath, err)
			continue
		}

		// Skip files larger than 50MB (Telegram limit)
		if info.Size() > 50*1024*1024 {
			log.Printf("[Task %s] Skipping file %s: too large (%d bytes)", taskID, entry.Name(), info.Size())
			continue
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("[Task %s] Failed to read file %s: %v", taskID, filePath, err)
			continue
		}

		encoded := base64.StdEncoding.EncodeToString(data)
		msg := Message{
			Type:     MsgTypeFileUpload,
			ID:       taskID,
			FileName: entry.Name(),
			FileSize: info.Size(),
			FileData: encoded,
		}

		log.Printf("[Task %s] Sending file: %s (%d bytes)", taskID, entry.Name(), info.Size())
		if err := c.sendWithRetry(msg, 3); err != nil {
			log.Printf("[Task %s] Failed to send file %s: %v", taskID, entry.Name(), err)
		} else {
			log.Printf("[Task %s] File %s sent successfully", taskID, entry.Name())
		}
	}

	// Clean up the output directory
	if err := os.RemoveAll(outputDir); err != nil {
		log.Printf("[Task %s] Failed to clean output dir %s: %v", taskID, outputDir, err)
	} else {
		log.Printf("[Task %s] Cleaned output dir %s", taskID, outputDir)
	}
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
