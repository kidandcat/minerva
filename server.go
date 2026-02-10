package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"minerva/tools"
)

// ServerState holds the running server components
type ServerState struct {
	bot            *Bot
	db             *DB
	taskRunner     *TaskRunner
	twilioManager  *TwilioCallManager
	voiceManager   *VoiceManager
	phoneBridge    *PhoneBridge
	webhook        *WebhookServer
	relayClient    *RelayClient
	scheduler      *Scheduler
	mu             sync.Mutex
	running        bool
}

var serverState *ServerState
var serverMu sync.Mutex

// StartServer starts the Minerva server with the given configuration
func StartServer(config *Config) error {
	serverMu.Lock()
	defer serverMu.Unlock()

	if serverState != nil && serverState.running {
		return fmt.Errorf("server already running")
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Minerva - Personal AI Assistant")

	// Initialize database
	db, err := InitDB(config.DatabasePath)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	log.Println("Database initialized")

	// Initialize schedule table
	if err := db.InitScheduleTable(); err != nil {
		db.Close()
		return fmt.Errorf("failed to initialize schedule table: %w", err)
	}

	// Initialize email
	if config.ResendAPIKey != "" {
		tools.SetResendAPIKey(config.ResendAPIKey)
		tools.SetFromEmail(config.FromEmail)
		log.Println("Email (Resend) configured")
		if config.ResendWebhookSecret == "" {
			log.Println("WARNING: RESEND_WEBHOOK_SECRET not set - email webhooks will reject all requests")
		}
	}

	// Create AI client
	ai := NewAIClient()
	log.Println("AI client initialized (Claude CLI)")

	// Create bot
	bot, err := NewBot(config, db, ai)
	if err != nil {
		db.Close()
		return fmt.Errorf("failed to create bot: %w", err)
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
	agentResult := func(message string) {
		if config.AdminID != 0 {
			if err := bot.ProcessSystemEvent(config.AdminID, message); err != nil {
				log.Printf("[Agent] Failed to process result through AI brain: %v", err)
				bot.sendMessage(config.AdminID, message)
			}
		}
	}
	bot.agentHub = NewAgentHub(config.AgentPassword, agentNotify, agentResult)
	if config.AgentPassword != "" {
		log.Println("Agent hub initialized (password protected)")
	} else {
		log.Println("Agent hub initialized (WARNING: no password set)")
	}

	// Create server state
	state := &ServerState{
		bot:        bot,
		db:         db,
		taskRunner: bot.taskRunner,
		running:    true,
	}

	// Start scheduler for scheduled tasks
	state.scheduler = NewScheduler(db, bot, bot.agentHub)
	state.scheduler.Start()

	// Initialize Twilio
	if config.TwilioAccountSID != "" && config.TwilioAuthToken != "" {
		state.twilioManager = NewTwilioCallManager(bot, config.TwilioAccountSID, config.TwilioAuthToken, config.TwilioPhoneNumber, config.BaseURL)
		bot.twilioManager = state.twilioManager
		log.Println("Twilio configured")

		// Start balance checker (alerts when below $5)
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			var alerted bool
			for range ticker.C {
				balance, err := state.twilioManager.GetBalance()
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

	// Initialize Voice AI (Gemini Live)
	if config.GoogleAPIKey != "" {
		state.voiceManager = NewVoiceManager(bot, config.GoogleAPIKey, config.BaseURL,
			config.TwilioAccountSID, config.TwilioAuthToken, config.TwilioPhoneNumber,
			config.OwnerName, config.DefaultCountryCode)
		log.Println("Voice AI (Gemini Live) configured")
	}

	// Initialize Phone Bridge (Android)
	if state.voiceManager != nil {
		state.phoneBridge = NewPhoneBridge(bot, state.voiceManager)
		log.Println("Phone Bridge (Android) configured")
	}

	// Start webhook server
	if config.WebhookPort > 0 {
		state.webhook = NewWebhookServer(bot, config.WebhookPort, config.ResendWebhookSecret,
			state.twilioManager, bot.agentHub, state.voiceManager, state.phoneBridge, config)
		go func() {
			if err := state.webhook.Start(); err != nil {
				log.Printf("Webhook server error: %v", err)
			}
		}()
	}

	// Start relay client if configured
	relayURL := os.Getenv("RELAY_URL")
	relayKey := os.Getenv("RELAY_KEY")
	if relayURL != "" && relayKey != "" {
		state.relayClient = NewRelayClient(relayURL, relayKey, config.WebhookPort)
		state.relayClient.Start()
		log.Printf("Relay client connected to %s (encrypted)", relayURL)
	}

	// Start bot polling
	go func() {
		log.Println("Starting bot polling...")
		bot.Start()
	}()

	serverState = state
	return nil
}

// StopServer stops the running Minerva server
func StopServer() {
	serverMu.Lock()
	defer serverMu.Unlock()

	if serverState == nil || !serverState.running {
		return
	}

	log.Println("Stopping Minerva...")

	// Stop components
	if serverState.scheduler != nil {
		serverState.scheduler.Stop()
	}
	serverState.bot.Stop()

	if serverState.relayClient != nil {
		serverState.relayClient.Stop()
	}

	if serverState.db != nil {
		serverState.db.Close()
	}

	serverState.running = false
	serverState = nil

	log.Println("Minerva shutdown complete")
}

// IsServerRunning returns whether the server is currently running
func IsServerRunning() bool {
	serverMu.Lock()
	defer serverMu.Unlock()
	return serverState != nil && serverState.running
}

// WaitForShutdown blocks until a shutdown signal is received
func WaitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	sig := <-sigChan
	log.Printf("Received %v, shutting down...", sig)
}
