package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// WebhookServer handles incoming HTTP requests
type WebhookServer struct {
	bot         *Bot
	port        int
	agentHub    *AgentHub
	phoneBridge *PhoneBridge
}

// NewWebhookServer creates a new webhook server
func NewWebhookServer(bot *Bot, port int, agentHub *AgentHub, phoneBridge *PhoneBridge) *WebhookServer {
	return &WebhookServer{
		bot:         bot,
		port:        port,
		agentHub:    agentHub,
		phoneBridge: phoneBridge,
	}
}

// Start starts the HTTP server on the configured port
func (w *WebhookServer) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", w.handleHealth)

	if w.agentHub != nil {
		mux.HandleFunc("/agent", w.agentHub.HandleWebSocket)
		mux.HandleFunc("/agent/list", w.handleAgentList)
		mux.HandleFunc("/agent/run", w.handleAgentRun)
		log.Println("Agent endpoints: /agent, /agent/list, /agent/run")
	}

	if w.phoneBridge != nil {
		mux.HandleFunc("/phone", w.phoneBridge.HandleWebSocket)
		mux.HandleFunc("/phone/call", w.handlePhoneCall)
		log.Println("Phone Bridge endpoints: /phone, /phone/call")
	}

	// Catch-all 404
	mux.HandleFunc("/", w.handleNotFound)

	addr := fmt.Sprintf(":%d", w.port)
	log.Printf("Webhook server starting on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (w *WebhookServer) handleHealth(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]string{"status": "ok"})
}

func (w *WebhookServer) handleAgentList(rw http.ResponseWriter, r *http.Request) {
	if w.agentHub == nil {
		http.Error(rw, `{"error":"agent hub not available"}`, http.StatusServiceUnavailable)
		return
	}

	agents := w.agentHub.ListAgents()
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(agents)
}

// handlePhoneCall initiates an outbound call via Android phone bridge
func (w *WebhookServer) handlePhoneCall(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		To      string `json:"to"`
		Purpose string `json:"purpose"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.To == "" {
		http.Error(rw, "Missing 'to' field", http.StatusBadRequest)
		return
	}

	if err := w.phoneBridge.MakeCall(req.To, req.Purpose); err != nil {
		log.Printf("[Phone] Failed to make call: %v", err)
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(map[string]any{"error": err.Error()})
		return
	}

	w.bot.sendMessage(w.bot.config.AdminID, fmt.Sprintf("Llamada saliente a %s iniciada (Android)", req.To))

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{
		"status": "calling",
		"to":     req.To,
	})
}

// handleAgentRun executes a task on an agent asynchronously
func (w *WebhookServer) handleAgentRun(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	if w.agentHub == nil {
		http.Error(rw, `{"error":"agent hub not available"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
		Dir    string `json:"dir,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Agent == "" || req.Prompt == "" {
		http.Error(rw, `{"error":"agent and prompt are required"}`, http.StatusBadRequest)
		return
	}

	taskID := fmt.Sprintf("%d", time.Now().UnixNano())

	go func(taskID, agent, prompt, dir string) {
		result, err := w.agentHub.SendTask(agent, prompt, dir, 60*time.Minute)
		if err != nil {
			log.Printf("[Agent] Task %s on '%s' failed: %v", taskID, agent, err)
			w.bot.ProcessSystemEvent(w.bot.config.AdminID,
				fmt.Sprintf("Agent task on '%s' failed: %v\nOriginal prompt: %s", agent, err, prompt))
			return
		}
		if result == nil {
			w.bot.ProcessSystemEvent(w.bot.config.AdminID,
				fmt.Sprintf("Agent '%s' returned no result.\nPrompt: %s", agent, prompt))
			return
		}
		output := result.Output
		if result.Error != "" {
			output = fmt.Sprintf("Error: %s\n\nOutput:\n%s", result.Error, result.Output)
		}
		log.Printf("[Agent] Task %s on '%s' completed (%d chars)", taskID, agent, len(output))
		if len(output) > 4000 {
			output = output[:4000] + "\n... (truncated)"
		}
		w.bot.ProcessSystemEvent(w.bot.config.AdminID,
			fmt.Sprintf("Agent '%s' completed task.\nPrompt: %s\n\nResult:\n%s", agent, prompt, output))
	}(taskID, req.Agent, req.Prompt, req.Dir)

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{
		"status":  "started",
		"task_id": taskID,
		"message": fmt.Sprintf("Task started on agent '%s'", req.Agent),
	})
}

func (w *WebhookServer) handleNotFound(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusNotFound)
	json.NewEncoder(rw).Encode(map[string]string{"error": "not found"})
}
