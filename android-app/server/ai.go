package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// chatRequest represents a queued chat request
type chatRequest struct {
	prompt     string
	resultChan chan *chatResponse
}

// chatResponse holds the result of a chat request
type chatResponse struct {
	output string
	err    error
}

// AIClient handles communication with Claude Code CLI
type AIClient struct {
	queue chan *chatRequest
	mu    sync.Mutex
}

// NewAIClient creates a new AI client using Claude Code CLI
func NewAIClient() *AIClient {
	client := &AIClient{
		queue: make(chan *chatRequest, 100),
	}
	go client.processQueue()
	return client
}

// processQueue processes chat requests one at a time
func (c *AIClient) processQueue() {
	for req := range c.queue {
		log.Printf("[AI] Processing queued request (%d in queue)", len(c.queue))
		output, err := executeClaude(req.prompt, "", 5*time.Minute)
		req.resultChan <- &chatResponse{output: output, err: err}
	}
}

// Chat sends a message to Claude Code CLI (queued, one at a time)
func (c *AIClient) Chat(prompt string) (string, error) {
	req := &chatRequest{
		prompt:     prompt,
		resultChan: make(chan *chatResponse, 1),
	}
	c.queue <- req
	resp := <-req.resultChan
	return resp.output, resp.err
}

// executeClaude runs Claude Code CLI and returns the output
func executeClaude(prompt string, workDir string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{
		"-p",
		"--dangerously-skip-permissions",
		"--output-format", "text",
	}

	// Add the prompt
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, "claude", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[AI] Running: claude -p --dangerously-skip-permissions --output-format text %q", truncateStr(prompt, 80))

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude timed out after %v", timeout)
		}
		errMsg := strings.TrimSpace(stderr.String())
		outMsg := strings.TrimSpace(stdout.String())
		log.Printf("[AI] Claude failed: err=%v stderr=%q stdout=%q", err, truncateStr(errMsg, 300), truncateStr(outMsg, 300))
		if errMsg != "" {
			return "", fmt.Errorf("claude error: %w\nstderr: %s", err, errMsg)
		}
		return "", fmt.Errorf("claude error: %w", err)
	}

	output := strings.TrimSpace(stdout.String())
	log.Printf("[AI] Claude response: %s", truncateStr(output, 200))
	return output, nil
}

// executeClaudeWithDir runs Claude Code CLI in a specific directory
func executeClaudeWithDir(prompt string, dir string, timeout time.Duration) (string, error) {
	return executeClaude(prompt, dir, timeout)
}

// CheckClaudeAuth verifies Claude Code authentication works
func CheckClaudeAuth() error {
	_, err := executeClaude("Reply with just 'ok'", "", 30*time.Second)
	return err
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
