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
	stopReminders  chan struct{}
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

	// Initialize email
	if config.ResendAPIKey != "" {
		tools.SetResendAPIKey(config.ResendAPIKey)
		log.Println("Email (Resend) configured")
	}

	// Create AI client
	ai := NewAIClient(config.OpenRouterAPIKey, config.Models)
	log.Printf("AI client initialized (model: %s)", ai.model)

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
	bot.agentHub = NewAgentHub(config.AgentPassword, agentNotify)
	if config.AgentPassword != "" {
		log.Println("Agent hub initialized (password protected)")
	} else {
		log.Println("Agent hub initialized (WARNING: no password set)")
	}

	// Create server state
	state := &ServerState{
		bot:           bot,
		db:            db,
		taskRunner:    bot.taskRunner,
		stopReminders: make(chan struct{}),
		running:       true,
	}

	// Start reminder checker
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
			case <-state.stopReminders:
				return
			}
		}
	}()

	// Initialize Twilio
	if config.TwilioAccountSID != "" && config.TwilioAuthToken != "" {
		baseURL := "https://home.jairo.cloud"
		state.twilioManager = NewTwilioCallManager(bot, config.TwilioAccountSID, config.TwilioAuthToken, config.TwilioPhoneNumber, baseURL)
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
		state.voiceManager = NewVoiceManager(bot, config.GoogleAPIKey, "https://home.jairo.cloud",
			config.TwilioAccountSID, config.TwilioAuthToken, config.TwilioPhoneNumber)
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
			state.twilioManager, bot.agentHub, state.voiceManager, state.phoneBridge)
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
	close(serverState.stopReminders)
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
