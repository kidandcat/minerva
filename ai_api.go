package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// OpenRouterClient handles API calls to OpenRouter
type OpenRouterClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	model      string
}

// OpenRouterRequest represents the API request
type OpenRouterRequest struct {
	Model    string              `json:"model"`
	Messages []OpenRouterMessage `json:"messages"`
}

// OpenRouterMessage represents a message in the API format
type OpenRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenRouterResponse represents the API response
type OpenRouterResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// NewOpenRouterClient creates a new OpenRouter API client
func NewOpenRouterClient() *OpenRouterClient {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		log.Println("[OpenRouter] Warning: OPENROUTER_API_KEY not set")
	}

	model := os.Getenv("OPENROUTER_MODEL")
	if model == "" {
		model = "anthropic/claude-sonnet-4" // Default model
	}

	return &OpenRouterClient{
		apiKey:  apiKey,
		baseURL: "https://openrouter.ai/api/v1",
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		model: model,
	}
}

// Chat sends a message to OpenRouter and returns the response
func (c *OpenRouterClient) Chat(messages []ChatMessage, systemPrompt string) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("OPENROUTER_API_KEY not configured")
	}

	// Convert messages to OpenRouter format
	orMessages := make([]OpenRouterMessage, 0, len(messages)+1)

	// Add system prompt as first message if provided
	if systemPrompt != "" {
		orMessages = append(orMessages, OpenRouterMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	// Convert chat messages
	for _, msg := range messages {
		content := extractContent(msg.Content)
		orMessages = append(orMessages, OpenRouterMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	req := OpenRouterRequest{
		Model:    c.model,
		Messages: orMessages,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Printf("[OpenRouter] Sending request to %s with %d messages", c.model, len(orMessages))

	httpReq, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("HTTP-Referer", "https://minerva.jairo.cloud")
	httpReq.Header.Set("X-Title", "Minerva AI Assistant")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var orResp OpenRouterResponse
	if err := json.Unmarshal(body, &orResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if orResp.Error != nil {
		return "", fmt.Errorf("API error: %s", orResp.Error.Message)
	}

	if len(orResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	result := orResp.Choices[0].Message.Content
	log.Printf("[OpenRouter] Got response (tokens: %d in / %d out)",
		orResp.Usage.PromptTokens, orResp.Usage.CompletionTokens)

	return result, nil
}
