package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ExecutionResult holds the result of a claude execution
type ExecutionResult struct {
	Output     string
	ExitCode   int
	DurationMs int64
}

// Executor runs claude commands
type Executor struct {
	outputDirs sync.Map // taskID -> output directory path
}

// NewExecutor creates a new executor
func NewExecutor() *Executor {
	return &Executor{}
}

// GetOutputDir returns the output directory for a task, if one was created
func (e *Executor) GetOutputDir(taskID string) string {
	if v, ok := e.outputDirs.LoadAndDelete(taskID); ok {
		return v.(string)
	}
	return ""
}

// Start launches a claude process and returns immediately after verifying it started.
// The caller receives stdout/stderr buffers and the cmd to wait on.
func (e *Executor) Start(ctx context.Context, taskID, prompt, workDir string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer, error) {
	// Create output directory for file transfers
	outputDir := filepath.Join(os.TempDir(), fmt.Sprintf("minerva-output-%s", taskID))
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Printf("[Executor] Failed to create output dir %s: %v", outputDir, err)
		// Non-fatal: continue without output dir
		outputDir = ""
	} else {
		e.outputDirs.Store(taskID, outputDir)
	}

	appendPrompt := "IMPORTANT: You are running in non-interactive mode. If you need clarification or have questions, DO NOT use AskUserQuestion (it will block). Instead, list all your questions in your response text and end your execution. The user will provide answers and run you again."
	if outputDir != "" {
		appendPrompt += fmt.Sprintf("\n\nFILE OUTPUT: If you need to send files to the user, save them in the directory $MINERVA_OUTPUT_DIR (%s). Any files placed there will be automatically sent to the user via Telegram when the task completes.", outputDir)
	}

	cmd := exec.CommandContext(ctx,
		"claude",
		"-p",
		"--dangerously-skip-permissions",
		"--model", "opus",
		"--append-system-prompt", appendPrompt,
		prompt,
	)
	cmd.Dir = workDir

	// Set MINERVA_OUTPUT_DIR environment variable
	cmd.Env = append(os.Environ(), fmt.Sprintf("MINERVA_OUTPUT_DIR=%s", outputDir))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[Executor] Starting claude in %s", workDir)
	log.Printf("[Executor] Prompt: %s", truncate(prompt, 200))
	if outputDir != "" {
		log.Printf("[Executor] Output dir: %s", outputDir)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[Executor] Failed to start claude: %v", err)
		// Clean up output dir on failure
		if outputDir != "" {
			os.RemoveAll(outputDir)
			e.outputDirs.Delete(taskID)
		}
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
