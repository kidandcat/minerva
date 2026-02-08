package mobile

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

// Internal server state
var (
	botAPI       *tgbotapi.BotAPI
	db           *sql.DB
	currentAdmin int64
	serverStopCh chan struct{}
)

// startServer initializes and runs the Minerva server
func startServer(cfg MobileConfig) error {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Minerva Mobile...")

	currentAdmin = cfg.AdminID
	serverStopCh = make(chan struct{})

	// Initialize database
	var err error
	db, err = sql.Open("sqlite", cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Create tables
	if err := createTables(db); err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	// Initialize Telegram bot
	botAPI, err = tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return fmt.Errorf("failed to create bot: %w", err)
	}
	log.Printf("Authorized as @%s", botAPI.Self.UserName)

	// Parse models
	var models []string
	if cfg.Models != "" {
		for _, m := range strings.Split(cfg.Models, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				models = append(models, m)
			}
		}
	}

	// Create AI client
	aiClient := newMobileAIClient(cfg.OpenRouterKey, models)

	// Create mobile bot handler
	handler := &mobileBotHandler{
		api:     botAPI,
		db:      db,
		ai:      aiClient,
		adminID: cfg.AdminID,
		running: true,
	}

	// Start webhook server if port is set
	if cfg.WebhookPort > 0 {
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("ok"))
			})
			addr := fmt.Sprintf(":%d", cfg.WebhookPort)
			log.Printf("Starting webhook server on %s", addr)
			if err := http.ListenAndServe(addr, mux); err != nil {
				log.Printf("Webhook server error: %v", err)
			}
		}()
	}

	// Start bot polling
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := botAPI.GetUpdatesChan(u)

	log.Println("Starting bot polling...")
	for {
		select {
		case <-serverStopCh:
			log.Println("Server stop signal received")
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if update.Message == nil {
				continue
			}
			go handler.handleMessage(update.Message)
		}
	}
}

// stopServer shuts down the server
func stopServer() {
	if serverStopCh != nil {
		select {
		case <-serverStopCh:
			// Already closed
		default:
			close(serverStopCh)
		}
	}
	if botAPI != nil {
		botAPI.StopReceivingUpdates()
	}
	if db != nil {
		db.Close()
	}
}

// sendTestMessage sends a message to the admin
func sendTestMessage(text string) error {
	if botAPI == nil {
		return fmt.Errorf("bot not initialized")
	}
	if currentAdmin == 0 {
		return fmt.Errorf("admin not configured")
	}

	msg := tgbotapi.NewMessage(currentAdmin, text)
	_, err := botAPI.Send(msg)
	return err
}

// mobileBotHandler handles Telegram messages
type mobileBotHandler struct {
	api     *tgbotapi.BotAPI
	db      *sql.DB
	ai      *mobileAIClient
	adminID int64
	running bool
}

func (h *mobileBotHandler) handleMessage(msg *tgbotapi.Message) {
	// Only allow admin
	if msg.From.ID != h.adminID {
		return
	}

	// Handle commands
	if msg.IsCommand() {
		switch msg.Command() {
		case "start":
			h.sendMessage(msg.Chat.ID, "Minerva Mobile is running!")
			return
		}
	}

	// Chat with AI
	h.sendTyping(msg.Chat.ID)

	response, err := h.ai.chat(msg.Text, "You are Minerva, a helpful personal AI assistant.")
	if err != nil {
		h.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}

	h.sendMessage(msg.Chat.ID, response)
}

func (h *mobileBotHandler) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := h.api.Send(msg); err != nil {
		// Retry without Markdown
		msg.ParseMode = ""
		h.api.Send(msg)
	}
}

func (h *mobileBotHandler) sendTyping(chatID int64) {
	action := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	h.api.Send(action)
}

// createTables creates the database schema
func createTables(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY,
		username TEXT,
		first_name TEXT,
		system_prompt TEXT DEFAULT '',
		memory TEXT DEFAULT '',
		approved INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS conversations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		title TEXT DEFAULT 'New conversation',
		active INTEGER DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		conversation_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		tool_calls TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id)
	);

	CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		message TEXT NOT NULL,
		remind_at DATETIME NOT NULL,
		status TEXT DEFAULT 'pending',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);
	`
	_, err := db.Exec(schema)
	return err
}

// mobileAIClient is a simplified AI client for mobile
type mobileAIClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

func newMobileAIClient(apiKey string, models []string) *mobileAIClient {
	model := "x-ai/grok-4.1-fast"
	if len(models) > 0 && models[0] != "" {
		model = models[0]
	}

	return &mobileAIClient{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// OpenRouter API types
type openRouterRequest struct {
	Model    string              `json:"model"`
	Messages []openRouterMessage `json:"messages"`
}

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *mobileAIClient) chat(userMessage, systemPrompt string) (string, error) {
	messages := []openRouterMessage{}

	if systemPrompt != "" {
		messages = append(messages, openRouterMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	messages = append(messages, openRouterMessage{
		Role:    "user",
		Content: userMessage,
	})

	reqBody := openRouterRequest{
		Model:    c.model,
		Messages: messages,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("HTTP-Referer", "https://home.jairo.cloud")
	req.Header.Set("X-Title", "Minerva Mobile")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp openRouterResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("no response from API")
	}

	return apiResp.Choices[0].Message.Content, nil
}
