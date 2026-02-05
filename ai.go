package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// AIClient handles communication with OpenRouter API
type AIClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
	models     []string // Priority order
}

// ChatMessage represents a message in the conversation
type ChatMessage struct {
	Role       string      `json:"role"`                   // "system", "user", "assistant", "tool"
	Content    interface{} `json:"content"`                // string or []ContentPart for multimodal
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"` // For tool responses
}

// ContentPart represents a part of multimodal content
type ContentPart struct {
	Type     string    `json:"type"`               // "text" or "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL contains the image data
type ImageURL struct {
	URL string `json:"url"` // "data:image/jpeg;base64,..." or URL
}

// ToolCall represents a tool call made by the assistant
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc contains the function details of a tool call
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// Tool represents a tool available to the assistant
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a function tool
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// ChatResponse represents the API response
type ChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"` // Can be string or int
	} `json:"error,omitempty"`
}

// chatRequest represents the request body for the chat API
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []Tool        `json:"tools,omitempty"`
}

// DefaultModels is the default model hierarchy (highest priority first)
// :online suffix enables native web search
var DefaultModels = []string{
	"x-ai/grok-4.1-fast:online",
	"google/gemini-3-flash-preview:online",
}

// NewAIClient creates a new AI client for OpenRouter
func NewAIClient(apiKey string, models []string) *AIClient {
	if len(models) == 0 {
		models = DefaultModels
	}
	return &AIClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		baseURL: "https://openrouter.ai/api/v1",
		models:  models,
	}
}

// ChatResult contains the response and metadata
type ChatResult struct {
	Message *ChatMessage
	Model   string
}

// Chat sends a chat completion request with automatic fallback
func (c *AIClient) Chat(messages []ChatMessage, systemPrompt string, tools []Tool) (*ChatResult, error) {
	// Base system prompt
	basePrompt := "You are Minerva, a helpful personal AI assistant. You can help with reminders, notes, and general conversation."
	finalSystemPrompt := basePrompt
	if systemPrompt != "" {
		finalSystemPrompt = basePrompt + "\n\n" + systemPrompt
	}

	// Prepend system prompt
	allMessages := append([]ChatMessage{{
		Role:    "system",
		Content: finalSystemPrompt,
	}}, messages...)

	var lastErr error
	for _, model := range c.models {
		resp, err := c.chatWithModel(model, allMessages, tools)
		if err == nil {
			return &ChatResult{Message: resp, Model: model}, nil
		}

		// Check if error is retryable (availability, rate limit, unsupported input)
		if isRetryableError(err) {
			log.Printf("Model %s failed with retryable error: %v, trying next model", model, err)
			lastErr = err
			continue
		}

		// Non-retryable error, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("all models failed, last error: %w", lastErr)
}

// chatWithModel sends a request to a specific model
func (c *AIClient) chatWithModel(model string, messages []ChatMessage, tools []Tool) (*ChatMessage, error) {
	reqBody := chatRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://github.com/kidandcat/minerva")
	req.Header.Set("X-Title", "Minerva")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &RetryableError{Err: fmt.Errorf("failed to send request: %w", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Handle HTTP errors
	if resp.StatusCode != http.StatusOK {
		// 429 = rate limit, 503 = service unavailable, 502 = bad gateway
		if resp.StatusCode == 429 || resp.StatusCode == 503 || resp.StatusCode == 502 {
			return nil, &RetryableError{Err: fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))}
		}
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for API errors in response
	if chatResp.Error != nil {
		errMsg := chatResp.Error.Message
		// Check for common retryable error patterns
		if isRetryableErrorMessage(errMsg) {
			return nil, &RetryableError{Err: fmt.Errorf("API error: %s", errMsg)}
		}
		return nil, fmt.Errorf("API error: %s (type: %s)", errMsg, chatResp.Error.Type)
	}

	if len(chatResp.Choices) == 0 {
		return nil, &RetryableError{Err: fmt.Errorf("no choices returned in response")}
	}

	return &chatResp.Choices[0].Message, nil
}

// RetryableError indicates an error that should trigger model fallback
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string {
	return e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// isRetryableError checks if an error should trigger model fallback
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*RetryableError)
	return ok
}

// isRetryableErrorMessage checks common retryable error patterns
func isRetryableErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	retryablePatterns := []string{
		"rate limit",
		"overloaded",
		"capacity",
		"unavailable",
		"timeout",
		"temporarily",
		"too many requests",
		"image",      // Unsupported image input
		"multimodal", // Unsupported multimodal
		"vision",     // Unsupported vision
	}
	for _, pattern := range retryablePatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}
