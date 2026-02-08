package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// chatRequest represents a queued chat request
type chatRequest struct {
	messages     []ChatMessage
	systemPrompt string
	tools        []Tool
	resultChan   chan *chatResponse
}

// chatResponse holds the result of a chat request
type chatResponse struct {
	result *ChatResult
	err    error
}

// AIClient handles communication with OpenRouter API
type AIClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	queue      chan *chatRequest
}

// ChatMessage represents a message in the conversation
type ChatMessage struct {
	Role       string      `json:"role"`                   // "system", "user", "assistant", "tool"
	Content    interface{} `json:"content"`                // string or []ContentPart for multimodal
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`   // Tool calls from assistant
	ToolCallID string      `json:"tool_call_id,omitempty"` // For tool response messages
}

// ContentPart represents a part of multimodal content
type ContentPart struct {
	Type     string    `json:"type"` // "text" or "image_url"
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

// ChatResult contains the response and metadata
type ChatResult struct {
	Message *ChatMessage
	Model   string
}

// OpenRouterRequest is the request body for OpenRouter API
type OpenRouterRequest struct {
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	Tools      []Tool        `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"` // "auto", "none", or specific
}

// OpenRouterResponse is the response from OpenRouter API
type OpenRouterResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"` // "stop", "tool_calls", "length"
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// NewAIClient creates a new AI client using OpenRouter API
func NewAIClient(apiKey string, models []string) *AIClient {
	model := "x-ai/grok-4.1-fast"
	if len(models) > 0 && models[0] != "" {
		model = models[0]
	}

	client := &AIClient{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		queue: make(chan *chatRequest, 100),
	}

	// Start the queue processor
	go client.processQueue()

	return client
}

// processQueue processes chat requests one at a time
func (c *AIClient) processQueue() {
	for req := range c.queue {
		log.Printf("[AI] Processing queued request (%d in queue)", len(c.queue))

		result, err := c.callOpenRouter(req.messages, req.systemPrompt, req.tools)

		if err != nil {
			req.resultChan <- &chatResponse{err: err}
		} else {
			req.resultChan <- &chatResponse{result: result}
		}
	}
}

// Chat sends a chat completion request using OpenRouter API (queued)
func (c *AIClient) Chat(messages []ChatMessage, systemPrompt string, tools []Tool) (*ChatResult, error) {
	log.Printf("[AI] Chat called with %d messages, queueing request", len(messages))

	req := &chatRequest{
		messages:     messages,
		systemPrompt: systemPrompt,
		tools:        tools,
		resultChan:   make(chan *chatResponse, 1),
	}

	c.queue <- req

	resp := <-req.resultChan
	return resp.result, resp.err
}

// callOpenRouter makes the actual API call to OpenRouter
func (c *AIClient) callOpenRouter(messages []ChatMessage, systemPrompt string, tools []Tool) (*ChatResult, error) {
	// Build messages array with system prompt first
	var apiMessages []ChatMessage

	if systemPrompt != "" {
		apiMessages = append(apiMessages, ChatMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	// Add conversation messages
	apiMessages = append(apiMessages, messages...)

	// Build request
	reqBody := OpenRouterRequest{
		Model:    c.model,
		Messages: apiMessages,
	}

	if len(tools) > 0 {
		reqBody.Tools = tools
		reqBody.ToolChoice = "auto"
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Printf("[AI] Calling OpenRouter with model %s, %d messages, %d tools",
		c.model, len(apiMessages), len(tools))

	// Create HTTP request
	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("HTTP-Referer", "https://home.jairo.cloud")
	req.Header.Set("X-Title", "Minerva")

	// Make request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var apiResp OpenRouterResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w (body: %s)", err, string(body))
	}

	// Check for API error
	if apiResp.Error != nil {
		return nil, fmt.Errorf("API error: %s (type: %s, code: %s)",
			apiResp.Error.Message, apiResp.Error.Type, apiResp.Error.Code)
	}

	// Check for valid response
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response: %s", string(body))
	}

	choice := apiResp.Choices[0]
	log.Printf("[AI] Response received: finish_reason=%s, tokens=%d/%d, tool_calls=%d",
		choice.FinishReason,
		apiResp.Usage.PromptTokens,
		apiResp.Usage.CompletionTokens,
		len(choice.Message.ToolCalls))

	return &ChatResult{
		Message: &choice.Message,
		Model:   apiResp.Model,
	}, nil
}
