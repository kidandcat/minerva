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

var TasksDir = filepath.Join(os.Getenv("HOME"), "minerva-tasks")

const TaskTimeout = 24 * time.Hour

// TaskRunner manages background tasks
type TaskRunner struct {
	bot *Bot
}

func NewTaskRunner(bot *Bot) *TaskRunner {
	os.MkdirAll(TasksDir, 0755)
	return &TaskRunner{bot: bot}
}

// LaunchTaskAsync launches a task in the background
func (tr *TaskRunner) LaunchTaskAsync(userID int64, description string) (string, error) {
	taskID := fmt.Sprintf("task_%d", time.Now().UnixNano())
	taskDir := filepath.Join(TasksDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create task directory: %w", err)
	}

	// Write TASK.md
	taskContent := fmt.Sprintf("# Task\n\n%s\n\n---\n\n## Instructions\n\n- Work on this task until completion\n- Update PROGRESS.md regularly\n- Create any files you need\n", description)
	os.WriteFile(filepath.Join(taskDir, "TASK.md"), []byte(taskContent), 0644)
	os.WriteFile(filepath.Join(taskDir, "PROGRESS.md"), []byte("# Progress\n\nStarting task...\n"), 0644)

	cmd := exec.Command("claude", "--dangerously-skip-permissions", "-p",
		"Read TASK.md and complete the task. Update PROGRESS.md with your progress regularly. "+
			"Work until the task is complete.")
	cmd.Dir = taskDir

	outputFile := filepath.Join(taskDir, "OUTPUT.txt")
	outFile, err := os.Create(outputFile)
	if err != nil {
		return "", err
	}
	cmd.Stdout = outFile
	cmd.Stderr = outFile

	if err := cmd.Start(); err != nil {
		outFile.Close()
		return "", err
	}

	pid := cmd.Process.Pid
	outFile.Close()

	tr.bot.db.CreateTask(taskID, userID, description, pid)
	go tr.monitorTask(cmd, taskID, userID, taskDir)

	return taskID, nil
}

func (tr *TaskRunner) monitorTask(cmd *exec.Cmd, taskID string, userID int64, taskDir string) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		output, _ := os.ReadFile(filepath.Join(taskDir, "OUTPUT.txt"))
		outputStr := string(output)
		if err != nil {
			tr.bot.db.UpdateTaskStatus(taskID, "failed", outputStr)
			tr.bot.sendMessage(userID, fmt.Sprintf("Task failed\nID: %s\n\n%s", taskID, truncateOutput(outputStr, 2000)))
		} else {
			tr.bot.db.UpdateTaskStatus(taskID, "completed", outputStr)
			tr.bot.sendMessage(userID, fmt.Sprintf("Task completed\nID: %s\n\n%s", taskID, truncateOutput(outputStr, 3000)))
		}
	case <-time.After(TaskTimeout):
		cmd.Process.Kill()
		tr.bot.db.UpdateTaskStatus(taskID, "failed", "Timed out after 24 hours")
		tr.bot.sendMessage(userID, fmt.Sprintf("Task timed out\nID: %s", taskID))
	}
}

func (tr *TaskRunner) GetTaskProgress(taskID string) (string, error) {
	content, err := os.ReadFile(filepath.Join(TasksDir, taskID, "PROGRESS.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "No progress file found", nil
		}
		return "", err
	}
	return string(content), nil
}

func (tr *TaskRunner) CancelTask(taskID string) error {
	task, err := tr.bot.db.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("task not found: %w", err)
	}
	if task.Status != "running" {
		return fmt.Errorf("task is not running (status: %s)", task.Status)
	}
	if task.PID > 0 {
		if p, err := os.FindProcess(task.PID); err == nil {
			p.Signal(syscall.SIGTERM)
			time.Sleep(2 * time.Second)
			p.Kill()
		}
	}
	tr.bot.db.UpdateTaskStatus(taskID, "cancelled", "Cancelled by user")
	return nil
}

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
	if _, err := os.Stat(taskDir); os.IsNotExist(err) {
		return fmt.Errorf("task directory not found")
	}

	cmd := exec.Command("claude", "--dangerously-skip-permissions", "-p",
		"Read TASK.md and PROGRESS.md. Continue where you left off. Update PROGRESS.md. Work until complete.")
	cmd.Dir = taskDir

	outputFile := filepath.Join(taskDir, "OUTPUT.txt")
	outFile, _ := os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	outFile.WriteString("\n\n--- RESUMED ---\n\n")
	cmd.Stdout = outFile
	cmd.Stderr = outFile

	if err := cmd.Start(); err != nil {
		outFile.Close()
		return err
	}

	pid := cmd.Process.Pid
	outFile.Close()

	tr.bot.db.UpdateTaskPID(taskID, pid)
	tr.bot.db.UpdateTaskStatus(taskID, "running", "")
	go tr.monitorTask(cmd, taskID, userID, taskDir)

	return nil
}

func (tr *TaskRunner) IsTaskRunning(taskID string) (bool, error) {
	task, err := tr.bot.db.GetTask(taskID)
	if err != nil {
		return false, err
	}
	if task.Status != "running" || task.PID <= 0 {
		return false, nil
	}
	p, err := os.FindProcess(task.PID)
	if err != nil {
		return false, nil
	}
	return p.Signal(syscall.Signal(0)) == nil, nil
}

func truncateOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	truncated := s[:n]
	if idx := strings.LastIndex(truncated, "\n"); idx > n/2 {
		truncated = truncated[:idx]
	}
	return truncated + "\n\n... (truncated)"
}
