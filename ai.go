package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// AIClient handles communication with Claude CLI
type AIClient struct {
	workspaceDir string
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

// ClaudeStreamEvent represents an event from Claude CLI's stream-json output
type ClaudeStreamEvent struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	// For result events
	Subtype      string  `json:"subtype,omitempty"`
	Result       string  `json:"result,omitempty"`
	DurationMS   float64 `json:"duration_ms,omitempty"`
	DurationAPI  float64 `json:"duration_api_ms,omitempty"`
	IsError      bool    `json:"is_error,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	TotalCost    float64 `json:"total_cost,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

// NewAIClient creates a new AI client using Claude CLI
func NewAIClient(apiKey string, models []string) *AIClient {
	// apiKey and models are ignored - Claude CLI uses its own config
	workspaceDir := os.Getenv("MINERVA_WORKSPACE")
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}

	return &AIClient{
		workspaceDir: workspaceDir,
	}
}

// Chat sends a chat completion request using Claude CLI
func (c *AIClient) Chat(messages []ChatMessage, systemPrompt string, tools []Tool) (*ChatResult, error) {
	log.Printf("[AI] Chat called with %d messages (using Claude CLI)", len(messages))

	// Build the prompt from conversation history
	prompt := c.buildPrompt(messages, systemPrompt)

	// Execute claude CLI
	result, err := c.executeClaude(prompt)
	if err != nil {
		return nil, err
	}

	return &ChatResult{
		Message: &ChatMessage{
			Role:    "assistant",
			Content: result,
		},
		Model: "claude-cli",
	}, nil
}

// buildPrompt constructs the prompt from messages and system prompt
func (c *AIClient) buildPrompt(messages []ChatMessage, systemPrompt string) string {
	var parts []string

	// Add context header if there's history
	if len(messages) > 1 {
		parts = append(parts, "## Conversation History")
		for _, msg := range messages[:len(messages)-1] {
			content := extractContent(msg.Content)
			if content == "" {
				continue
			}
			switch msg.Role {
			case "user":
				parts = append(parts, fmt.Sprintf("User: %s", content))
			case "assistant":
				parts = append(parts, fmt.Sprintf("Assistant: %s", content))
			case "system":
				// Skip system messages in history, they're handled separately
			}
		}
		parts = append(parts, "\n## Current Request")
	}

	// Add the last user message as the main prompt
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1]
		content := extractContent(lastMsg.Content)
		parts = append(parts, content)
	}

	return strings.Join(parts, "\n")
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

// executeClaude runs the claude CLI and returns the result
func (c *AIClient) executeClaude(prompt string) (string, error) {
	// Ensure workspace directory exists
	if err := os.MkdirAll(c.workspaceDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create workspace directory: %w", err)
	}

	args := []string{
		"-p",
		"--continue",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		prompt,
	}

	log.Printf("[AI] Executing claude CLI in %s", c.workspaceDir)

	cmd := exec.Command("claude", args...)
	cmd.Dir = c.workspaceDir

	// Get stdout pipe for streaming output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	// Capture stderr for debugging
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start claude CLI: %w", err)
	}

	// Read stderr in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[Claude stderr] %s", scanner.Text())
		}
	}()

	// Parse stream-json output
	var result string
	scanner := bufio.NewScanner(stdout)
	// Increase buffer size for large outputs
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event ClaudeStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("[AI] Failed to parse event: %s", line)
			continue
		}

		// Look for the result event
		if event.Type == "result" {
			result = event.Result
			log.Printf("[AI] Got result (cost: $%.4f, tokens: %d in / %d out)",
				event.TotalCost, event.InputTokens, event.OutputTokens)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[AI] Scanner error: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		// If we got a result, don't fail even if exit code is non-zero
		if result != "" {
			log.Printf("[AI] Claude exited with error but got result: %v", err)
		} else {
			return "", fmt.Errorf("claude CLI failed: %w", err)
		}
	}

	if result == "" {
		return "", fmt.Errorf("no result received from claude CLI")
	}

	return result, nil
}
