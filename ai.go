package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// AIClient handles communication with Claude Code
type AIClient struct {
	workspaceDir string
	mcpBinary    string
	dbPath       string
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

// ClaudeEvent represents an event from claude's stream-json output
type ClaudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// For result
	Result     string `json:"result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`

	// For system init
	Model string `json:"model,omitempty"`
}

// NewAIClient creates a new AI client using Claude Code
func NewAIClient(apiKey string, models []string) *AIClient {
	// apiKey and models are ignored - we use Claude Code directly
	return &AIClient{
		workspaceDir: "./workspace",
		mcpBinary:    "./minerva-mcp",
		dbPath:       "./minerva.db",
	}
}

// Chat sends a chat request to Claude Code
func (c *AIClient) Chat(messages []ChatMessage, systemPrompt string, tools []Tool) (*ChatResult, error) {
	// Build the conversation context
	var prompt strings.Builder

	// Add conversation history
	for _, msg := range messages {
		content := extractTextContent(msg.Content)
		switch msg.Role {
		case "user":
			prompt.WriteString(fmt.Sprintf("User: %s\n\n", content))
		case "assistant":
			prompt.WriteString(fmt.Sprintf("Assistant: %s\n\n", content))
		}
	}

	// Get the last user message as the actual prompt
	lastUserMsg := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMsg = extractTextContent(messages[i].Content)
			break
		}
	}

	if lastUserMsg == "" {
		return nil, fmt.Errorf("no user message found")
	}

	// Build MCP config
	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"minerva": map[string]any{
				"command": c.mcpBinary,
				"env": map[string]string{
					"MINERVA_DB": c.dbPath,
				},
			},
		},
	}
	mcpConfigJSON, _ := json.Marshal(mcpConfig)

	// Execute claude
	cmd := exec.Command("claude",
		"-p",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--mcp-config", string(mcpConfigJSON),
		lastUserMsg,
	)
	cmd.Dir = c.workspaceDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude execution failed: %v\nstderr: %s", err, stderr.String())
	}

	// Parse the output
	var result string
	var model string

	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event ClaudeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case "system":
			if event.Model != "" {
				model = event.Model
			}
		case "result":
			result = event.Result
			if event.IsError {
				return nil, fmt.Errorf("claude error: %s", result)
			}
		}
	}

	if result == "" {
		return nil, fmt.Errorf("no result from claude")
	}

	return &ChatResult{
		Message: &ChatMessage{
			Role:    "assistant",
			Content: result,
		},
		Model: model,
	}, nil
}

// extractTextContent extracts text from message content (handles both string and multimodal)
func extractTextContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var texts []string
		for _, part := range v {
			if m, ok := part.(map[string]interface{}); ok {
				if m["type"] == "text" {
					if text, ok := m["text"].(string); ok {
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
