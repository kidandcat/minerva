package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"minerva/tools"
)

// ToolExecutor handles tool execution with user context
type ToolExecutor struct {
	db     *sql.DB
	userID int64
}

// NewToolExecutor creates a new tool executor for a user
func NewToolExecutor(db *sql.DB, userID int64) *ToolExecutor {
	return &ToolExecutor{
		db:     db,
		userID: userID,
	}
}

// Execute runs a tool by name with the given arguments
func (t *ToolExecutor) Execute(name string, arguments string, bot *Bot) (string, error) {
	switch name {
	case "create_reminder":
		return tools.CreateReminder(t.db, t.userID, arguments)
	case "list_reminders":
		return tools.ListReminders(t.db, t.userID)
	case "delete_reminder":
		return tools.DeleteReminder(t.db, t.userID, arguments)
	case "update_memory":
		return tools.UpdateMemory(t.db, t.userID, arguments)
	case "run_code":
		return tools.RunCode(arguments)
	case "send_email":
		return tools.SendEmail(arguments)
	case "make_call":
		return executeCall(bot, t.userID, arguments)
	case "create_task":
		return executeCreateTask(bot, t.userID, arguments)
	case "get_task_progress":
		return executeGetTaskProgress(bot, arguments)
	case "list_agents", "run_claude_task":
		if bot.agentHub == nil {
			return "", fmt.Errorf("agent hub not configured")
		}
		return ExecuteAgentTool(bot.agentHub, name, arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// executeCall handles the make_call tool
func executeCall(bot *Bot, userID int64, arguments string) (string, error) {
	if bot.twilioManager == nil {
		return "", fmt.Errorf("voice calls not configured")
	}

	var args struct {
		PhoneNumber string `json:"phone_number"`
		Purpose     string `json:"purpose"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.PhoneNumber == "" {
		return "", fmt.Errorf("phone_number is required")
	}
	if args.Purpose == "" {
		return "", fmt.Errorf("purpose is required")
	}

	// Get conversation ID for the user
	user, _, err := bot.db.GetOrCreateUser(userID, "", "")
	if err != nil {
		return "", fmt.Errorf("failed to get user: %w", err)
	}
	conv, err := bot.db.GetActiveConversation(user.ID)
	if err != nil {
		return "", fmt.Errorf("failed to get conversation: %w", err)
	}

	callSID, err := bot.twilioManager.InitiateCall(args.PhoneNumber, args.Purpose, userID, conv.ID)
	if err != nil {
		return "", fmt.Errorf("failed to initiate call: %w", err)
	}

	return fmt.Sprintf("Call initiated successfully. Call ID: %s. I will keep you updated on the call progress.", callSID), nil
}

// executeCreateTask handles the create_task tool
func executeCreateTask(bot *Bot, userID int64, arguments string) (string, error) {
	if bot.taskRunner == nil {
		return "", fmt.Errorf("task runner not configured")
	}

	var args struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.Description == "" {
		return "", fmt.Errorf("description is required")
	}

	taskID, err := bot.taskRunner.LaunchTaskAsync(userID, args.Description)
	if err != nil {
		return "", fmt.Errorf("failed to create task: %w", err)
	}

	return fmt.Sprintf("Task created successfully. Task ID: %s. Claude Code is now working on it. The user will be notified when it completes. They can check progress with /tasks or ask you to check using get_task_progress.", taskID), nil
}

// executeGetTaskProgress handles the get_task_progress tool
func executeGetTaskProgress(bot *Bot, arguments string) (string, error) {
	if bot.taskRunner == nil {
		return "", fmt.Errorf("task runner not configured")
	}

	var args struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.TaskID == "" {
		return "", fmt.Errorf("task_id is required")
	}

	// Get task status from DB
	task, err := bot.db.GetTask(args.TaskID)
	if err != nil {
		return "", fmt.Errorf("task not found: %w", err)
	}

	// Get progress file content
	progress, _ := bot.taskRunner.GetTaskProgress(args.TaskID)

	result := fmt.Sprintf("Task: %s\nStatus: %s\nCreated: %s\n\n--- PROGRESS.md ---\n%s",
		task.Description, task.Status, task.CreatedAt.Format("2006-01-02 15:04"), progress)

	if task.Status == "completed" || task.Status == "failed" {
		result += fmt.Sprintf("\n\n--- OUTPUT ---\n%s", task.Output)
	}

	return result, nil
}

// GetToolDefinitions returns all available tool definitions for the AI
func GetToolDefinitions() []Tool {
	return []Tool{
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "create_reminder",
				Description: "Create a reminder for the user at a specific time. Use target='user' to send directly to the user, or target='ai' to send to you (AI) for processing (useful for tasks that need AI action).",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"message": map[string]any{
							"type":        "string",
							"description": "The reminder message",
						},
						"remind_at": map[string]any{
							"type":        "string",
							"description": "When to send the reminder in ISO8601 format (e.g., 2024-01-15T14:30:00Z)",
						},
						"target": map[string]any{
							"type":        "string",
							"enum":        []string{"user", "ai"},
							"description": "Who receives the reminder: 'user' (direct message) or 'ai' (AI processes and responds). Default: 'user'",
						},
					},
					"required": []string{"message", "remind_at"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_reminders",
				Description: "List all pending reminders for the user",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "delete_reminder",
				Description: "Delete a reminder by ID",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "integer",
							"description": "The ID of the reminder to delete",
						},
					},
					"required": []string{"id"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "update_memory",
				Description: "Update the user's persistent memory. Use this to store important information about the user that should be remembered across conversations (preferences, facts about them, ongoing projects, etc). The memory has a 2000 character limit, so summarize and prioritize the most important information. The new content completely replaces the old memory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{
							"type":        "string",
							"description": "The complete memory content (max 2000 characters). This replaces all previous memory.",
						},
					},
					"required": []string{"content"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "run_code",
				Description: "Execute JavaScript code in a sandboxed environment. Use this to perform calculations, data transformations, or any logic that benefits from code execution. The sandbox has console.log available for output. Execution is limited to 5 seconds.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code": map[string]any{
							"type":        "string",
							"description": "JavaScript code to execute. Use console.log() for output. The last expression's value is also returned.",
						},
					},
					"required": []string{"code"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "send_email",
				Description: "Send an email on behalf of the user. Use this when the user wants to send an email to someone.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"to": map[string]any{
							"type":        "string",
							"description": "Recipient email address",
						},
						"subject": map[string]any{
							"type":        "string",
							"description": "Email subject line",
						},
						"body": map[string]any{
							"type":        "string",
							"description": "Email body content (plain text)",
						},
					},
					"required": []string{"to", "subject", "body"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "make_call",
				Description: "Make a phone call on behalf of the user. Use this when the user wants you to call someone (e.g., to make a reservation, ask for information, etc.). You (Minerva) will handle the conversation and report back.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"phone_number": map[string]any{
							"type":        "string",
							"description": "Phone number to call. Can be with or without country code (if no country code, +34 Spain is assumed)",
						},
						"purpose": map[string]any{
							"type":        "string",
							"description": "Detailed description of the purpose of the call and what you need to accomplish. Be specific about what information to gather or actions to take.",
						},
					},
					"required": []string{"phone_number", "purpose"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "create_task",
				Description: "Create a long-running background task that will be executed by Claude Code. Use this for complex tasks that require web browsing, research, file creation, phone calls, or any work that takes significant time. The task runs autonomously and the user will be notified when it completes.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{
							"type":        "string",
							"description": "Detailed description of the task to accomplish. Be specific about goals, constraints, and expected deliverables. Claude Code will have full autonomy to complete this task.",
						},
					},
					"required": []string{"description"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "get_task_progress",
				Description: "Check the progress of a running background task. Returns the current status and contents of the PROGRESS.md file that Claude Code updates.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task_id": map[string]any{
							"type":        "string",
							"description": "The ID of the task to check (e.g., task_1234567890)",
						},
					},
					"required": []string{"task_id"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_agents",
				Description: "List all connected Claude Code agents. Agents are remote machines running minerva-agent that can execute Claude Code tasks.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "run_claude_task",
				Description: "Run a task on a remote Claude Code agent. The agent will execute the prompt using Claude Code with full permissions and return the result. Use list_agents first to see available agents.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agent": map[string]any{
							"type":        "string",
							"description": "Name of the agent to run the task on",
						},
						"prompt": map[string]any{
							"type":        "string",
							"description": "The prompt/task for Claude Code to execute",
						},
						"dir": map[string]any{
							"type":        "string",
							"description": "Optional working directory override (defaults to agent's configured directory)",
						},
					},
					"required": []string{"agent", "prompt"},
				},
			},
		},
	}
}
