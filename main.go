package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"minerva/tools"
)

var db *DB

const pidFile = "/tmp/minerva.pid"

// acquireLock checks if another instance is running and creates a PID file
func acquireLock() error {
	// Check if PID file exists
	if data, err := os.ReadFile(pidFile); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			// Check if process is still running
			process, err := os.FindProcess(pid)
			if err == nil {
				// Send signal 0 to check if process exists
				if err := process.Signal(syscall.Signal(0)); err == nil {
					return fmt.Errorf("another instance is already running (PID %d)", pid)
				}
			}
		}
		// Stale PID file, remove it
		os.Remove(pidFile)
	}

	// Write current PID
	return os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
}

// releaseLock removes the PID file
func releaseLock() {
	os.Remove(pidFile)
}

func main() {
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
	if len(config.Models) > 0 {
		log.Printf("  Models: %v", config.Models)
	} else {
		log.Printf("  Models: %v (default)", DefaultModels)
	}

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

	// Initialize agent hub
	bot.agentHub = NewAgentHub(config.AgentPassword)
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
