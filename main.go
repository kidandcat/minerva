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
	"strconv"
	"strings"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"minerva/tools"
)

var db *DB

const lockFile = "/tmp/minerva.lock"

var lockFd *os.File

// acquireLock uses flock to ensure only one instance of minerva runs at a time.
// If the lock file contains a PID of a dead process, it reclaims the lock.
func acquireLock() error {
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("cannot open lock file %s: %w", lockFile, err)
	}

	// Try non-blocking exclusive lock
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Lock held by another process — read the PID for a clear error message
		data, _ := io.ReadAll(f)
		f.Close()
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			return fmt.Errorf("another instance is already running (PID %d, lock file: %s)", pid, lockFile)
		}
		return fmt.Errorf("another instance is already running (lock file: %s)", lockFile)
	}

	// Lock acquired — write our PID
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	lockFd = f // keep open so the flock is held for the process lifetime
	return nil
}

// releaseLock releases the flock and removes the lock file.
func releaseLock() {
	if lockFd != nil {
		syscall.Flock(int(lockFd.Fd()), syscall.LOCK_UN)
		lockFd.Close()
		os.Remove(lockFile)
	}
}

func main() {
	// Check if CLI subcommand is provided
	if len(os.Args) > 1 {
		runCLI()
		return
	}

	// Run as bot (default behavior)
	runBot()
}

// runCLI handles CLI subcommands
func runCLI() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	// Handle help before anything else
	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		printUsage()
		return
	}

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
	case "reminder":
		handleReminderCLI(db, userID, args)
	case "memory":
		handleMemoryCLI(db, userID, args)
	case "send":
		handleSendCLI(config, args)
	case "context":
		handleContextCLI(db, userID, config.MaxContextMessages)
	case "agent":
		handleAgentCLI(config, args)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Minerva CLI - Personal AI Assistant

Usage:
  minerva                              Run the Telegram bot
  minerva reminder create "text" --at "2024-02-06T10:00:00Z"  Create a reminder
  minerva reminder list                List pending reminders
  minerva reminder delete <id>         Delete a reminder
  minerva memory get [key]             Get user memory (all or grep for key)
  minerva memory set "content"         Set/update user memory
  minerva send "message"               Send a message to admin via Telegram
  minerva context                      Get recent conversation context
  minerva agent list                   List connected agents and their projects
  minerva agent run <name> "prompt" [--dir /path]  Run a task on an agent
  minerva help                         Show this help message`)
}

func handleReminderCLI(db *DB, userID int64, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: reminder subcommand required (create, list, delete)\n")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "create":
		if len(subargs) < 3 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva reminder create \"message\" --at \"2024-02-06T10:00:00Z\"\n")
			os.Exit(1)
		}
		message := subargs[0]
		var remindAt string
		for i, arg := range subargs {
			if arg == "--at" && i+1 < len(subargs) {
				remindAt = subargs[i+1]
				break
			}
		}
		if remindAt == "" {
			fmt.Fprintf(os.Stderr, "error: --at flag is required\n")
			os.Exit(1)
		}

		// Create reminder using the tools package
		argsJSON, _ := json.Marshal(map[string]string{
			"message":   message,
			"remind_at": remindAt,
			"target":    "user",
		})
		result, err := tools.CreateReminder(db.DB, userID, string(argsJSON))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(result)

	case "list":
		result, err := tools.ListReminders(db.DB, userID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(result)

	case "delete":
		if len(subargs) < 1 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva reminder delete <id>\n")
			os.Exit(1)
		}
		id, err := strconv.ParseInt(subargs[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid reminder ID: %v\n", err)
			os.Exit(1)
		}
		argsJSON, _ := json.Marshal(map[string]int64{"id": id})
		result, err := tools.DeleteReminder(db.DB, userID, string(argsJSON))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(result)

	default:
		fmt.Fprintf(os.Stderr, "error: unknown reminder subcommand: %s\n", subcmd)
		os.Exit(1)
	}
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
			fmt.Fprintf(os.Stderr, "error: usage: minerva memory set \"content\"\n")
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
		fmt.Fprintf(os.Stderr, "error: usage: minerva send \"message\"\n")
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
	baseURL := "http://localhost:8080"
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
			fmt.Fprintf(os.Stderr, "error: usage: minerva agent run <agent-name> \"prompt\" [--dir /path]\n")
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

// runBot runs the main Telegram bot
func runBot() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Minerva - Personal AI Assistant")

	// Check for existing instance
	if err := acquireLock(); err != nil {
		log.Fatalf("Failed to start: %v", err)
	}
	defer releaseLock()

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Println("Configuration loaded")
	log.Printf("  Database: %s", config.DatabasePath)
	log.Printf("  Max context: %d messages", config.MaxContextMessages)
	log.Println("  AI: Claude Code (local)")

	// Initialize database
	db, err = InitDB(config.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()
	log.Println("Database initialized")

	// Initialize email
	if config.ResendAPIKey != "" {
		tools.SetResendAPIKey(config.ResendAPIKey)
		log.Println("Email (Resend) configured")
	}

	// Create AI client
	ai := NewAIClient(config.OpenRouterAPIKey, config.Models)

	// Create bot
	bot, err := NewBot(config, db, ai)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}
	log.Println("Bot created")

	// Initialize task runner
	bot.taskRunner = NewTaskRunner(bot)
	log.Println("Task runner initialized")

	// Initialize agent hub with notification callback
	agentNotify := func(message string) {
		if config.AdminID != 0 {
			bot.sendMessage(config.AdminID, message)
		}
	}
	bot.agentHub = NewAgentHub(config.AgentPassword, agentNotify)
	if config.AgentPassword != "" {
		log.Println("Agent hub initialized (password protected)")
	} else {
		log.Println("Agent hub initialized (WARNING: no password set)")
	}

	// Start reminder checker
	stopReminders := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		log.Println("Reminder checker started")

		for {
			select {
			case <-ticker.C:
				if err := bot.CheckReminders(); err != nil {
					log.Printf("Reminder check error: %v", err)
				}
			case <-stopReminders:
				return
			}
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Initialize Twilio
	var twilioManager *TwilioCallManager
	if config.TwilioAccountSID != "" && config.TwilioAuthToken != "" {
		baseURL := "https://home.jairo.cloud"
		twilioManager = NewTwilioCallManager(bot, config.TwilioAccountSID, config.TwilioAuthToken, config.TwilioPhoneNumber, baseURL)
		bot.twilioManager = twilioManager
		log.Println("Twilio configured")

		// Start balance checker (alerts when below $5)
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			var alerted bool
			for range ticker.C {
				balance, err := twilioManager.GetBalance()
				if err != nil {
					log.Printf("Failed to check Twilio balance: %v", err)
					continue
				}
				if balance < 5.0 && !alerted {
					bot.sendMessage(config.AdminID, fmt.Sprintf("⚠️ *Twilio balance bajo*\nSaldo actual: $%.2f", balance))
					alerted = true
				} else if balance >= 5.0 {
					alerted = false
				}
			}
		}()
	}

	// Start webhook server
	if config.WebhookPort > 0 {
		webhook := NewWebhookServer(bot, config.WebhookPort, config.ResendWebhookSecret, twilioManager, bot.agentHub)
		go func() {
			if err := webhook.Start(); err != nil {
				log.Printf("Webhook server error: %v", err)
			}
		}()
	}

	// Start bot
	go func() {
		log.Println("Starting bot polling...")
		bot.Start()
	}()

	// Wait for shutdown
	sig := <-sigChan
	log.Printf("Received %v, shutting down...", sig)

	close(stopReminders)
	bot.Stop()

	log.Println("Minerva shutdown complete")
}
