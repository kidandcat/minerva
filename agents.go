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

// PendingTask represents a task waiting for a result
type PendingTask struct {
	ID       string
	Agent    string
	Result   chan AgentMessage
	Created  time.Time
}

// NotifyFunc is a callback for agent events
type NotifyFunc func(message string)

// AgentHub manages agent connections
type AgentHub struct {
	agents   map[string]*Agent
	tasks    map[string]*PendingTask
	password string
	notify   NotifyFunc
	mu       sync.RWMutex
	upgrader websocket.Upgrader
}

// NewAgentHub creates a new agent hub
func NewAgentHub(password string, notify NotifyFunc) *AgentHub {
	return &AgentHub{
		agents:   make(map[string]*Agent),
		tasks:    make(map[string]*PendingTask),
		password: password,
		notify:   notify,
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
	h.tasks[reqID] = &PendingTask{
		ID:      reqID,
		Agent:   agentName,
		Result:  resultChan,
		Created: time.Now(),
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.tasks, reqID)
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

// SendTask sends a task to an agent and waits for the result
func (h *AgentHub) SendTask(agentName, prompt, dir string, timeout time.Duration) (*AgentMessage, error) {
	h.mu.RLock()
	agent, ok := h.agents[agentName]
	h.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("agent '%s' not found", agentName)
	}

	// Create task
	taskID := fmt.Sprintf("%d", time.Now().UnixNano())
	resultChan := make(chan AgentMessage, 1)

	h.mu.Lock()
	h.tasks[taskID] = &PendingTask{
		ID:      taskID,
		Agent:   agentName,
		Result:  resultChan,
		Created: time.Now(),
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.tasks, taskID)
		h.mu.Unlock()
	}()

	// Send task to agent
	agent.send <- AgentMessage{
		Type:   AgentMsgTask,
		ID:     taskID,
		Prompt: prompt,
		Dir:    dir,
	}

	// Wait for result
	select {
	case result := <-resultChan:
		return &result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("task timeout after %v", timeout)
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

func (h *AgentHub) handleResult(msg AgentMessage) {
	h.mu.RLock()
	task, ok := h.tasks[msg.ID]
	h.mu.RUnlock()

	if ok {
		select {
		case task.Result <- msg:
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

		case AgentMsgResult, AgentMsgProjects:
			a.hub.handleResult(msg)

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

		result, err := hub.SendTask(args.Agent, args.Prompt, args.Dir, 60*time.Minute)
		if err != nil {
			return "", err
		}

		if result.Error != "" {
			return fmt.Sprintf("Error: %s\n\nOutput:\n%s", result.Error, result.Output), nil
		}

		return result.Output, nil

	default:
		return "", fmt.Errorf("unknown agent tool: %s", name)
	}
}
