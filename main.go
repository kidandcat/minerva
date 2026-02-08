package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"minerva/tools"
)

var db *DB

var lockFd *os.File

func getLockFile() string {
	// Prefer HOME over TMPDIR for Android compatibility (SELinux issues with /tmp)
	if home := os.Getenv("HOME"); home != "" {
		return home + "/.minerva.lock"
	}
	if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
		return tmpDir + "/minerva.lock"
	}
	return "/tmp/minerva.lock"
}

// acquireLock uses flock to ensure only one instance of minerva runs at a time.
// If the lock file contains a PID of a dead process, it reclaims the lock.
func acquireLock() error {
	f, err := os.OpenFile(getLockFile(), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("cannot open lock file %s: %w", getLockFile(), err)
	}

	// Try non-blocking exclusive lock
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Lock held by another process — read the PID for a clear error message
		data, _ := io.ReadAll(f)
		f.Close()
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			return fmt.Errorf("another instance is already running (PID %d, lock file: %s)", pid, getLockFile())
		}
		return fmt.Errorf("another instance is already running (lock file: %s)", getLockFile())
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
		os.Remove(getLockFile())
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
	case "email":
		handleEmailCLI(config, args)
	case "call":
		handleCallCLI(config, args)
	case "phone":
		handlePhoneCLI(config, args)
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
  minerva reminder delete <id>         Dismiss a reminder
  minerva reminder reschedule <id> --at "time"  Reschedule a reminder
  minerva memory get [key]             Get user memory (all or grep for key)
  minerva memory set "content"         Set/update user memory
  minerva send "message"               Send a message to admin via Telegram
  minerva context                      Get recent conversation context
  minerva agent list                   List connected agents and their projects
  minerva agent run <name> "prompt" [--dir /path]  Run a task on an agent
  minerva email send <to> --subject "subject" --body "body"  Send email via Resend
  minerva call <number> "purpose"      Make a phone call (via Twilio)
  minerva phone list                   List connected Android phones
  minerva phone call <number> "purpose"  Make a call via Android phone
  minerva help                         Show this help message`)
}

func handleReminderCLI(db *DB, userID int64, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: reminder subcommand required (create, list, delete, reschedule)\n")
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

	case "reschedule":
		if len(subargs) < 3 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva reminder reschedule <id> --at \"2024-02-06T10:00:00Z\"\n")
			os.Exit(1)
		}
		id, err := strconv.ParseInt(subargs[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid reminder ID: %v\n", err)
			os.Exit(1)
		}
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
		argsJSON, _ := json.Marshal(map[string]any{"id": id, "remind_at": remindAt})
		result, err := tools.RescheduleReminder(db.DB, userID, string(argsJSON))
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

	// Default to localhost webhook server (port 8081)
	baseURL := "http://localhost:8081"
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
			fmt.Fprintf(os.Stderr, "error: usage: minerva email send <to> --subject \"subject\" --body \"body\"\n")
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
		fmt.Fprintf(os.Stderr, "error: usage: minerva call <phone_number> \"purpose\"\n")
		fmt.Fprintf(os.Stderr, "example: minerva call +34612345678 \"Llama a esta peluquería y pide cita para mañana a las 10\"\n")
		os.Exit(1)
	}

	phoneNumber := args[0]
	purpose := args[1]

	// Default to localhost webhook server (port 8081)
	baseURL := "http://localhost:8081"
	if envURL := os.Getenv("MINERVA_URL"); envURL != "" {
		baseURL = envURL
	}

	reqBody, _ := json.Marshal(map[string]string{
		"to":      phoneNumber,
		"purpose": purpose,
	})

	resp, err := http.Post(baseURL+"/voice/call", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to connect to Minerva: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}

func handlePhoneCLI(config *Config, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: phone subcommand required (list, call)\n")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	// Default to localhost webhook server (port 8081)
	baseURL := "http://localhost:8081"
	if envURL := os.Getenv("MINERVA_URL"); envURL != "" {
		baseURL = envURL
	}

	switch subcmd {
	case "list":
		resp, err := http.Get(baseURL + "/phone/list")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to connect to Minerva: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		fmt.Println(string(body))

	case "call":
		if len(subargs) < 2 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva phone call <number> \"purpose\"\n")
			os.Exit(1)
		}

		phoneNumber := subargs[0]
		purpose := subargs[1]

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

	default:
		fmt.Fprintf(os.Stderr, "error: unknown phone subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

// runBot runs the main Telegram bot
func runBot() {
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
	log.Printf("  AI: OpenRouter API")

	// Start server
	if err := StartServer(config); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	// Wait for shutdown signal
	WaitForShutdown()

	// Stop server
	StopServer()
}
