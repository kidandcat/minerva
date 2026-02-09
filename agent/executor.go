package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"
)

// ExecutionResult holds the result of a claude execution
type ExecutionResult struct {
	Output     string
	ExitCode   int
	DurationMs int64
}

// Executor runs claude commands
type Executor struct{}

// NewExecutor creates a new executor
func NewExecutor() *Executor {
	return &Executor{}
}

// Start launches a claude process and returns immediately after verifying it started.
// The caller receives stdout/stderr buffers and the cmd to wait on.
func (e *Executor) Start(ctx context.Context, prompt, workDir string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer, error) {
	cmd := exec.CommandContext(ctx,
		"claude",
		"-p",
		"--dangerously-skip-permissions",
		"--model", "opus",
		"--append-system-prompt", "IMPORTANT: You are running in non-interactive mode. If you need clarification or have questions, DO NOT use AskUserQuestion (it will block). Instead, list all your questions in your response text and end your execution. The user will provide answers and run you again.",
		prompt,
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[Executor] Starting claude in %s", workDir)
	log.Printf("[Executor] Prompt: %s", truncate(prompt, 200))

	if err := cmd.Start(); err != nil {
		log.Printf("[Executor] Failed to start claude: %v", err)
		return nil, nil, nil, fmt.Errorf("failed to start claude: %w", err)
	}

	log.Printf("[Executor] Claude started (PID: %d)", cmd.Process.Pid)
	return cmd, &stdout, &stderr, nil
}

// Wait waits for a started command and returns the result
func (e *Executor) Wait(cmd *exec.Cmd, stdout, stderr *bytes.Buffer, start time.Time) *ExecutionResult {
	err := cmd.Wait()
	elapsed := time.Since(start)

	result := &ExecutionResult{
		Output:     stdout.String(),
		DurationMs: elapsed.Milliseconds(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			log.Printf("[Executor] Claude exited with code %d after %v", result.ExitCode, elapsed)
			if stderr.Len() > 0 {
				log.Printf("[Executor] Stderr: %s", truncate(stderr.String(), 500))
				result.Output = fmt.Sprintf("%s\n\nStderr:\n%s", result.Output, stderr.String())
			}
		} else {
			log.Printf("[Executor] Claude wait error: %v", err)
			result.ExitCode = 1
			result.Output = fmt.Sprintf("claude error: %v", err)
		}
	} else {
		log.Printf("[Executor] Claude completed successfully in %v (output: %d bytes)", elapsed, len(result.Output))
	}

	return result
}
