package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
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

// AIClient handles communication with Claude CLI
type AIClient struct {
	workspaceDir string
	queue        chan *chatRequest
}

// ChatMessage represents a message in the conversation
type ChatMessage struct {
	Role       string      `json:"role"`                   // "system", "user", "assistant", "tool"
	Content    interface{} `json:"content"`                // string or []ContentPart for multimodal
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`   // Kept for DB compatibility
	ToolCallID string      `json:"tool_call_id,omitempty"` // Kept for DB compatibility
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

// NewAIClient creates a new AI client using Claude CLI
func NewAIClient() *AIClient {
	workspaceDir := os.Getenv("MINERVA_WORKSPACE")
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}

	client := &AIClient{
		workspaceDir: workspaceDir,
		queue:        make(chan *chatRequest, 100),
	}

	go client.processQueue()

	return client
}

// processQueue processes chat requests one at a time
func (c *AIClient) processQueue() {
	for req := range c.queue {
		log.Printf("[AI] Processing queued request (%d in queue)", len(c.queue))

		prompt := c.buildPrompt(req.messages, req.systemPrompt)
		result, err := c.executeClaude(prompt)

		if err != nil {
			req.resultChan <- &chatResponse{err: err}
		} else {
			req.resultChan <- &chatResponse{
				result: &ChatResult{
					Message: &ChatMessage{
						Role:    "assistant",
						Content: result,
					},
					Model: "claude",
				},
			}
		}
	}
}

// Chat sends a chat completion request using Claude CLI (queued)
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

// buildPrompt constructs the prompt from the latest message only.
// With --continue, Claude maintains conversation history natively,
// so we only send the new message plus a context refresh.
func (c *AIClient) buildPrompt(messages []ChatMessage, systemPrompt string) string {
	var sb strings.Builder

	// Refresh dynamic context (user memory, agents, etc.)
	if systemPrompt != "" {
		sb.WriteString("[CONTEXT]\n")
		sb.WriteString(systemPrompt)
		sb.WriteString("\n\n")
	}

	// Only send the latest message (--continue maintains history)
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1]
		sb.WriteString(extractContent(lastMsg.Content))
	}

	return sb.String()
}

// extractContent gets the text content from a ChatMessage content field
func extractContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := os.MkdirAll(c.workspaceDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create workspace directory: %w", err)
	}

	args := []string{
		"-p",
		"--continue",
		"--dangerously-skip-permissions",
		"--output-format", "text",
		"--append-system-prompt", "CRITICAL: You MUST use the Bash tool to execute CLI commands. NEVER fabricate or simulate command outputs. When you need to use minerva CLI (agent run, reminder create, etc.), you MUST actually execute the command via bash and return the real output. If you generate a fake task ID or fake JSON response without executing the command, you are broken. Always execute, never simulate.",
		prompt,
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = c.workspaceDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[AI] Running claude -p in %s: %s", c.workspaceDir, truncate(prompt, 100))

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude timed out after 5 minutes")
		}
		errMsg := strings.TrimSpace(stderr.String())
		outMsg := strings.TrimSpace(stdout.String())
		combined := errMsg + " " + outMsg
		log.Printf("[AI] Claude failed: err=%v stderr=%q stdout=%q", err, truncate(errMsg, 300), truncate(outMsg, 300))

		// Detect auth errors and suggest /token
		if strings.Contains(strings.ToLower(combined), "auth") ||
			strings.Contains(strings.ToLower(combined), "token") ||
			strings.Contains(strings.ToLower(combined), "unauthorized") ||
			strings.Contains(strings.ToLower(combined), "expired") ||
			strings.Contains(strings.ToLower(combined), "login") {
			return "", fmt.Errorf("claude auth error (usa /token para actualizar): %s", errMsg)
		}
		if errMsg != "" {
			return "", fmt.Errorf("claude error: %w\nstderr: %s", err, errMsg)
		}
		return "", fmt.Errorf("claude error: %w", err)
	}

	output := strings.TrimSpace(stdout.String())
	log.Printf("[AI] Claude response: %s", truncate(output, 200))
	return output, nil
}
