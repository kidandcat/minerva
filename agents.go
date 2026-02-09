package main

import (
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
	AgentMsgListProjects = "list_projects"
	AgentMsgProjects     = "projects"
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
}

// Agent represents a connected agent
type Agent struct {
	Name      string
	Cwd       string
	Projects  []string
	conn      *websocket.Conn
	hub       *AgentHub
	send      chan AgentMessage
	connected time.Time
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

// PendingAck represents a request waiting for a task ack
type PendingAck struct {
	Result chan AgentMessage
}

// AgentHub manages agent connections
type AgentHub struct {
	agents      map[string]*Agent
	projectReqs map[string]*PendingProjectReq
	pendingAcks map[string]*PendingAck
	password    string
	notify      NotifyFunc
	onResult    ResultFunc
	mu          sync.RWMutex
	upgrader    websocket.Upgrader
}

// NewAgentHub creates a new agent hub
func NewAgentHub(password string, notify NotifyFunc, onResult ResultFunc) *AgentHub {
	return &AgentHub{
		agents:      make(map[string]*Agent),
		projectReqs: make(map[string]*PendingProjectReq),
		pendingAcks: make(map[string]*PendingAck),
		password:    password,
		notify:      notify,
		onResult:    onResult,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
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
		send:      make(chan AgentMessage, 10),
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
		list = append(list, map[string]any{
			"name":      name,
			"cwd":       agent.Cwd,
			"projects":  agent.Projects,
			"connected": agent.connected.Format(time.RFC3339),
		})
	}
	return list
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
func (h *AgentHub) SendTask(agentName, prompt, dir string) (string, error) {
	h.mu.RLock()
	agent, ok := h.agents[agentName]
	h.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("agent '%s' not found", agentName)
	}

	taskID := fmt.Sprintf("%d", time.Now().UnixNano())

	// Register pending ack before sending
	ackChan := make(chan AgentMessage, 1)
	h.mu.Lock()
	h.pendingAcks[taskID] = &PendingAck{Result: ackChan}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pendingAcks, taskID)
		h.mu.Unlock()
	}()

	// Send task to agent
	msg := AgentMessage{
		Type:   AgentMsgTask,
		ID:     taskID,
		Prompt: prompt,
		Dir:    dir,
	}
	select {
	case agent.send <- msg:
		log.Printf("[Agent] Task %s sent to '%s' (dir: %s), waiting for ACK...", taskID, agentName, dir)
	default:
		return "", fmt.Errorf("agent '%s' send channel full or closed", agentName)
	}

	// Wait for ACK (agent confirms Claude started or reports error)
	select {
	case ack := <-ackChan:
		if ack.Error != "" {
			return "", fmt.Errorf("agent '%s' failed to start task: %s", agentName, ack.Error)
		}
		log.Printf("[Agent] Task %s ACK received from '%s' - Claude is running", taskID, agentName)
		return taskID, nil
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("timeout waiting for agent '%s' to start task (30s)", agentName)
	}
}

func (h *AgentHub) registerAgent(agent *Agent, password string) bool {
	// Verify password
	if h.password != "" && password != h.password {
		log.Printf("Agent '%s' rejected: invalid password", agent.Name)
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Close existing agent with same name
	if existing, ok := h.agents[agent.Name]; ok {
		close(existing.send)
	}

	h.agents[agent.Name] = agent
	log.Printf("Agent '%s' connected from %s", agent.Name, agent.Cwd)

	if h.notify != nil {
		h.notify(fmt.Sprintf("🟢 Agent '%s' connected", agent.Name))
	}
	return true
}

func (h *AgentHub) unregisterAgent(agent *Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, ok := h.agents[agent.Name]; ok && existing == agent {
		delete(h.agents, agent.Name)
		log.Printf("Agent '%s' disconnected", agent.Name)

		if h.notify != nil {
			h.notify(fmt.Sprintf("🔴 Agent '%s' disconnected", agent.Name))
		}
	}
}

func (h *AgentHub) handleResult(agentName string, msg AgentMessage) {
	log.Printf("[AgentHub] Result from '%s': task=%s, exit=%d, output=%d bytes, error=%q, duration=%dms",
		agentName, msg.ID, msg.ExitCode, len(msg.Output), msg.Error, msg.Duration)

	if h.onResult == nil {
		log.Printf("[AgentHub] WARNING: onResult callback is nil, dropping result")
		return
	}

	var text string
	if msg.Error != "" {
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

func (a *Agent) readPump() {
	defer func() {
		a.hub.unregisterAgent(a)
		a.conn.Close()
	}()

	a.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	a.conn.SetPongHandler(func(string) error {
		a.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		var msg AgentMessage
		if err := a.conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Agent read error: %v", err)
			}
			return
		}

		// Reset read deadline on any message
		a.conn.SetReadDeadline(time.Now().Add(60 * time.Second))

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

		case AgentMsgProjects:
			a.hub.handleProjectsResult(msg)

		case AgentMsgPing:
			// Respond with pong
			a.send <- AgentMessage{Type: AgentMsgPong}

		case AgentMsgPong:
			// Keep alive
		}
	}
}

func (a *Agent) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
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
