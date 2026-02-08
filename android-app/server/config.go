package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all configuration for Minerva Android
type Config struct {
	TelegramBotToken    string
	DatabasePath        string
	MaxContextMessages  int
	AdminID             int64
	ResendAPIKey        string
	ResendWebhookSecret string
	WebhookPort         int
	AgentPassword       string
	GoogleAPIKey        string
	RelayURL            string
	RelayKey            string
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	_ = godotenv.Overload()

	config := &Config{
		TelegramBotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		DatabasePath:        getEnvOrDefault("DATABASE_PATH", "./minerva.db"),
		MaxContextMessages:  getEnvAsIntOrDefault("MAX_CONTEXT_MESSAGES", 20),
		AdminID:             int64(getEnvAsIntOrDefault("ADMIN_ID", 0)),
		ResendAPIKey:        os.Getenv("RESEND_API_KEY"),
		ResendWebhookSecret: os.Getenv("RESEND_WEBHOOK_SECRET"),
		WebhookPort:         getEnvAsIntOrDefault("WEBHOOK_PORT", 8081),
		AgentPassword:       os.Getenv("AGENT_PASSWORD"),
		GoogleAPIKey:        os.Getenv("GOOGLE_API_KEY"),
		RelayURL:            os.Getenv("RELAY_URL"),
		RelayKey:            os.Getenv("RELAY_KEY"),
	}

	return config, nil
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

// UpdateEnvFile updates a key in the .env file
func UpdateEnvFile(key, value string) error {
	_ = godotenv.Overload()
	envMap, err := godotenv.Read(".env")
	if err != nil {
		envMap = make(map[string]string)
	}
	envMap[key] = value
	return godotenv.Write(envMap, ".env")
}

// GetEnvKeys returns all env keys for display (masking values)
func GetEnvKeys() map[string]string {
	_ = godotenv.Overload()
	envMap, _ := godotenv.Read(".env")
	masked := make(map[string]string)
	for k, v := range envMap {
		if strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "key") || strings.Contains(strings.ToLower(k), "secret") {
			if len(v) > 8 {
				masked[k] = v[:4] + "..." + v[len(v)-4:]
			} else {
				masked[k] = "***"
			}
		} else {
			masked[k] = v
		}
	}
	return masked
}
