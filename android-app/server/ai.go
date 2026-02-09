package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
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

// claudeBin is the resolved path to the claude CLI binary
var claudeBin string

func init() {
	claudeBin = findClaudeBin()
}

// findClaudeBin locates the claude binary, checking common Termux/npm paths
func findClaudeBin() string {
	// Try PATH first
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}

	// Common Termux npm global install paths
	home := os.Getenv("HOME")
	candidates := []string{
		home + "/.npm-global/bin/claude",
		home + "/node_modules/.bin/claude",
		"/data/data/com.termux/files/usr/bin/claude",
		"/data/data/com.termux/files/home/.npm-global/bin/claude",
		"/data/data/com.termux/files/usr/local/bin/claude",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			log.Printf("[AI] Found claude at %s", p)
			return p
		}
	}

	// Fallback — let exec fail with a clear error
	return "claude"
}

// AIClient handles communication with Claude Code CLI
type AIClient struct {
	queue        chan *chatRequest
	mu           sync.Mutex
	workspaceDir string
}

// NewAIClient creates a new AI client using Claude Code CLI
func NewAIClient() *AIClient {
	workspaceDir := os.Getenv("MINERVA_WORKSPACE")
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}
	client := &AIClient{
		queue:        make(chan *chatRequest, 100),
		workspaceDir: workspaceDir,
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

	cmd := exec.CommandContext(ctx, claudeBin, args...)
	if workDir != "" {
		cmd.Dir = workDir
	} else {
		// Default to workspace directory so Claude reads CLAUDE.md
		wsDir := os.Getenv("MINERVA_WORKSPACE")
		if wsDir == "" {
			wsDir = "./workspace"
		}
		_ = os.MkdirAll(wsDir, 0755)
		cmd.Dir = wsDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[AI] Running: %s -p --dangerously-skip-permissions --output-format text %q", claudeBin, truncateStr(prompt, 80))

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
