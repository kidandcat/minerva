package main

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Agent message types
const (
	AgentMsgRegister     = "register"
	AgentMsgTask         = "task"
	AgentMsgAck          = "ack"
	AgentMsgResult       = "result"
	AgentMsgPing         = "ping"
	AgentMsgPong         = "pong"
	AgentMsgHeartbeat    = "heartbeat"
	AgentMsgListProjects = "list_projects"
	AgentMsgProjects     = "projects"
	AgentMsgKill         = "kill"
	AgentMsgKilled       = "killed"
	AgentMsgFileUpload   = "file_upload"
)

const (
	// TaskStaleThreshold is how long a task can go without a heartbeat before being considered stale
	TaskStaleThreshold = 10 * time.Minute
	// TaskWatchdogInterval is how often we check for stale tasks
	TaskWatchdogInterval = 2 * time.Minute
)

// AgentMessage represents a message to/from an agent
type AgentMessage struct {
	Type string `json:"type"`

	// Register
	Name     string   `json:"name,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Password string   `json:"password,omitempty"`
	Projects []string `json:"projects,omitempty"` // Folders in home directory

	// Task
	ID     string `json:"id,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Dir    string `json:"dir,omitempty"`

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

// Agent represents a connected agent
type Agent struct {
	Name        string
	Cwd         string
	Projects    []string
	conn        *websocket.Conn
	hub         *AgentHub
	send        chan AgentMessage
	connected   time.Time
	activeTasks sync.Map // taskID -> *ActiveTask
}

// ActiveTask holds information about a running task
type ActiveTask struct {
	StartTime     time.Time
	LastHeartbeat time.Time
	Prompt        string // first 100 chars for identification
	Killed        bool
	MessageID     int   // Telegram message ID for the confirmation message with Kill button
	ChatID        int64
}

// PendingProjectReq represents a request waiting for a project list response
type PendingProjectReq struct {
	ID     string
	Agent  string
	Result chan AgentMessage
}

// NotifyFunc is a callback for agent events (connect/disconnect)
type NotifyFunc func(message string)

// ResultFunc is a callback for agent task results, processed through the AI brain
type ResultFunc func(message string)

// TaskStartFunc is a callback when an agent task starts (for sending Kill button)
type TaskStartFunc func(taskID, agentName, prompt string)

// FileUploadFunc is a callback for receiving files from agents
type FileUploadFunc func(agentName, fileName string, data []byte)

// PendingAck represents a request waiting for a task ack
type PendingAck struct {
	Result chan AgentMessage
}

// AgentHub manages agent connections
type AgentHub struct {
	agents         map[string]*Agent
	projectReqs    map[string]*PendingProjectReq
	pendingAcks    map[string]*PendingAck
	disconnTimers  map[string]*time.Timer // debounce disconnect notifications
	taskAgentMap   map[string]string      // taskID -> agentName
	password       string
	notify         NotifyFunc
	onResult       ResultFunc
	onTaskStart    TaskStartFunc
	onFileUpload   FileUploadFunc
	mu             sync.RWMutex
	upgrader       websocket.Upgrader
	stopWatchdog   chan struct{}
}

// NewAgentHub creates a new agent hub
func NewAgentHub(password string, notify NotifyFunc, onResult ResultFunc) *AgentHub {
	h := &AgentHub{
		agents:        make(map[string]*Agent),
		projectReqs:   make(map[string]*PendingProjectReq),
		pendingAcks:   make(map[string]*PendingAck),
		disconnTimers: make(map[string]*time.Timer),
		taskAgentMap:  make(map[string]string),
		password:      password,
		notify:        notify,
		onResult:      onResult,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		stopWatchdog: make(chan struct{}),
	}
	go h.taskWatchdog()
	return h
}

// taskWatchdog periodically checks for stale tasks that haven't sent a heartbeat
func (h *AgentHub) taskWatchdog() {
	ticker := time.NewTicker(TaskWatchdogInterval)
	defer ticker.Stop()

	// Track which tasks we've already alerted about to avoid spam
	alerted := make(map[string]bool)

	for {
		select {
		case <-h.stopWatchdog:
			return
		case <-ticker.C:
			h.mu.RLock()
			for agentName, agent := range h.agents {
				agent.activeTasks.Range(func(key, value any) bool {
					taskID := key.(string)
					info := value.(*ActiveTask)
					alertKey := agentName + ":" + taskID

					sinceHeartbeat := time.Since(info.LastHeartbeat)
					sinceStart := time.Since(info.StartTime)

					if sinceHeartbeat > TaskStaleThreshold && !alerted[alertKey] {
						alerted[alertKey] = true
						log.Printf("[Watchdog] STALE task %s on agent '%s': no heartbeat for %v (started %v ago, prompt: %s)",
							taskID, agentName, sinceHeartbeat.Round(time.Second), sinceStart.Round(time.Second), info.Prompt)
						if h.notify != nil {
							h.notify(fmt.Sprintf("⚠️ Task stuck on agent '%s': no heartbeat for %v\nStarted: %v ago\nTask: %s",
								agentName, sinceHeartbeat.Round(time.Minute), sinceStart.Round(time.Minute), info.Prompt))
						}
					}
					return true
				})
			}
			h.mu.RUnlock()

			// Clean up alerts for tasks that no longer exist
			for alertKey := range alerted {
				// Parse agentName from alertKey
				found := false
				h.mu.RLock()
				for agentName, agent := range h.agents {
					agent.activeTasks.Range(func(key, _ any) bool {
						if agentName+":"+key.(string) == alertKey {
							found = true
							return false
						}
						return true
					})
					if found {
						break
					}
				}
				h.mu.RUnlock()
				if !found {
					delete(alerted, alertKey)
				}
			}
		}
	}
}

// SetTaskStartCallback sets the callback for when an agent task starts
func (h *AgentHub) SetTaskStartCallback(fn TaskStartFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onTaskStart = fn
}

// SetFileUploadCallback sets the callback for when an agent uploads a file
func (h *AgentHub) SetFileUploadCallback(fn FileUploadFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onFileUpload = fn
}

// HandleWebSocket handles agent WebSocket connections
func (h *AgentHub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Agent WebSocket upgrade error: %v", err)
		return
	}

	agent := &Agent{
		conn:      conn,
		hub:       h,
		send:      make(chan AgentMessage, 64),
		connected: time.Now(),
	}

	go agent.readPump()
	go agent.writePump()
}

// ListAgents returns a list of connected agents
func (h *AgentHub) ListAgents() []map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()

	list := make([]map[string]any, 0, len(h.agents))
	for name, agent := range h.agents {
		taskCount := 0
		agent.activeTasks.Range(func(_, _ any) bool {
			taskCount++
			return true
		})
		list = append(list, map[string]any{
			"name":         name,
			"cwd":          agent.Cwd,
			"projects":     agent.Projects,
			"connected":    agent.connected.Format(time.RFC3339),
			"active_tasks": taskCount,
		})
	}
	return list
}

// GetAgentCwd returns the working directory of a connected agent
func (h *AgentHub) GetAgentCwd(agentName string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if agent, ok := h.agents[agentName]; ok {
		return agent.Cwd
	}
	return ""
}

// GetProjects requests the current project list from an agent
func (h *AgentHub) GetProjects(agentName string, timeout time.Duration) ([]string, error) {
	h.mu.RLock()
	agent, ok := h.agents[agentName]
	h.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("agent '%s' not found", agentName)
	}

	// Create request
	reqID := fmt.Sprintf("proj_%d", time.Now().UnixNano())
	resultChan := make(chan AgentMessage, 1)

	h.mu.Lock()
	h.projectReqs[reqID] = &PendingProjectReq{
		ID:     reqID,
		Agent:  agentName,
		Result: resultChan,
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.projectReqs, reqID)
		h.mu.Unlock()
	}()

	// Request projects
	agent.send <- AgentMessage{
		Type: AgentMsgListProjects,
		ID:   reqID,
	}

	// Wait for result
	select {
	case result := <-resultChan:
		return result.Projects, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for projects from '%s'", agentName)
	}
}

// SendTask sends a task to an agent and waits for acknowledgment that Claude started.
// Returns the task ID once the agent confirms the process launched.
// Results will arrive later via handleResult and be sent as Telegram messages.
// Multiple tasks can run concurrently on the same agent.
func (h *AgentHub) SendTask(agentName, prompt, dir string) (string, error) {
	h.mu.RLock()
	agent, ok := h.agents[agentName]
	h.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("agent '%s' not found", agentName)
	}

	taskID := fmt.Sprintf("%d", time.Now().UnixNano())
	now := time.Now()

	// Track active task on this agent (will update with messageID after callback)
	agent.activeTasks.Store(taskID, &ActiveTask{
		StartTime:     now,
		LastHeartbeat: now,
		Prompt:        truncateText(prompt, 100),
		MessageID:     0, // Will be set by callback after Telegram message sent
		ChatID:        0, // Will be set by callback after Telegram message sent
	})

	// Store task -> agent mapping for kill functionality
	h.mu.Lock()
	h.taskAgentMap[taskID] = agentName
	h.mu.Unlock()

	// Register pending ack before sending
	ackChan := make(chan AgentMessage, 1)
	h.mu.Lock()
	h.pendingAcks[taskID] = &PendingAck{Result: ackChan}
	h.mu.Unlock()

	cleanup := func() {
		h.mu.Lock()
		delete(h.pendingAcks, taskID)
		h.mu.Unlock()
	}

	// Send task to agent (safe send to avoid panic on closed channel during reconnection)
	msg := AgentMessage{
		Type:   AgentMsgTask,
		ID:     taskID,
		Prompt: prompt,
		Dir:    dir,
	}
	if !safeSendAgent(agent.send, msg) {
		cleanup()
		agent.activeTasks.Delete(taskID)
		h.mu.Lock()
		delete(h.taskAgentMap, taskID)
		h.mu.Unlock()
		return "", fmt.Errorf("agent '%s' send channel full or closed", agentName)
	}
	log.Printf("[Agent] Task %s sent to '%s' (dir: %s), waiting for ACK...", taskID, agentName, dir)

	// Wait for ACK (agent confirms Claude started or reports error)
	select {
	case ack := <-ackChan:
		cleanup()
		if ack.Error != "" {
			agent.activeTasks.Delete(taskID)
			h.mu.Lock()
			delete(h.taskAgentMap, taskID)
			h.mu.Unlock()
			return "", fmt.Errorf("agent '%s' failed to start task: %s", agentName, ack.Error)
		}
		log.Printf("[Agent] Task %s ACK received from '%s' - Claude is running", taskID, agentName)

		// Notify about task start (for Kill button)
		h.mu.RLock()
		onTaskStart := h.onTaskStart
		h.mu.RUnlock()
		if onTaskStart != nil {
			onTaskStart(taskID, agentName, prompt)
		}

		return taskID, nil
	case <-time.After(30 * time.Second):
		cleanup()
		agent.activeTasks.Delete(taskID)
		h.mu.Lock()
		delete(h.taskAgentMap, taskID)
		h.mu.Unlock()
		return "", fmt.Errorf("timeout waiting for agent '%s' to start task (30s)", agentName)
	}
}

// UpdateTaskMessage updates the Telegram message ID for a task (for Kill button updates)
func (h *AgentHub) UpdateTaskMessage(taskID string, messageID int, chatID int64) error {
	h.mu.RLock()
	agentName, ok := h.taskAgentMap[taskID]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("task '%s' not found", taskID)
	}

	h.mu.RLock()
	agent, ok := h.agents[agentName]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("agent '%s' not found", agentName)
	}

	// Update the task info
	if taskInfo, ok := agent.activeTasks.Load(taskID); ok {
		if activeTask, ok := taskInfo.(*ActiveTask); ok {
			activeTask.MessageID = messageID
			activeTask.ChatID = chatID
		}
	}

	return nil
}

// KillTask sends a kill signal to the agent running the given task
func (h *AgentHub) KillTask(taskID string) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, agent := range h.agents {
		if val, ok := agent.activeTasks.Load(taskID); ok {
			info := val.(*ActiveTask)
			info.Killed = true
			msg := AgentMessage{Type: AgentMsgKill, ID: taskID}
			if !safeSendAgent(agent.send, msg) {
				return fmt.Errorf("failed to send kill signal (channel full or closed)")
			}
			log.Printf("[AgentHub] Kill signal sent for task %s", taskID)
			return nil
		}
	}
	return fmt.Errorf("task %s not found on any agent", taskID)
}

// GetTaskMessageID returns the Telegram message ID for a task (used to update the Kill button)
func (h *AgentHub) GetTaskMessageID(taskID string) (int, int64, error) {
	h.mu.RLock()
	agentName, ok := h.taskAgentMap[taskID]
	h.mu.RUnlock()

	if !ok {
		return 0, 0, fmt.Errorf("task not found")
	}

	h.mu.RLock()
	agent, ok := h.agents[agentName]
	h.mu.RUnlock()

	if !ok {
		return 0, 0, fmt.Errorf("agent not found")
	}

	if taskInfo, ok := agent.activeTasks.Load(taskID); ok {
		if activeTask, ok := taskInfo.(*ActiveTask); ok {
			return activeTask.MessageID, activeTask.ChatID, nil
		}
	}

	return 0, 0, fmt.Errorf("task info not found")
}

// safeSendAgent sends a message to an agent channel, recovering from panic if the channel was closed.
func safeSendAgent(ch chan AgentMessage, msg AgentMessage) (sent bool) {
	defer func() {
		if r := recover(); r != nil {
			sent = false
		}
	}()
	select {
	case ch <- msg:
		return true
	default:
		return false
	}
}

func (h *AgentHub) registerAgent(agent *Agent, password string) bool {
	// Verify password using constant-time comparison to prevent timing attacks
	if h.password != "" && subtle.ConstantTimeCompare([]byte(password), []byte(h.password)) != 1 {
		log.Printf("Agent '%s' rejected: invalid password", agent.Name)
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Close existing agent with same name
	replaced := false
	if existing, ok := h.agents[agent.Name]; ok {
		close(existing.send)
		replaced = true
	}

	h.agents[agent.Name] = agent
	log.Printf("Agent '%s' connected from %s", agent.Name, agent.Cwd)

	// Cancel pending disconnect notification (agent reconnected quickly)
	if timer, ok := h.disconnTimers[agent.Name]; ok {
		timer.Stop()
		delete(h.disconnTimers, agent.Name)
		// Silent reconnect - no notifications
	} else if replaced {
		// Replaced existing connection - silent (avoids spam during reconnect loops)
	} else if h.notify != nil {
		h.notify(fmt.Sprintf("🟢 Agent '%s' connected", agent.Name))
	}
	return true
}

func (h *AgentHub) unregisterAgent(agent *Agent) {
	h.mu.Lock()

	if existing, ok := h.agents[agent.Name]; ok && existing == agent {
		delete(h.agents, agent.Name)
		log.Printf("Agent '%s' disconnected", agent.Name)

		// Check for orphaned tasks
		var orphanedTasks []string
		agent.activeTasks.Range(func(key, value any) bool {
			taskID := key.(string)
			info := value.(*ActiveTask)
			orphanedTasks = append(orphanedTasks, fmt.Sprintf("  - %s (running %v): %s",
				taskID, time.Since(info.StartTime).Round(time.Second), info.Prompt))
			return true
		})

		// Debounce: wait 10s before notifying, in case agent reconnects quickly
		name := agent.Name
		if h.notify != nil {
			h.disconnTimers[name] = time.AfterFunc(10*time.Second, func() {
				h.mu.Lock()
				delete(h.disconnTimers, name)
				// Only notify if agent is still disconnected
				_, reconnected := h.agents[name]
				h.mu.Unlock()
				if !reconnected {
					msg := fmt.Sprintf("🔴 Agent '%s' disconnected", name)
					if len(orphanedTasks) > 0 {
						msg += fmt.Sprintf("\n⚠️ %d task(s) were running and may be lost:\n", len(orphanedTasks))
						for _, t := range orphanedTasks {
							msg += t + "\n"
						}
					}
					h.notify(msg)
				}
			})
		}
	}

	h.mu.Unlock()
}

func (h *AgentHub) handleHeartbeat(agentName string, msg AgentMessage) {
	h.mu.RLock()
	agent, ok := h.agents[agentName]
	h.mu.RUnlock()

	if !ok {
		return
	}

	if val, ok := agent.activeTasks.Load(msg.ID); ok {
		info := val.(*ActiveTask)
		info.LastHeartbeat = time.Now()
		log.Printf("[AgentHub] Heartbeat from '%s' for task %s (running %v)",
			agentName, msg.ID, time.Since(info.StartTime).Round(time.Second))
	}
}

func (h *AgentHub) handleResult(agentName string, msg AgentMessage) {
	// Calculate server-side tracking duration
	var trackingInfo string
	var killed bool

	// Remove from active tasks and task map
	h.mu.Lock()
	delete(h.taskAgentMap, msg.ID)
	h.mu.Unlock()

	h.mu.RLock()
	if agent, ok := h.agents[agentName]; ok {
		if val, ok := agent.activeTasks.Load(msg.ID); ok {
			info := val.(*ActiveTask)
			trackingInfo = fmt.Sprintf(", tracked_duration=%v", time.Since(info.StartTime).Round(time.Second))
			killed = info.Killed
		}
		agent.activeTasks.Delete(msg.ID)
	}
	h.mu.RUnlock()

	log.Printf("[AgentHub] Result from '%s': task=%s, exit=%d, output=%d bytes, error=%q, duration=%dms, killed=%v%s",
		agentName, msg.ID, msg.ExitCode, len(msg.Output), msg.Error, msg.Duration, killed, trackingInfo)

	if h.onResult == nil {
		log.Printf("[AgentHub] WARNING: onResult callback is nil, dropping result")
		return
	}

	var text string
	if killed {
		text = fmt.Sprintf("[AGENT %s] 🛑 Task killed by user", agentName)
		if msg.Output != "" {
			text += fmt.Sprintf("\n\nPartial output:\n%s", truncateText(msg.Output, 3500))
		}
	} else if msg.Error != "" {
		text = fmt.Sprintf("[AGENT %s] Error: %s", agentName, msg.Error)
		if msg.Output != "" {
			text += fmt.Sprintf("\n\nOutput:\n%s", truncateText(msg.Output, 3500))
		}
	} else if msg.Output != "" {
		text = fmt.Sprintf("[AGENT %s]\n%s", agentName, truncateText(msg.Output, 3800))
	} else {
		text = fmt.Sprintf("[AGENT %s] Task completed (no output)", agentName)
	}

	log.Printf("[AgentHub] Forwarding result to onResult callback (%d bytes)", len(text))
	h.onResult(text)
}

func (h *AgentHub) handleAck(msg AgentMessage) {
	h.mu.RLock()
	pending, ok := h.pendingAcks[msg.ID]
	h.mu.RUnlock()

	if ok {
		select {
		case pending.Result <- msg:
		default:
		}
	}
}

func (h *AgentHub) handleProjectsResult(msg AgentMessage) {
	h.mu.RLock()
	req, ok := h.projectReqs[msg.ID]
	h.mu.RUnlock()

	if ok {
		select {
		case req.Result <- msg:
		default:
		}
	}
}

// handleKilled processes the killed confirmation from an agent
func (h *AgentHub) handleKilled(agentName string, msg AgentMessage) {
	log.Printf("[AgentHub] Task %s killed confirmation from agent '%s'", msg.ID, agentName)

	// Notify via onResult callback so the brain knows it was killed
	if h.onResult != nil {
		text := fmt.Sprintf("[AGENT %s] Task killed by user", agentName)
		h.onResult(text)
	}
}

// handleFileUpload processes a file upload from an agent
func (h *AgentHub) handleFileUpload(agentName string, msg AgentMessage) {
	log.Printf("[AgentHub] File upload from '%s': %s (%d bytes)", agentName, msg.FileName, msg.FileSize)

	h.mu.RLock()
	onFileUpload := h.onFileUpload
	h.mu.RUnlock()

	if onFileUpload == nil {
		log.Printf("[AgentHub] WARNING: onFileUpload callback is nil, dropping file %s", msg.FileName)
		return
	}

	// Decode base64 data
	data, err := base64.StdEncoding.DecodeString(msg.FileData)
	if err != nil {
		log.Printf("[AgentHub] Failed to decode file %s from agent '%s': %v", msg.FileName, agentName, err)
		return
	}

	onFileUpload(agentName, msg.FileName, data)
}

func (a *Agent) readPump() {
	defer func() {
		a.hub.unregisterAgent(a)
		a.conn.Close()
	}()

	a.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	a.conn.SetPongHandler(func(string) error {
		a.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	// IMPORTANT: Do NOT write to the connection from PingHandler.
	// PingHandler runs in the readPump goroutine, and ANY write (even WriteControl)
	// can race with writePump's writes, causing "concurrent write" panics.
	// The agent's read deadline is reset by application-level pongs from writePump,
	// so missing WebSocket-level pongs is fine.
	a.conn.SetPingHandler(func(string) error {
		a.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	for {
		var msg AgentMessage
		if err := a.conn.ReadJSON(&msg); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("Agent '%s' read error: %v", a.Name, err)
			}
			return
		}

		// Reset read deadline on any message
		a.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		switch msg.Type {
		case AgentMsgRegister:
			a.Name = msg.Name
			a.Cwd = msg.Cwd
			a.Projects = msg.Projects
			if !a.hub.registerAgent(a, msg.Password) {
				// Auth failed, close connection
				return
			}

		case AgentMsgAck:
			a.hub.handleAck(msg)

		case AgentMsgResult:
			a.hub.handleResult(a.Name, msg)

		case AgentMsgHeartbeat:
			a.hub.handleHeartbeat(a.Name, msg)

		case AgentMsgProjects:
			a.hub.handleProjectsResult(msg)

		case AgentMsgKilled:
			a.hub.handleKilled(a.Name, msg)

		case AgentMsgFileUpload:
			a.hub.handleFileUpload(a.Name, msg)

		case AgentMsgPing:
			// Respond with pong
			a.send <- AgentMessage{Type: AgentMsgPong}

		case AgentMsgPong:
			// Keep alive
		}
	}
}

func (a *Agent) writePump() {
	ticker := time.NewTicker(25 * time.Second)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Agent '%s' writePump panic (recovered): %v", a.Name, r)
		}
		ticker.Stop()
		a.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-a.send:
			a.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				a.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := a.conn.WriteJSON(msg); err != nil {
				return
			}

		case <-ticker.C:
			// Send application-level ping only (no WebSocket-level WriteMessage
			// to avoid concurrent write panics with PingHandler in readPump)
			a.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := a.conn.WriteJSON(AgentMessage{Type: AgentMsgPing}); err != nil {
				return
			}
		}
	}
}

// Tool definitions for agents

// GetAgentToolDefinitions returns the tool definitions for agent-related tools
func GetAgentToolDefinitions() []Tool {
	return []Tool{
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_agents",
				Description: "List all connected Claude Code agents",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "run_claude",
				Description: "Run a task on a remote Claude Code agent. The agent will execute the prompt using Claude Code and return the result.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent": map[string]interface{}{
							"type":        "string",
							"description": "Name of the agent to run the task on",
						},
						"prompt": map[string]interface{}{
							"type":        "string",
							"description": "The prompt/task for Claude Code to execute",
						},
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Optional working directory override",
						},
					},
					"required": []string{"agent", "prompt"},
				},
			},
		},
	}
}

// ExecuteAgentTool executes an agent-related tool
func ExecuteAgentTool(hub *AgentHub, name, arguments string) (string, error) {
	switch name {
	case "list_claude_projects":
		agents := hub.ListAgents()
		if len(agents) == 0 {
			return "No agents connected", nil
		}

		result := make(map[string][]string)
		for _, agent := range agents {
			agentName := agent["name"].(string)
			projects, err := hub.GetProjects(agentName, 10*time.Second)
			if err != nil {
				result[agentName] = []string{fmt.Sprintf("error: %v", err)}
			} else {
				result[agentName] = projects
			}
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data), nil

	case "run_claude":
		var args struct {
			Agent  string `json:"agent"`
			Prompt string `json:"prompt"`
			Dir    string `json:"dir"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}

		_, err := hub.SendTask(args.Agent, args.Prompt, args.Dir)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("OK. Claude is running on agent '%s'.", args.Agent), nil

	default:
		return "", fmt.Errorf("unknown agent tool: %s", name)
	}
}
