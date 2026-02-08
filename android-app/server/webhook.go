package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// WebhookServer handles incoming HTTP requests
type WebhookServer struct {
	bot      *Bot
	port     int
	agentHub *AgentHub
}

// NewWebhookServer creates a new webhook server
func NewWebhookServer(bot *Bot, port int, agentHub *AgentHub) *WebhookServer {
	return &WebhookServer{
		bot:      bot,
		port:     port,
		agentHub: agentHub,
	}
}

// Start starts the HTTP server on the configured port
func (w *WebhookServer) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", w.handleHealth)

	if w.agentHub != nil {
		mux.HandleFunc("/agent", w.agentHub.HandleWebSocket)
		mux.HandleFunc("/agent/list", w.handleAgentList)
	}

	// Phone bridge placeholder - will be implemented later
	mux.HandleFunc("/phone", w.handlePhonePlaceholder)

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

func (w *WebhookServer) handlePhonePlaceholder(rw http.ResponseWriter, r *http.Request) {
	// Placeholder: phone bridge WebSocket will be implemented later
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusNotImplemented)
	json.NewEncoder(rw).Encode(map[string]string{"error": "phone bridge not yet implemented"})
}

func (w *WebhookServer) handleNotFound(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusNotFound)
	json.NewEncoder(rw).Encode(map[string]string{"error": "not found"})
}
