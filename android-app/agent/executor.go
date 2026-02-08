package main

import (
	"bytes"
	"context"
	"fmt"
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

// Run executes a claude prompt and returns the result
func (e *Executor) Run(prompt, workDir string, timeout time.Duration) (*ExecutionResult, error) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"claude",
		"-p",
		"--continue",
		"--dangerously-skip-permissions",
		"--model", "opus",
		"--output-format", "text",
		"--append-system-prompt", "IMPORTANT: You are running in non-interactive mode. If you need clarification or have questions, DO NOT use AskUserQuestion (it will block). Instead, list all your questions in your response text and end your execution. The user will provide answers and run you again.",
		prompt,
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &ExecutionResult{
		Output:     stdout.String(),
		DurationMs: time.Since(start).Milliseconds(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			// Append stderr to output if there was an error
			if stderr.Len() > 0 {
				result.Output = fmt.Sprintf("%s\n\nStderr:\n%s", result.Output, stderr.String())
			}
		} else {
			return nil, fmt.Errorf("failed to run claude: %w", err)
		}
	}

	return result, nil
}
