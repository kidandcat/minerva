package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the Minerva bot
type Config struct {
	TelegramBotToken   string
	DatabasePath       string
	MaxContextMessages int
	AdminID            int64  // Telegram user ID of the admin
	ResendAPIKey         string // Resend API key for email
	ResendWebhookSecret  string // Resend webhook signing secret
	WebhookPort          int    // Port for incoming webhooks
	TwilioAccountSID     string // Twilio Account SID
	TwilioAuthToken      string // Twilio Auth Token
	TwilioPhoneNumber    string // Twilio phone number for outbound calls
	AgentPassword        string // Password for agent authentication
	GoogleAPIKey         string // Google API Key for Gemini Live voice
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	// Load .env file (overrides existing env vars)
	_ = godotenv.Overload()

	config := &Config{
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		DatabasePath:       getEnvOrDefault("DATABASE_PATH", "./minerva.db"),
		MaxContextMessages: getEnvAsIntOrDefault("MAX_CONTEXT_MESSAGES", 20),
		AdminID:            int64(getEnvAsIntOrDefault("ADMIN_ID", 0)),
		ResendAPIKey:       os.Getenv("RESEND_API_KEY"),
		ResendWebhookSecret: os.Getenv("RESEND_WEBHOOK_SECRET"),
		WebhookPort:        getEnvAsIntOrDefault("WEBHOOK_PORT", 8080),
		TwilioAccountSID:   os.Getenv("TWILIO_ACCOUNT_SID"),
		TwilioAuthToken:    os.Getenv("TWILIO_AUTH_TOKEN"),
		TwilioPhoneNumber:  os.Getenv("TWILIO_PHONE_NUMBER"),
		AgentPassword:      os.Getenv("AGENT_PASSWORD"),
		GoogleAPIKey:       os.Getenv("GOOGLE_API_KEY"),
	}

	return config, nil
}

// LoadConfigForCLI loads configuration without requiring bot tokens
func LoadConfigForCLI() (*Config, error) {
	// Load .env file (overrides existing env vars)
	_ = godotenv.Overload()

	config := &Config{
		TelegramBotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		DatabasePath:        getEnvOrDefault("DATABASE_PATH", "./minerva.db"),
		MaxContextMessages:  getEnvAsIntOrDefault("MAX_CONTEXT_MESSAGES", 20),
		AdminID:             int64(getEnvAsIntOrDefault("ADMIN_ID", 0)),
		ResendAPIKey:        os.Getenv("RESEND_API_KEY"),
		ResendWebhookSecret: os.Getenv("RESEND_WEBHOOK_SECRET"),
		WebhookPort:         getEnvAsIntOrDefault("WEBHOOK_PORT", 8080),
		TwilioAccountSID:    os.Getenv("TWILIO_ACCOUNT_SID"),
		TwilioAuthToken:     os.Getenv("TWILIO_AUTH_TOKEN"),
		TwilioPhoneNumber:   os.Getenv("TWILIO_PHONE_NUMBER"),
		AgentPassword:       os.Getenv("AGENT_PASSWORD"),
		GoogleAPIKey:        os.Getenv("GOOGLE_API_KEY"),
	}

	return config, nil
}

// updateEnvFile updates or adds a key=value pair in the .env file
func updateEnvFile(key, value string) error {
	envFile := ".env"
	lines, err := readLines(envFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	found := false
	prefix := key + "="
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = fmt.Sprintf("%s=%s", key, value)
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, fmt.Sprintf("%s=%s", key, value))
	}

	return os.WriteFile(envFile, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsIntOrDefault(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}

	return value
}
