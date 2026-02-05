package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	TasksDir    = "/home/ubuntu/minerva-tasks"
	TaskTimeout = 24 * time.Hour
)

// TaskRunner manages background tasks
type TaskRunner struct {
	bot *Bot
}

// NewTaskRunner creates a new task runner
func NewTaskRunner(bot *Bot) *TaskRunner {
	return &TaskRunner{bot: bot}
}

// CreateTask creates a new task and launches Claude Code
func (tr *TaskRunner) CreateTask(userID int64, description string) (string, error) {
	// Generate task ID
	taskID := fmt.Sprintf("task_%d", time.Now().UnixNano())

	// Create task directory
	taskDir := filepath.Join(TasksDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create task directory: %w", err)
	}

	// Write TASK.md
	taskFile := filepath.Join(taskDir, "TASK.md")
	taskContent := fmt.Sprintf(`# Task

%s

---

## Instructions

- Work on this task until completion
- Update PROGRESS.md regularly with your progress
- Create any files you need in this directory
- When done, your final output will be sent to the user
`, description)

	if err := os.WriteFile(taskFile, []byte(taskContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write TASK.md: %w", err)
	}

	// Create empty PROGRESS.md
	progressFile := filepath.Join(taskDir, "PROGRESS.md")
	if err := os.WriteFile(progressFile, []byte("# Progress\n\nStarting task...\n"), 0644); err != nil {
		return "", fmt.Errorf("failed to write PROGRESS.md: %w", err)
	}

	// Launch Claude Code
	cmd := exec.Command("claude", "--dangerously-skip-permissions", "-p",
		"Lee TASK.md y completa la tarea. Actualiza PROGRESS.md con tu progreso regularmente. "+
			"Tienes libertad total en esta carpeta para crear archivos, buscar en internet, hacer llamadas, etc. "+
			"Trabaja hasta completar la tarea.")
	cmd.Dir = taskDir

	// Capture output
	output, err := tr.runWithTimeout(cmd, taskID, userID)
	if err != nil {
		// Save error to DB
		tr.bot.db.UpdateTaskStatus(taskID, "failed", err.Error())
		return taskID, nil // Return taskID even on failure so user can check
	}

	// Save completed task
	tr.bot.db.UpdateTaskStatus(taskID, "completed", output)

	// Notify user
	summary := output
	if len(summary) > 3000 {
		summary = summary[:3000] + "\n\n... (truncado)"
	}
	tr.bot.sendMessage(userID, fmt.Sprintf("✅ *Tarea completada*\nID: `%s`\n\n%s", taskID, summary))

	return taskID, nil
}

// runWithTimeout runs the command with a timeout and monitors progress
func (tr *TaskRunner) runWithTimeout(cmd *exec.Cmd, taskID string, userID int64) (string, error) {
	// Start the command
	outputBytes, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude exited with error: %s", string(exitErr.Stderr))
		}
		return "", err
	}

	return string(outputBytes), nil
}

// LaunchTaskAsync launches a task in the background
func (tr *TaskRunner) LaunchTaskAsync(userID int64, description string) (string, error) {
	// Generate task ID
	taskID := fmt.Sprintf("task_%d", time.Now().UnixNano())

	// Create task directory
	taskDir := filepath.Join(TasksDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create task directory: %w", err)
	}

	// Write TASK.md
	taskFile := filepath.Join(taskDir, "TASK.md")
	taskContent := fmt.Sprintf(`# Task

%s

---

## Instructions

- Work on this task until completion
- Update PROGRESS.md regularly with your progress
- Create any files you need in this directory
- When done, your final output will be sent to the user
`, description)

	if err := os.WriteFile(taskFile, []byte(taskContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write TASK.md: %w", err)
	}

	// Create empty PROGRESS.md
	progressFile := filepath.Join(taskDir, "PROGRESS.md")
	if err := os.WriteFile(progressFile, []byte("# Progress\n\nStarting task...\n"), 0644); err != nil {
		return "", fmt.Errorf("failed to write PROGRESS.md: %w", err)
	}

	// Launch Claude Code in background with permissions
	cmd := exec.Command("claude", "--dangerously-skip-permissions", "-p",
		"Lee TASK.md y completa la tarea. Actualiza PROGRESS.md con tu progreso regularmente. "+
			"Tienes libertad total en esta carpeta para crear archivos, buscar en internet, hacer llamadas, etc. "+
			"Trabaja hasta completar la tarea.")
	cmd.Dir = taskDir

	// Create output file for capturing stdout
	outputFile := filepath.Join(taskDir, "OUTPUT.txt")
	outFile, err := os.Create(outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	cmd.Stdout = outFile
	cmd.Stderr = outFile

	// Start the process
	if err := cmd.Start(); err != nil {
		outFile.Close()
		return "", fmt.Errorf("failed to start claude: %w", err)
	}

	pid := cmd.Process.Pid
	outFile.Close()

	// Save task to DB
	if err := tr.bot.db.CreateTask(taskID, userID, description, pid); err != nil {
		cmd.Process.Kill()
		return "", fmt.Errorf("failed to save task: %w", err)
	}

	// Monitor in background
	go tr.monitorTask(cmd, taskID, userID, taskDir)

	return taskID, nil
}

// monitorTask waits for task completion and notifies user
func (tr *TaskRunner) monitorTask(cmd *exec.Cmd, taskID string, userID int64, taskDir string) {
	// Wait for process with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		// Read output
		outputFile := filepath.Join(taskDir, "OUTPUT.txt")
		output, _ := os.ReadFile(outputFile)
		outputStr := string(output)

		if err != nil {
			tr.bot.db.UpdateTaskStatus(taskID, "failed", outputStr)
			tr.bot.sendMessage(userID, fmt.Sprintf("❌ *Tarea fallida*\nID: `%s`\n\n%s", taskID, truncateOutput(outputStr, 2000)))
		} else {
			tr.bot.db.UpdateTaskStatus(taskID, "completed", outputStr)
			tr.bot.sendMessage(userID, fmt.Sprintf("✅ *Tarea completada*\nID: `%s`\n\n%s", taskID, truncateOutput(outputStr, 3000)))
		}

	case <-time.After(TaskTimeout):
		cmd.Process.Kill()
		tr.bot.db.UpdateTaskStatus(taskID, "failed", "Task timed out after 24 hours")
		tr.bot.sendMessage(userID, fmt.Sprintf("⏰ *Tarea cancelada por timeout*\nID: `%s`", taskID))
	}
}

// GetTaskProgress reads the PROGRESS.md file for a task
func (tr *TaskRunner) GetTaskProgress(taskID string) (string, error) {
	progressFile := filepath.Join(TasksDir, taskID, "PROGRESS.md")
	content, err := os.ReadFile(progressFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "No progress file found", nil
		}
		return "", err
	}
	return string(content), nil
}

// CancelTask cancels a running task
func (tr *TaskRunner) CancelTask(taskID string) error {
	task, err := tr.bot.db.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("task not found: %w", err)
	}

	if task.Status != "running" {
		return fmt.Errorf("task is not running (status: %s)", task.Status)
	}

	// Kill the process
	if task.PID > 0 {
		process, err := os.FindProcess(task.PID)
		if err == nil {
			process.Signal(syscall.SIGTERM)
			time.Sleep(2 * time.Second)
			process.Kill()
		}
	}

	tr.bot.db.UpdateTaskStatus(taskID, "cancelled", "Cancelled by user")
	return nil
}

// ListTasks returns running tasks for a user
func (tr *TaskRunner) ListTasks(userID int64) ([]Task, error) {
	return tr.bot.db.GetUserTasks(userID)
}

// ResumeTask resumes a failed task, keeping existing progress
func (tr *TaskRunner) ResumeTask(taskID string, userID int64) error {
	task, err := tr.bot.db.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("task not found: %w", err)
	}

	if task.UserID != userID {
		return fmt.Errorf("task does not belong to you")
	}

	if task.Status == "running" {
		return fmt.Errorf("task is already running")
	}

	taskDir := filepath.Join(TasksDir, taskID)

	// Check if task directory exists
	if _, err := os.Stat(taskDir); os.IsNotExist(err) {
		return fmt.Errorf("task directory not found")
	}

	// Launch Claude Code again
	cmd := exec.Command("claude", "--dangerously-skip-permissions", "-p",
		"Lee TASK.md y PROGRESS.md. Continúa donde lo dejaste. "+
			"Actualiza PROGRESS.md con tu progreso. "+
			"Trabaja hasta completar la tarea.")
	cmd.Dir = taskDir

	// Create output file for capturing stdout (append mode)
	outputFile := filepath.Join(taskDir, "OUTPUT.txt")
	outFile, err := os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open output file: %w", err)
	}
	outFile.WriteString("\n\n--- RESUMED ---\n\n")
	cmd.Stdout = outFile
	cmd.Stderr = outFile

	// Start the process
	if err := cmd.Start(); err != nil {
		outFile.Close()
		return fmt.Errorf("failed to start claude: %w", err)
	}

	pid := cmd.Process.Pid
	outFile.Close()

	// Update task status
	tr.bot.db.UpdateTaskPID(taskID, pid)
	tr.bot.db.UpdateTaskStatus(taskID, "running", "")

	// Monitor in background
	go tr.monitorTask(cmd, taskID, userID, taskDir)

	return nil
}

// IsTaskRunning checks if a task's process is still running
func (tr *TaskRunner) IsTaskRunning(taskID string) (bool, error) {
	task, err := tr.bot.db.GetTask(taskID)
	if err != nil {
		return false, err
	}

	if task.Status != "running" {
		return false, nil
	}

	if task.PID <= 0 {
		return false, nil
	}

	// Check if process exists
	process, err := os.FindProcess(task.PID)
	if err != nil {
		return false, nil
	}

	// Send signal 0 to check if process exists
	err = process.Signal(syscall.Signal(0))
	return err == nil, nil
}

func truncateOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Find last newline before truncation point
	truncated := s[:n]
	if idx := strings.LastIndex(truncated, "\n"); idx > n/2 {
		truncated = truncated[:idx]
	}
	return truncated + "\n\n... (truncado)"
}
