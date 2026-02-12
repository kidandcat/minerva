package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// AgentConfig is loaded from ~/.minerva-agent.json
type AgentConfig struct {
	HomeDir string `json:"home_dir"`
}

func loadConfig() AgentConfig {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".minerva-agent.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return AgentConfig{HomeDir: home}
	}

	var cfg AgentConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("Warning: failed to parse %s: %v", configPath, err)
		return AgentConfig{HomeDir: home}
	}

	if cfg.HomeDir == "" {
		cfg.HomeDir = home
	}

	log.Printf("Loaded config from %s (homeDir=%s)", configPath, cfg.HomeDir)
	return cfg
}

// Message types
const (
	MsgRegister     = "register"
	MsgTask         = "task"
	MsgAck          = "ack"
	MsgResult       = "result"
	MsgPing         = "ping"
	MsgPong         = "pong"
	MsgListProjects = "list_projects"
	MsgProjects     = "projects"
	MsgKill         = "kill"
	MsgKilled       = "killed"
)

// AgentMessage represents a message to/from Minerva
type AgentMessage struct {
	Type string `json:"type"`

	// Register
	Name     string   `json:"name,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Password string   `json:"password,omitempty"`
	Projects []string `json:"projects,omitempty"`

	// Task
	ID     string `json:"id,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Dir    string `json:"dir,omitempty"`

	// Result
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
}

type Agent struct {
	name         string
	relayURL     string
	password     string
	homeDir      string
	conn         *websocket.Conn
	mu           sync.Mutex
	stopCh       chan struct{}
	activeTasks  sync.Map // taskID -> *exec.Cmd
}

func main() {
	name := flag.String("name", "", "Agent name (required)")
	relayURL := flag.String("relay", os.Getenv("MINERVA_RELAY_URL"), "Relay WebSocket URL")
	password := flag.String("password", os.Getenv("MINERVA_PASSWORD"), "Agent password")
	flag.Parse()

	if *name == "" {
		hostname, _ := os.Hostname()
		*name = hostname
	}

	if *relayURL == "" {
		log.Fatal("Relay URL required: use -relay or MINERVA_RELAY_URL env var")
	}

	cfg := loadConfig()

	agent := &Agent{
		name:     *name,
		relayURL: *relayURL,
		password: *password,
		homeDir:  cfg.HomeDir,
		stopCh:   make(chan struct{}),
	}

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		close(agent.stopCh)
	}()

	agent.run()
}

func (a *Agent) run() {
	for {
		select {
		case <-a.stopCh:
			return
		default:
		}

		err := a.connect()
		if err != nil {
			log.Printf("Connection failed: %v, retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		a.handleMessages()
		log.Println("Connection lost, reconnecting...")
		time.Sleep(1 * time.Second)
	}
}

func (a *Agent) connect() error {
	conn, _, err := websocket.DefaultDialer.Dial(a.relayURL, nil)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()

	// Get projects (directories in home)
	projects := a.listProjects()

	// Register
	reg := AgentMessage{
		Type:     MsgRegister,
		Name:     a.name,
		Cwd:      a.homeDir,
		Password: a.password,
		Projects: projects,
	}

	if err := conn.WriteJSON(reg); err != nil {
		conn.Close()
		return err
	}

	log.Printf("Connected to Minerva as '%s'", a.name)
	return nil
}

func (a *Agent) listProjects() []string {
	var projects []string
	entries, err := os.ReadDir(a.homeDir)
	if err != nil {
		return projects
	}

	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(a.homeDir, e.Name())
		if isProject(path) {
			projects = append(projects, path)
		}
	}
	return projects
}

func isProject(path string) bool {
	indicators := []string{".git", "package.json", "go.mod", "Cargo.toml", "pyproject.toml", "Makefile"}
	for _, ind := range indicators {
		if _, err := os.Stat(filepath.Join(path, ind)); err == nil {
			return true
		}
	}
	return false
}

func (a *Agent) handleMessages() {
	// Ping ticker
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	// Read messages in goroutine
	msgCh := make(chan AgentMessage)
	errCh := make(chan error)

	go func() {
		for {
			var msg AgentMessage
			if err := a.conn.ReadJSON(&msg); err != nil {
				errCh <- err
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case <-a.stopCh:
			a.conn.Close()
			return

		case <-ticker.C:
			a.send(AgentMessage{Type: MsgPing})

		case err := <-errCh:
			log.Printf("Read error: %v", err)
			return

		case msg := <-msgCh:
			a.handleMessage(msg)
		}
	}
}

func (a *Agent) handleMessage(msg AgentMessage) {
	switch msg.Type {
	case MsgPing:
		a.send(AgentMessage{Type: MsgPong})

	case MsgPong:
		// Keepalive response, ignore

	case MsgListProjects:
		projects := a.listProjects()
		a.send(AgentMessage{
			Type:     MsgProjects,
			ID:       msg.ID,
			Projects: projects,
		})

	case MsgTask:
		go a.executeTask(msg)

	case MsgKill:
		a.killTask(msg.ID)
	}
}

func (a *Agent) executeTask(task AgentMessage) {
	log.Printf("Executing task %s: %s", task.ID, truncate(task.Prompt, 50))

	start := time.Now()

	// Determine working directory
	workDir := a.homeDir
	if task.Dir != "" {
		if filepath.IsAbs(task.Dir) {
			workDir = task.Dir
		} else {
			workDir = filepath.Join(a.homeDir, task.Dir)
		}
	}

	// Execute claude CLI
	cmd := exec.Command("claude",
		"--print",
		"--dangerously-skip-permissions",
		"--output-format", "text",
		task.Prompt,
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Start the process
	err := cmd.Start()
	if err != nil {
		// Send ACK with error
		a.send(AgentMessage{
			Type:  MsgAck,
			ID:    task.ID,
			Error: fmt.Sprintf("Failed to start claude: %v", err),
		})
		return
	}

	// Store the process so we can kill it later
	a.activeTasks.Store(task.ID, cmd)

	// Send ACK - process started successfully
	a.send(AgentMessage{
		Type: MsgAck,
		ID:   task.ID,
	})

	// Wait for process to complete
	err = cmd.Wait()
	duration := time.Since(start).Milliseconds()

	// Remove from active tasks
	a.activeTasks.Delete(task.ID)

	result := AgentMessage{
		Type:     MsgResult,
		ID:       task.ID,
		Duration: duration,
	}

	if err != nil {
		result.Error = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n--- stderr ---\n" + stderr.String()
	}
	result.Output = output

	log.Printf("Task %s completed in %dms (exit=%d)", task.ID, duration, result.ExitCode)
	a.send(result)
}

// killTask kills a running task by its ID
func (a *Agent) killTask(taskID string) {
	taskInfo, ok := a.activeTasks.Load(taskID)
	if !ok {
		log.Printf("Task %s not found (may have already completed)", taskID)
		// Send killed confirmation anyway
		a.send(AgentMessage{
			Type:   MsgKilled,
			ID:     taskID,
			Output: "Task not found or already completed",
		})
		return
	}

	cmd, ok := taskInfo.(*exec.Cmd)
	if !ok || cmd.Process == nil {
		log.Printf("Task %s has no running process", taskID)
		a.activeTasks.Delete(taskID)
		a.send(AgentMessage{
			Type:   MsgKilled,
			ID:     taskID,
			Output: "Task has no running process",
		})
		return
	}

	log.Printf("Killing task %s (PID: %d)", taskID, cmd.Process.Pid)

	// Kill the process group to also kill any child processes
	if err := cmd.Process.Kill(); err != nil {
		log.Printf("Failed to kill task %s: %v", taskID, err)
		a.send(AgentMessage{
			Type:   MsgKilled,
			ID:     taskID,
			Error:  fmt.Sprintf("Failed to kill: %v", err),
			Output: "Kill signal failed",
		})
		return
	}

	// Remove from active tasks
	a.activeTasks.Delete(taskID)

	// Send confirmation
	a.send(AgentMessage{
		Type:   MsgKilled,
		ID:     taskID,
		Output: "Task killed successfully",
	})

	log.Printf("Task %s killed successfully", taskID)
}

func (a *Agent) send(msg AgentMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.conn == nil {
		return fmt.Errorf("not connected")
	}
	return a.conn.WriteJSON(msg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
