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
	case "file":
		handleFileCLI(config, args)
	case "schedule":
		handleScheduleCLI(db, args)
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
  minerva memory get [key]             Get user memory (all or grep for key)
  minerva memory set "content"         Set/update user memory
  minerva send "message"               Send a message to admin via Telegram
  minerva context                      Get recent conversation context
  minerva agent list                   List connected agents and their projects
  minerva agent run <name> "prompt" [--dir /path]  Run a task on an agent
  minerva email send <to> --subject "subject" --body "body" [--from "sender"]  Send email via Resend
  minerva call <number> "purpose"      Make a phone call (via Telnyx)
  minerva phone list                   List connected Android phones
  minerva phone call <number> "purpose"  Make a call via Android phone
  minerva file send <path> ["caption"]  Send a file to admin via Telegram
  minerva schedule create "task" --at "time" [--agent name] [--dir /path] [--recurring daily|weekly|monthly]
  minerva schedule list                List active scheduled tasks
  minerva schedule delete <id>         Delete a scheduled task
  minerva schedule run <id>            Manually trigger a scheduled task
  minerva help                         Show this help message`)
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

	chunks := splitMessage(message, 4096)
	for _, chunk := range chunks {
		msg := tgbotapi.NewMessage(config.AdminID, chunk)
		msg.ParseMode = "Markdown"
		_, err = api.Send(msg)
		if err != nil {
			msg.ParseMode = ""
			_, err = api.Send(msg)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to send message: %v\n", err)
			os.Exit(1)
		}
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

	baseURL := config.CLIBaseURL()

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
			fmt.Fprintf(os.Stderr, "error: usage: minerva email send <to> --subject \"subject\" --body \"body\" [--from \"sender\"]\n")
			os.Exit(1)
		}

		to := subargs[0]
		var subject, body, from string
		for i, arg := range subargs {
			if arg == "--subject" && i+1 < len(subargs) {
				subject = subargs[i+1]
			}
			if arg == "--body" && i+1 < len(subargs) {
				body = subargs[i+1]
			}
			if arg == "--from" && i+1 < len(subargs) {
				from = subargs[i+1]
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
		if config.FromEmail != "" {
			tools.SetFromEmail(config.FromEmail)
		}
		if len(config.VerifiedEmailDomains) > 0 {
			tools.SetVerifiedDomains(config.VerifiedEmailDomains)
		}

		emailArgs := map[string]string{
			"to":      to,
			"subject": subject,
			"body":    body,
		}
		if from != "" {
			emailArgs["from"] = from
		}
		argsJSON, _ := json.Marshal(emailArgs)

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

	baseURL := config.CLIBaseURL()

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

	baseURL := config.CLIBaseURL()

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

func handleFileCLI(config *Config, args []string) {
	if len(args) < 2 || args[0] != "send" {
		fmt.Fprintf(os.Stderr, "error: usage: minerva file send <path> [\"caption\"]\n")
		os.Exit(1)
	}

	filePath := args[1]
	caption := ""
	if len(args) > 2 {
		caption = args[2]
	}

	if config.TelegramBotToken == "" {
		fmt.Fprintf(os.Stderr, "error: TELEGRAM_BOT_TOKEN not configured\n")
		os.Exit(1)
	}
	if config.AdminID == 0 {
		fmt.Fprintf(os.Stderr, "error: ADMIN_ID not configured\n")
		os.Exit(1)
	}

	// Verify file exists
	info, err := os.Stat(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if info.IsDir() {
		fmt.Fprintf(os.Stderr, "error: %s is a directory, not a file\n", filePath)
		os.Exit(1)
	}

	api, err := tgbotapi.NewBotAPI(config.TelegramBotToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create bot API: %v\n", err)
		os.Exit(1)
	}

	// Send as photo for image files, document for everything else
	ext := strings.ToLower(filePath)
	isImage := strings.HasSuffix(ext, ".jpg") || strings.HasSuffix(ext, ".jpeg") ||
		strings.HasSuffix(ext, ".png") || strings.HasSuffix(ext, ".gif") ||
		strings.HasSuffix(ext, ".webp")

	var msg tgbotapi.Chattable
	if isImage {
		photo := tgbotapi.NewPhoto(config.AdminID, tgbotapi.FilePath(filePath))
		photo.Caption = caption
		msg = photo
	} else {
		doc := tgbotapi.NewDocument(config.AdminID, tgbotapi.FilePath(filePath))
		doc.Caption = caption
		msg = doc
	}

	_, err = api.Send(msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to send file: %v\n", err)
		os.Exit(1)
	}

	result, _ := json.Marshal(map[string]any{
		"success":   true,
		"message":   "File sent to admin",
		"file_name": info.Name(),
		"file_size": info.Size(),
	})
	fmt.Println(string(result))
}

func handleScheduleCLI(db *DB, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "error: schedule subcommand required (create, list, delete, run)\n")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "create":
		if len(subargs) < 1 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva schedule create \"task description\" --at \"2026-02-10T16:00:00+01:00\" [--agent name] [--dir /path] [--recurring daily|weekly|monthly]\n")
			os.Exit(1)
		}
		description := subargs[0]
		var scheduledAt, agentName, workingDir, recurring string
		for i, arg := range subargs {
			switch arg {
			case "--at":
				if i+1 < len(subargs) {
					scheduledAt = subargs[i+1]
				}
			case "--agent":
				if i+1 < len(subargs) {
					agentName = subargs[i+1]
				}
			case "--dir":
				if i+1 < len(subargs) {
					workingDir = subargs[i+1]
				}
			case "--recurring":
				if i+1 < len(subargs) {
					recurring = subargs[i+1]
				}
			}
		}
		if scheduledAt == "" {
			fmt.Fprintf(os.Stderr, "error: --at flag is required\n")
			os.Exit(1)
		}

		t, err := time.Parse(time.RFC3339, scheduledAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid time format, use ISO8601 (e.g., 2026-02-10T16:00:00+01:00): %v\n", err)
			os.Exit(1)
		}
		if t.Before(time.Now()) {
			fmt.Fprintf(os.Stderr, "error: scheduled time must be in the future\n")
			os.Exit(1)
		}
		if recurring != "" && recurring != "none" && recurring != "daily" && recurring != "weekly" && recurring != "monthly" {
			fmt.Fprintf(os.Stderr, "error: recurring must be one of: none, daily, weekly, monthly\n")
			os.Exit(1)
		}

		id, err := db.CreateScheduledTask(description, t, agentName, workingDir, recurring)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		target := agentName
		if target == "" {
			target = "brain"
		}
		result, _ := json.Marshal(map[string]any{
			"success":      true,
			"id":           id,
			"description":  description,
			"scheduled_at": t.Format(time.RFC3339),
			"agent":        target,
			"dir":          workingDir,
			"recurring":    recurring,
			"message":      fmt.Sprintf("Task scheduled for %s (target: %s)", t.Format("Jan 2, 2006 at 15:04"), target),
		})
		fmt.Println(string(result))

	case "list":
		tasks, err := db.GetScheduledTasks()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		type taskResult struct {
			ID          int64  `json:"id"`
			Description string `json:"description"`
			ScheduledAt string `json:"scheduled_at"`
			Agent       string `json:"agent"`
			Dir         string `json:"dir,omitempty"`
			Status      string `json:"status"`
			Recurring   string `json:"recurring"`
		}

		var results []taskResult
		for _, t := range tasks {
			agent := t.AgentName
			if agent == "" {
				agent = "brain"
			}
			results = append(results, taskResult{
				ID:          t.ID,
				Description: t.Description,
				ScheduledAt: t.ScheduledAt.Format(time.RFC3339),
				Agent:       agent,
				Dir:         t.WorkingDir,
				Status:      t.Status,
				Recurring:   t.Recurring,
			})
		}

		response, _ := json.Marshal(map[string]any{
			"success": true,
			"tasks":   results,
			"count":   len(results),
		})
		fmt.Println(string(response))

	case "delete":
		if len(subargs) < 1 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva schedule delete <id>\n")
			os.Exit(1)
		}
		id, err := strconv.ParseInt(subargs[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid task ID: %v\n", err)
			os.Exit(1)
		}
		if err := db.DeleteScheduledTask(id); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		result, _ := json.Marshal(map[string]any{
			"success": true,
			"message": "Scheduled task deleted",
		})
		fmt.Println(string(result))

	case "run":
		if len(subargs) < 1 {
			fmt.Fprintf(os.Stderr, "error: usage: minerva schedule run <id>\n")
			os.Exit(1)
		}
		id, err := strconv.ParseInt(subargs[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid task ID: %v\n", err)
			os.Exit(1)
		}

		task, err := db.GetScheduledTask(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: task not found: %v\n", err)
			os.Exit(1)
		}
		if task.Status != "pending" {
			fmt.Fprintf(os.Stderr, "error: task is not in pending status (current: %s)\n", task.Status)
			os.Exit(1)
		}

		// Set scheduled_at to now so the scheduler picks it up on next tick
		if err := db.RescheduleTask(id, time.Now().Add(-1*time.Second)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		result, _ := json.Marshal(map[string]any{
			"success": true,
			"message": "Task will execute on next scheduler tick (within 1 minute)",
		})
		fmt.Println(string(result))

	default:
		fmt.Fprintf(os.Stderr, "error: unknown schedule subcommand: %s\n", subcmd)
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
	log.Printf("  AI: Claude CLI")

	// Start server
	if err := StartServer(config); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	// Wait for shutdown signal
	WaitForShutdown()

	// Stop server
	StopServer()
}
