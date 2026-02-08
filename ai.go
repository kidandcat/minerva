package main

import (
	"fmt"
	"log"
	"os"
	"strings"
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

// AIClient handles communication with AI APIs
type AIClient struct {
	workspaceDir   string
	queue          chan *chatRequest
	openRouterClient *OpenRouterClient
}

// ChatMessage represents a message in the conversation
type ChatMessage struct {
	Role       string      `json:"role"`                   // "system", "user", "assistant", "tool"
	Content    interface{} `json:"content"`                // string or []ContentPart for multimodal
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`   // Kept for compatibility but unused
	ToolCallID string      `json:"tool_call_id,omitempty"` // Kept for compatibility but unused
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

// ToolCall represents a tool call made by the assistant (kept for compatibility)
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

// Tool represents a tool available to the assistant (kept for compatibility)
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

// NewAIClient creates a new AI client using OpenRouter API
func NewAIClient(apiKey string, models []string) *AIClient {
	workspaceDir := os.Getenv("MINERVA_WORKSPACE")
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}

	client := &AIClient{
		workspaceDir:     workspaceDir,
		queue:            make(chan *chatRequest, 100), // Buffer up to 100 requests
		openRouterClient: NewOpenRouterClient(),
	}

	// Start the queue processor
	go client.processQueue()

	return client
}

// processQueue processes chat requests one at a time
func (c *AIClient) processQueue() {
	for req := range c.queue {
		log.Printf("[AI] Processing queued request (%d in queue)", len(c.queue))

		// Call OpenRouter API
		result, err := c.openRouterClient.Chat(req.messages, req.systemPrompt)

		// Send response back
		if err != nil {
			log.Printf("[AI] OpenRouter error: %v", err)
			req.resultChan <- &chatResponse{err: err}
		} else {
			req.resultChan <- &chatResponse{
				result: &ChatResult{
					Message: &ChatMessage{
						Role:    "assistant",
						Content: result,
					},
					Model: c.openRouterClient.model,
				},
			}
		}
	}
}

// Chat sends a chat completion request using Claude CLI (queued)
func (c *AIClient) Chat(messages []ChatMessage, systemPrompt string, tools []Tool) (*ChatResult, error) {
	log.Printf("[AI] Chat called with %d messages, queueing request", len(messages))

	// Create request with response channel
	req := &chatRequest{
		messages:     messages,
		systemPrompt: systemPrompt,
		tools:        tools,
		resultChan:   make(chan *chatResponse, 1),
	}

	// Queue the request
	c.queue <- req

	// Wait for response
	resp := <-req.resultChan
	return resp.result, resp.err
}

// extractContent gets the text content from a ChatMessage content field
func extractContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		// Handle multimodal content - extract text parts
		var texts []string
		for _, part := range v {
			if partMap, ok := part.(map[string]interface{}); ok {
				if partMap["type"] == "text" {
					if text, ok := partMap["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		return fmt.Sprintf("%v", content)
	}
}
