package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// CLIBaseURL returns the base URL for CLI commands to connect to the local server
func (c *Config) CLIBaseURL() string {
	if envURL := os.Getenv("MINERVA_URL"); envURL != "" {
		return envURL
	}
	return fmt.Sprintf("http://localhost:%d", c.WebhookPort)
}

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
	BaseURL              string // Public URL for webhooks (e.g., https://example.com)
	FromEmail            string // Email sender address (e.g., Minerva <minerva@example.com>)
	OwnerName            string // Name of the assistant owner (used in voice prompts)
	DefaultCountryCode   string // Default country code for phone numbers (e.g., +34)
	VerifiedEmailDomains []string // Verified email domains for sending (e.g., example.com)
	TasksDir             string // Directory for background tasks
	GeminiModel          string // Gemini model for voice calls
	GeminiVoice          string // Gemini voice name for voice calls
	VoiceLanguage        string // Default language for voice calls (e.g., "Spanish", "English")
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	// Load .env file (overrides existing env vars)
	_ = godotenv.Overload()

	config := loadConfigCommon()
	return config, nil
}

// LoadConfigForCLI loads configuration without requiring bot tokens
func LoadConfigForCLI() (*Config, error) {
	// Load .env file (overrides existing env vars)
	_ = godotenv.Overload()

	config := loadConfigCommon()
	return config, nil
}

func loadConfigCommon() *Config {
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
		BaseURL:            os.Getenv("BASE_URL"),
		FromEmail:          getEnvOrDefault("FROM_EMAIL", ""),
		OwnerName:          getEnvOrDefault("OWNER_NAME", "the owner"),
		DefaultCountryCode: getEnvOrDefault("DEFAULT_COUNTRY_CODE", "+1"),
		TasksDir:           getEnvOrDefault("TASKS_DIR", "./minerva-tasks"),
		GeminiModel:        getEnvOrDefault("GEMINI_MODEL", "models/gemini-2.5-flash-native-audio-latest"),
		GeminiVoice:        getEnvOrDefault("GEMINI_VOICE", "Zephyr"),
		VoiceLanguage:      getEnvOrDefault("VOICE_LANGUAGE", "Spanish"),
	}

	// Parse verified email domains
	domainsStr := os.Getenv("VERIFIED_EMAIL_DOMAINS")
	if domainsStr != "" {
		for _, d := range strings.Split(domainsStr, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				config.VerifiedEmailDomains = append(config.VerifiedEmailDomains, d)
			}
		}
	}

	return config
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
