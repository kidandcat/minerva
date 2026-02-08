package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

var startTime time.Time

func main() {
	startTime = time.Now()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// On Android, drop from root to Termux user while keeping inet group for network
	if runtime.GOOS == "android" && os.Getuid() == 0 {
		dropPrivileges()
	}

	if len(os.Args) > 1 && os.Args[1] == "check" {
		// Quick health check - verify Claude Code works
		fmt.Println("Checking Claude Code authentication...")
		if err := CheckClaudeAuth(); err != nil {
			fmt.Printf("FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("OK")
		os.Exit(0)
	}

	// Acquire lock to prevent multiple instances
	lockFile := filepath.Join(os.Getenv("HOME"), ".minerva-android.lock")
	lock, err := acquireLock(lockFile)
	if err != nil {
		log.Fatalf("Another instance is running: %v", err)
	}
	defer lock.Close()
	defer os.Remove(lockFile)

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

	// Start reminder checker
	stopReminders := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
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

	// Start webhook server
	if config.WebhookPort > 0 {
		webhook := NewWebhookServer(bot, config.WebhookPort, bot.agentHub)
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

	close(stopReminders)
	bot.Stop()
	log.Println("Minerva Android shutdown complete")
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
