// Package mobile provides the gomobile bindings for Minerva
package mobile

import (
	"encoding/json"
	"fmt"
	"sync"
)

// MobileConfig holds configuration for mobile deployment
type MobileConfig struct {
	TelegramToken    string `json:"telegram_token"`
	AdminID          int64  `json:"admin_id"`
	OpenRouterKey    string `json:"openrouter_key"`
	DatabasePath     string `json:"database_path"`
	Models           string `json:"models"` // comma-separated
	ResendAPIKey     string `json:"resend_api_key,omitempty"`
	TelnyxAPIKey     string `json:"telnyx_api_key,omitempty"`
	TelnyxAppID      string `json:"telnyx_app_id,omitempty"`
	TelnyxPhone      string `json:"telnyx_phone,omitempty"`
	AgentPassword    string `json:"agent_password,omitempty"`
	GoogleAPIKey     string `json:"google_api_key,omitempty"`
	WebhookPort      int    `json:"webhook_port,omitempty"`
}

var (
	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
)

// Start initializes and starts Minerva with the given JSON configuration.
// Returns an error string if failed, empty string on success.
func Start(configJSON string) string {
	mu.Lock()
	defer mu.Unlock()

	if running {
		return "already running"
	}

	var cfg MobileConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Sprintf("invalid config: %v", err)
	}

	// Validate required fields
	if cfg.TelegramToken == "" {
		return "telegram_token is required"
	}
	if cfg.OpenRouterKey == "" {
		return "openrouter_key is required"
	}
	if cfg.AdminID == 0 {
		return "admin_id is required"
	}

	// Set defaults
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = "./minerva.db"
	}
	if cfg.WebhookPort == 0 {
		cfg.WebhookPort = 8081
	}

	// Start the server in a goroutine
	stopCh = make(chan struct{})
	go func() {
		if err := startServer(cfg); err != nil {
			// Log error - in mobile context this could be sent to a callback
			fmt.Printf("Minerva error: %v\n", err)
		}
	}()

	running = true
	return ""
}

// Stop stops the running Minerva instance
func Stop() {
	mu.Lock()
	defer mu.Unlock()

	if !running {
		return
	}

	if stopCh != nil {
		close(stopCh)
	}

	stopServer()
	running = false
}

// IsRunning returns true if Minerva is currently running
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return running
}

// SendTestMessage sends a test message to verify the bot is working.
// Returns an error string if failed, empty string on success.
func SendTestMessage(text string) string {
	mu.Lock()
	defer mu.Unlock()

	if !running {
		return "not running"
	}

	if err := sendTestMessage(text); err != nil {
		return err.Error()
	}
	return ""
}

// GetStatus returns a JSON string with the current status
func GetStatus() string {
	mu.Lock()
	defer mu.Unlock()

	status := map[string]interface{}{
		"running": running,
	}

	data, _ := json.Marshal(status)
	return string(data)
}
