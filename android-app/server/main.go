package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"minerva-android/tools"
)

var startTime time.Time

func main() {
	startTime = time.Now()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// CLI subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check":
			// Quick health check - verify Claude Code works
			fmt.Println("Checking Claude Code authentication...")
			if err := CheckClaudeAuth(); err != nil {
				fmt.Printf("FAIL: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("OK")
			os.Exit(0)
		case "help", "--help", "-h":
			printUsage()
			return
		default:
			runCLI()
			return
		}
	}

	// Acquire lock BEFORE dropping privileges (SELinux context issue on Android)
	lockFile := filepath.Join(os.Getenv("HOME"), ".minerva-android.lock")
	lock, err := acquireLock(lockFile)
	if err != nil {
		log.Fatalf("Another instance is running: %v", err)
	}
	defer lock.Close()
	defer os.Remove(lockFile)

	// Drop from root to Termux user while keeping inet group for network
	// Must happen after lock acquisition (SELinux blocks file ops after context change)
	if os.Getuid() == 0 {
		dropPrivileges()
	}

	// Load config
	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if config.TelegramBotToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	if config.AdminID == 0 {
		log.Fatal("ADMIN_ID is required")
	}

	log.Println("Starting Minerva Android")

	// Initialize database
	dbDir := filepath.Dir(config.DatabasePath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Fatalf("Failed to create database directory: %v", err)
	}

	db, err := InitDB(config.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()
	log.Println("Database initialized")

	// Create AI client (Claude Code CLI)
	ai := NewAIClient()
	log.Println("AI client initialized (Claude Code CLI)")

	// Create bot
	bot, err := NewBot(config, db, ai)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	// Initialize task runner
	bot.taskRunner = NewTaskRunner(bot)
	log.Println("Task runner initialized")

	// Initialize agent hub
	agentNotify := func(message string) {
		if config.AdminID != 0 {
			bot.sendMessage(config.AdminID, message)
		}
	}
	bot.agentHub = NewAgentHub(config.AgentPassword, agentNotify)
	log.Println("Agent hub initialized")

	// Initialize voice manager and phone bridge
	var phoneBridge *PhoneBridge
	if config.GoogleAPIKey != "" {
		voiceManager := NewVoiceManager(bot, config.GoogleAPIKey)
		phoneBridge = NewPhoneBridge(bot, voiceManager)
		log.Println("Voice manager and phone bridge initialized")
	} else {
		log.Println("GOOGLE_API_KEY not set, phone bridge disabled")
	}

	// Start webhook server
	if config.WebhookPort > 0 {
		webhook := NewWebhookServer(bot, config.WebhookPort, bot.agentHub, phoneBridge)
		go func() {
			if err := webhook.Start(); err != nil {
				log.Printf("Webhook server error: %v", err)
			}
		}()
	}

	// Start relay client if configured
	if config.RelayURL != "" && config.RelayKey != "" {
		relay := NewRelayClient(config.RelayURL, config.RelayKey, config.WebhookPort)
		relay.Start()
		log.Printf("Relay client connected to %s", config.RelayURL)
	}

	// Start bot polling
	go func() {
		log.Println("Starting bot polling...")
		bot.Start()
	}()

	// Send startup notification
	bot.sendMessage(config.AdminID, fmt.Sprintf("Minerva Android started\nDatabase: %s\nWebhook port: %d", config.DatabasePath, config.WebhookPort))

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	sig := <-sigChan
	log.Printf("Received %v, shutting down...", sig)

	bot.Stop()
	log.Println("Minerva Android shutdown complete")
}

// runCLI handles CLI subcommands
func runCLI() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	// Load config for database path and admin ID (without requiring bot tokens)
	config, err := LoadConfigForCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize database
	db, err := InitDB(config.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to initialize database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	userID := config.AdminID
	if userID == 0 {
		fmt.Fprintf(os.Stderr, "error: ADMIN_ID not configured\n")
		os.Exit(1)
	}

	switch cmd {
	case "memory":
		handleMemoryCLI(db, userID, args)
	case "send":
		handleSendCLI(config, args)
	case "context":
		handleContextCLI(db, userID, config.MaxContextMessages)
	case "agent":
		handleAgentCLI(config, args)
	case "email":
		handleEmailCLI(config, args)
	case "call":
		handleCallCLI(config, args)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Minerva Android CLI - Personal AI Assistant

Usage:
  minerva-android                              Run the Telegram bot
  minerva-android check                        Check Claude Code authentication
  minerva-android memory get [key]             Get user memory (all or grep for key)
  minerva-android memory set "content"         Set/update user memory
  minerva-android send "message"               Send a message to admin via Telegram
  minerva-android context                      Get recent conversation context
  minerva-android agent list                   List connected agents and their projects
  minerva-android agent run <name> "prompt" [--dir /path]  Run a task on an agent
  minerva-android email send <to> --subject "subject" --body "body"  Send email via Resend
  minerva-android call <number> "purpose"      Make a phone call (via Android + Gemini Live)
  minerva-android help                         Show this help message`)
}

func handleMemoryCLI(db *DB, userID int64, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: memory subcommand required (get, set)\n")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "get":
		memory, err := tools.GetMemory(db.DB, userID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if memory == "" {
			fmt.Println("{\"memory\": null, \"message\": \"No memory stored\"}")
			return
		}
		// If a key is provided, filter the memory
		if len(subargs) > 0 {
			key := strings.ToLower(subargs[0])
			lines := strings.Split(memory, "\n")
			var filtered []string
			for _, line := range lines {
				if strings.Contains(strings.ToLower(line), key) {
					filtered = append(filtered, line)
				}
			}
			if len(filtered) == 0 {
				fmt.Printf("{\"memory\": null, \"message\": \"No memory found for key: %s\"}\n", key)
				return
			}
			result, _ := json.Marshal(map[string]any{
				"memory":     strings.Join(filtered, "\n"),
				"filtered":   true,
				"key":        key,
				"char_count": len(strings.Join(filtered, "\n")),
			})
			fmt.Println(string(result))
			return
		}
		result, _ := json.Marshal(map[string]any{
			"memory":     memory,
			"char_count": len(memory),
			"char_limit": 2000,
		})
		fmt.Println(string(result))

	case "set":
		if len(subargs) < 1 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva-android memory set \"content\"\n")
			os.Exit(1)
		}
		content := subargs[0]
		argsJSON, _ := json.Marshal(map[string]string{"content": content})
		result, err := tools.UpdateMemory(db.DB, userID, string(argsJSON))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(result)

	default:
		fmt.Fprintf(os.Stderr, "error: unknown memory subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func handleSendCLI(config *Config, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: usage: minerva-android send \"message\"\n")
		os.Exit(1)
	}

	message := args[0]
	if config.TelegramBotToken == "" {
		fmt.Fprintf(os.Stderr, "error: TELEGRAM_BOT_TOKEN not configured\n")
		os.Exit(1)
	}
	if config.AdminID == 0 {
		fmt.Fprintf(os.Stderr, "error: ADMIN_ID not configured\n")
		os.Exit(1)
	}

	api, err := tgbotapi.NewBotAPI(config.TelegramBotToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create bot API: %v\n", err)
		os.Exit(1)
	}

	msg := tgbotapi.NewMessage(config.AdminID, message)
	msg.ParseMode = "Markdown"
	_, err = api.Send(msg)
	if err != nil {
		// Retry without Markdown
		msg.ParseMode = ""
		_, err = api.Send(msg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to send message: %v\n", err)
		os.Exit(1)
	}

	result, _ := json.Marshal(map[string]any{
		"success": true,
		"message": "Message sent to admin",
	})
	fmt.Println(string(result))
}

func handleContextCLI(db *DB, userID int64, maxMessages int) {
	// Get active conversation
	conv, err := db.GetActiveConversation(userID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Get recent messages
	messages, err := db.GetConversationMessages(conv.ID, maxMessages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	type contextMessage struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		CreatedAt string `json:"created_at"`
	}

	var contextMessages []contextMessage
	for _, m := range messages {
		contextMessages = append(contextMessages, contextMessage{
			Role:      m.Role,
			Content:   m.Content,
			CreatedAt: m.CreatedAt.Format(time.RFC3339),
		})
	}

	result, _ := json.Marshal(map[string]any{
		"conversation_id": conv.ID,
		"message_count":   len(contextMessages),
		"messages":        contextMessages,
	})
	fmt.Println(string(result))
}

func handleAgentCLI(config *Config, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: agent subcommand required (list, run)\n")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	// Default to localhost webhook server
	baseURL := fmt.Sprintf("http://localhost:%d", config.WebhookPort)
	if envURL := os.Getenv("MINERVA_URL"); envURL != "" {
		baseURL = envURL
	}

	switch subcmd {
	case "list":
		resp, err := http.Get(baseURL + "/agent/list")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to connect to Minerva: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		fmt.Println(string(body))

	case "run":
		if len(subargs) < 2 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva-android agent run <agent-name> \"prompt\" [--dir /path]\n")
			os.Exit(1)
		}

		agentName := subargs[0]
		prompt := subargs[1]
		dir := ""

		// Parse optional --dir flag
		for i, arg := range subargs {
			if arg == "--dir" && i+1 < len(subargs) {
				dir = subargs[i+1]
				break
			}
		}

		reqBody, _ := json.Marshal(map[string]string{
			"agent":  agentName,
			"prompt": prompt,
			"dir":    dir,
		})

		resp, err := http.Post(baseURL+"/agent/run", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to connect to Minerva: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		fmt.Println(string(body))

	default:
		fmt.Fprintf(os.Stderr, "error: unknown agent subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func handleEmailCLI(config *Config, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: email subcommand required (send)\n")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "send":
		if len(subargs) < 1 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva-android email send <to> --subject \"subject\" --body \"body\"\n")
			os.Exit(1)
		}

		to := subargs[0]
		var subject, body string
		for i, arg := range subargs {
			if arg == "--subject" && i+1 < len(subargs) {
				subject = subargs[i+1]
			}
			if arg == "--body" && i+1 < len(subargs) {
				body = subargs[i+1]
			}
		}

		if subject == "" {
			fmt.Fprintf(os.Stderr, "error: --subject is required\n")
			os.Exit(1)
		}
		if body == "" {
			fmt.Fprintf(os.Stderr, "error: --body is required\n")
			os.Exit(1)
		}

		if config.ResendAPIKey == "" {
			fmt.Fprintf(os.Stderr, "error: RESEND_API_KEY not configured in .env\n")
			os.Exit(1)
		}

		tools.SetResendAPIKey(config.ResendAPIKey)

		argsJSON, _ := json.Marshal(map[string]string{
			"to":      to,
			"subject": subject,
			"body":    body,
		})

		result, err := tools.SendEmail(string(argsJSON))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(result)

	default:
		fmt.Fprintf(os.Stderr, "error: unknown email subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func handleCallCLI(config *Config, args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "error: usage: minerva-android call <phone_number> \"purpose\"\n")
		fmt.Fprintf(os.Stderr, "example: minerva-android call +34612345678 \"Llama a esta peluqueria y pide cita para manana a las 10\"\n")
		os.Exit(1)
	}

	phoneNumber := args[0]
	purpose := args[1]

	// Default to localhost webhook server
	baseURL := fmt.Sprintf("http://localhost:%d", config.WebhookPort)
	if envURL := os.Getenv("MINERVA_URL"); envURL != "" {
		baseURL = envURL
	}

	reqBody, _ := json.Marshal(map[string]string{
		"to":      phoneNumber,
		"purpose": purpose,
	})

	resp, err := http.Post(baseURL+"/phone/call", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to connect to Minerva: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}

// dropPrivileges drops from root to Termux user (u0_a129 / UID 10129)
// while keeping supplementary groups for network access (inet=3003).
// This allows Claude Code CLI to use --dangerously-skip-permissions
// which refuses to run as root.
func dropPrivileges() {
	const (
		termuxUID = 10129
		termuxGID = 10129
		inetGID   = 3003 // Android network access group
		everyGID  = 9997 // Android everybody group
	)

	groups := []int{termuxGID, inetGID, everyGID}

	if err := syscall.Setgroups(groups); err != nil {
		log.Printf("WARNING: setgroups failed: %v (network may not work)", err)
	}
	if err := syscall.Setgid(termuxGID); err != nil {
		log.Printf("WARNING: setgid failed: %v", err)
	}
	if err := syscall.Setuid(termuxUID); err != nil {
		log.Printf("WARNING: setuid failed: %v", err)
	}

	log.Printf("Dropped privileges to uid=%d gid=%d groups=%v", os.Getuid(), os.Getgid(), groups)
}

// acquireLock creates an exclusive file lock
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock file %s is held by another process", path)
	}
	return f, nil
}
