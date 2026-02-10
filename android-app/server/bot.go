package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot represents the Telegram bot
type Bot struct {
	api        *tgbotapi.BotAPI
	db         *DB
	ai         *AIClient
	config     *Config
	running    bool
	taskRunner *TaskRunner
	agentHub   *AgentHub
}

// NewBot creates a new Telegram bot instance
func NewBot(config *Config, db *DB, ai *AIClient) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(config.TelegramBotToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}
	log.Printf("Authorized as @%s", api.Self.UserName)

	return &Bot{
		api:    api,
		db:     db,
		ai:     ai,
		config: config,
	}, nil
}

// Start begins the bot's main polling loop
func (b *Bot) Start() {
	commands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Show help"},
		tgbotapi.BotCommand{Command: "new", Description: "New conversation"},
		tgbotapi.BotCommand{Command: "history", Description: "View conversations"},
		tgbotapi.BotCommand{Command: "tasks", Description: "View background tasks"},
		tgbotapi.BotCommand{Command: "memory", Description: "View what I remember"},
		tgbotapi.BotCommand{Command: "system", Description: "Set system prompt"},
		tgbotapi.BotCommand{Command: "token", Description: "Update OAuth token"},
		tgbotapi.BotCommand{Command: "status", Description: "Phone/server status"},
		tgbotapi.BotCommand{Command: "agents", Description: "List connected agents"},
	)
	if _, err := b.api.Request(commands); err != nil {
		log.Printf("Failed to set bot commands: %v", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)
	b.running = true

	for update := range updates {
		if !b.running {
			break
		}
		if update.CallbackQuery != nil {
			if err := b.handleCallback(update.CallbackQuery); err != nil {
				log.Printf("Callback error: %v", err)
			}
			continue
		}
		if update.Message == nil {
			continue
		}
		if update.Message.IsCommand() {
			go func(upd tgbotapi.Update) {
				if err := b.handleCommand(upd); err != nil {
					log.Printf("Command error: %v", err)
					b.sendMessage(upd.Message.Chat.ID, fmt.Sprintf("Error: %v", err))
				}
			}(update)
			continue
		}
		go func(upd tgbotapi.Update) {
			if err := b.handleMessage(upd); err != nil {
				log.Printf("Message error: %v", err)
				b.sendMessage(upd.Message.Chat.ID, fmt.Sprintf("Error: %v", err))
			}
		}(update)
	}
}

func (b *Bot) Stop() {
	b.running = false
	b.api.StopReceivingUpdates()
}

func (b *Bot) handleCallback(callback *tgbotapi.CallbackQuery) error {
	parts := strings.Split(callback.Data, ":")
	if len(parts) != 2 {
		return nil
	}
	action := parts[0]
	userID, err := fmt.Sscanf(parts[1], "%d", new(int64))
	if err != nil || userID == 0 {
		return nil
	}

	var targetID int64
	fmt.Sscanf(parts[1], "%d", &targetID)

	switch action {
	case "approve":
		b.db.ApproveUser(targetID)
		user, _ := b.db.GetUser(targetID)
		editMsg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID,
			fmt.Sprintf("Approved: %s (@%s)", user.FirstName, user.Username))
		b.api.Send(editMsg)
		b.sendMessage(targetID, "You have been approved! Send /start to begin.")
		b.api.Send(tgbotapi.NewCallback(callback.ID, "User approved"))
	case "reject":
		editMsg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, "Rejected")
		b.api.Send(editMsg)
		b.api.Send(tgbotapi.NewCallback(callback.ID, "Rejected"))
	}
	return nil
}

func (b *Bot) isAdmin(userID int64) bool {
	return b.config.AdminID != 0 && userID == b.config.AdminID
}

func (b *Bot) checkUserApproval(msg *tgbotapi.Message) (*User, bool, error) {
	user, isNew, err := b.db.GetOrCreateUser(msg.From.ID, msg.From.UserName, msg.From.FirstName)
	if err != nil {
		return nil, false, err
	}
	if b.isAdmin(user.ID) {
		if !user.Approved {
			b.db.ApproveUser(user.ID)
			user.Approved = true
		}
		return user, true, nil
	}
	if b.config.AdminID == 0 {
		b.sendMessage(msg.Chat.ID, "Bot not configured. ADMIN_ID is required.")
		return user, false, nil
	}
	if user.Approved {
		return user, true, nil
	}
	if isNew {
		b.sendApprovalRequest(user)
		b.sendMessage(msg.Chat.ID, "Your access request has been sent to the administrator.")
	} else {
		b.sendMessage(msg.Chat.ID, "Your access request is pending.")
	}
	return user, false, nil
}

func (b *Bot) sendApprovalRequest(user *User) {
	if b.config.AdminID == 0 {
		return
	}
	text := fmt.Sprintf("New User Request\n\nID: %d\nUsername: @%s\nName: %s", user.ID, user.Username, user.FirstName)
	msg := tgbotapi.NewMessage(b.config.AdminID, text)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Approve", fmt.Sprintf("approve:%d", user.ID)),
			tgbotapi.NewInlineKeyboardButtonData("Reject", fmt.Sprintf("reject:%d", user.ID)),
		),
	)
	msg.ReplyMarkup = keyboard
	b.api.Send(msg)
}

func (b *Bot) handleCommand(update tgbotapi.Update) error {
	msg := update.Message
	command := msg.Command()
	args := msg.CommandArguments()

	user, approved, err := b.checkUserApproval(msg)
	if err != nil {
		return err
	}
	if command == "start" {
		if !approved {
			return nil
		}
		return b.handleStart(msg)
	}
	if !approved {
		return nil
	}

	switch command {
	case "new":
		return b.handleNew(msg, user)
	case "history":
		return b.handleHistory(msg, user)
	case "system":
		return b.handleSystem(msg, args, user)
	case "memory":
		return b.handleMemory(msg, user)
	case "tasks":
		return b.handleTasks(msg, user)
	case "token":
		return b.handleToken(msg, args)
	case "status":
		return b.handleStatus(msg)
	case "agents":
		return b.handleAgents(msg)
	case "cancel":
		return b.handleCancelTask(msg, args)
	default:
		return b.sendMessage(msg.Chat.ID, "Unknown command. Use /start for help.")
	}
}

func (b *Bot) handleStart(msg *tgbotapi.Message) error {
	welcome := `*Minerva Android - Personal AI Assistant*

Commands:
/new - Start a new conversation
/history - View past conversations
/system <prompt> - Set custom AI behavior
/memory - View what I remember
/tasks - View running tasks
/token <token> - Update Claude Code OAuth token
/status - Phone/server status
/agents - List connected agents

Just send me a message to chat!`
	return b.sendMessage(msg.Chat.ID, welcome)
}

func (b *Bot) handleNew(msg *tgbotapi.Message, user *User) error {
	_, err := b.db.CreateConversation(user.ID, "")
	if err != nil {
		return err
	}
	return b.sendMessage(msg.Chat.ID, "New conversation started!")
}

func (b *Bot) handleHistory(msg *tgbotapi.Message, user *User) error {
	conversations, err := b.db.GetUserConversations(user.ID, 10)
	if err != nil {
		return err
	}
	if len(conversations) == 0 {
		return b.sendMessage(msg.Chat.ID, "No conversations yet.")
	}
	var sb strings.Builder
	sb.WriteString("*Recent conversations:*\n\n")
	for i, conv := range conversations {
		status := ""
		if conv.Active {
			status = " (active)"
		}
		sb.WriteString(fmt.Sprintf("%d. %s%s\n   %s\n\n", i+1, conv.Title, status, conv.CreatedAt.Format("Jan 2, 15:04")))
	}
	return b.sendMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleSystem(msg *tgbotapi.Message, args string, user *User) error {
	if args == "" {
		if user.SystemPrompt == "" {
			return b.sendMessage(msg.Chat.ID, "No custom system prompt. Use /system <prompt> to set one.")
		}
		return b.sendMessage(msg.Chat.ID, fmt.Sprintf("Current: %s\n\nUse /system clear to remove.", user.SystemPrompt))
	}
	if strings.ToLower(args) == "clear" {
		b.db.UpdateUserSystemPrompt(user.ID, "")
		return b.sendMessage(msg.Chat.ID, "System prompt cleared.")
	}
	b.db.UpdateUserSystemPrompt(user.ID, args)
	return b.sendMessage(msg.Chat.ID, "System prompt updated!")
}

func (b *Bot) handleMemory(msg *tgbotapi.Message, user *User) error {
	memory, err := b.db.GetUserMemory(user.ID)
	if err != nil {
		return err
	}
	if memory == "" {
		return b.sendMessage(msg.Chat.ID, "No memory stored yet.")
	}
	return b.sendMessage(msg.Chat.ID, fmt.Sprintf("*Memory:*\n\n%s\n\n(%d/2000 chars)", memory, len(memory)))
}

func (b *Bot) handleTasks(msg *tgbotapi.Message, user *User) error {
	tasks, err := b.db.GetUserTasks(user.ID)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return b.sendMessage(msg.Chat.ID, "No tasks.")
	}
	var sb strings.Builder
	sb.WriteString("*Tasks:*\n\n")
	for _, t := range tasks {
		emoji := map[string]string{"running": "🔄", "completed": "✅", "failed": "❌", "cancelled": "🚫"}[t.Status]
		desc := t.Description
		if len(desc) > 50 {
			desc = desc[:50] + "..."
		}
		sb.WriteString(fmt.Sprintf("%s `%s` %s\n   %s\n\n", emoji, t.ID, desc, t.CreatedAt.Format("Jan 2, 15:04")))
	}
	return b.sendMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleToken(msg *tgbotapi.Message, args string) error {
	if !b.isAdmin(msg.From.ID) {
		return b.sendMessage(msg.Chat.ID, "Admin only.")
	}
	if args == "" {
		return b.sendMessage(msg.Chat.ID, "Usage: /token <new_oauth_token>")
	}
	// Update .env file
	if err := UpdateEnvFile("CLAUDE_CODE_OAUTH_TOKEN", args); err != nil {
		return b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed to update .env: %v", err))
	}
	// Set in current process
	os.Setenv("CLAUDE_CODE_OAUTH_TOKEN", args)

	// Verify it works
	if err := CheckClaudeAuth(); err != nil {
		return b.sendMessage(msg.Chat.ID, fmt.Sprintf("Token saved but auth check failed: %v\nToken will be used on next restart.", err))
	}
	return b.sendMessage(msg.Chat.ID, "OAuth token updated and verified!")
}

func (b *Bot) handleStatus(msg *tgbotapi.Message) error {
	uptime := time.Since(startTime).Round(time.Second)
	agentCount := 0
	if b.agentHub != nil {
		agentCount = len(b.agentHub.ListAgents())
	}

	status := fmt.Sprintf(`*Minerva Android Status*

Uptime: %s
Agents: %d connected
Database: %s

Claude Code: checking...`, uptime, agentCount, b.config.DatabasePath)

	b.sendMessage(msg.Chat.ID, status)

	// Check Claude Code auth async
	go func() {
		if err := CheckClaudeAuth(); err != nil {
			b.sendMessage(msg.Chat.ID, fmt.Sprintf("Claude Code: ❌ %v", err))
		} else {
			b.sendMessage(msg.Chat.ID, "Claude Code: ✅ working")
		}
	}()
	return nil
}

func (b *Bot) handleAgents(msg *tgbotapi.Message) error {
	if b.agentHub == nil {
		return b.sendMessage(msg.Chat.ID, "Agent hub not configured.")
	}
	agents := b.agentHub.ListAgents()
	if len(agents) == 0 {
		return b.sendMessage(msg.Chat.ID, "No agents connected.")
	}
	var sb strings.Builder
	sb.WriteString("*Connected agents:*\n\n")
	for _, a := range agents {
		name, _ := a["name"].(string)
		workDir, _ := a["workDir"].(string)
		sb.WriteString(fmt.Sprintf("• %s (%s)\n", name, workDir))
	}
	return b.sendMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleCancelTask(msg *tgbotapi.Message, args string) error {
	if args == "" {
		return b.sendMessage(msg.Chat.ID, "Usage: /cancel <task_id>")
	}
	if b.taskRunner == nil {
		return b.sendMessage(msg.Chat.ID, "Task runner not configured")
	}
	if err := b.taskRunner.CancelTask(args); err != nil {
		return b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
	}
	return b.sendMessage(msg.Chat.ID, fmt.Sprintf("Task %s cancelled", args))
}

func (b *Bot) handleMessage(update tgbotapi.Update) error {
	msg := update.Message
	userMessage := msg.Text
	if userMessage == "" && msg.Caption != "" {
		userMessage = msg.Caption
	}
	if userMessage == "" {
		userMessage = "[Image]"
	}

	log.Printf("Message from %d: %s", msg.From.ID, truncateStr(userMessage, 50))

	user, approved, err := b.checkUserApproval(msg)
	if err != nil {
		return err
	}
	if !approved {
		return nil
	}

	b.sendTypingAction(msg.Chat.ID)

	conv, err := b.db.GetActiveConversation(user.ID)
	if err != nil {
		return err
	}

	// Handle file attachments
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		if filePath, err := b.downloadFileToTemp(photo.FileID, ".jpg"); err == nil {
			userMessage = fmt.Sprintf("%s %s", userMessage, filePath)
		}
	}
	if msg.Document != nil {
		ext := filepath.Ext(msg.Document.FileName)
		if ext == "" {
			ext = ".bin"
		}
		if filePath, err := b.downloadFileToTemp(msg.Document.FileID, ext); err == nil {
			userMessage = fmt.Sprintf("%s %s", userMessage, filePath)
		}
	}

	// Save user message
	b.db.SaveMessage(conv.ID, "user", userMessage)

	// Build context from conversation history
	dbMessages, err := b.db.GetConversationMessages(conv.ID, b.config.MaxContextMessages)
	if err != nil {
		return err
	}

	// Build a prompt with conversation context for Claude Code
	var promptBuilder strings.Builder

	// System prompt
	memory, _ := b.db.GetUserMemory(user.ID)
	if user.SystemPrompt != "" || memory != "" {
		promptBuilder.WriteString("[CONTEXT]\n")
		if user.SystemPrompt != "" {
			promptBuilder.WriteString("System prompt: " + user.SystemPrompt + "\n")
		}
		if memory != "" {
			promptBuilder.WriteString("User memory: " + memory + "\n")
		}
		promptBuilder.WriteString("\n")
	}

	// Agent context
	if b.agentHub != nil {
		agents := b.agentHub.ListAgents()
		if len(agents) > 0 {
			promptBuilder.WriteString("[CONNECTED AGENTS]\n")
			for _, a := range agents {
				name, _ := a["name"].(string)
				promptBuilder.WriteString(fmt.Sprintf("- %s\n", name))
			}
			promptBuilder.WriteString("\n")
		}
	}

	// Conversation history
	if len(dbMessages) > 1 { // More than just the current message
		promptBuilder.WriteString("[CONVERSATION HISTORY]\n")
		for _, m := range dbMessages[:len(dbMessages)-1] { // Exclude the last (current) message
			promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", m.Role, truncateStr(m.Content, 500)))
		}
		promptBuilder.WriteString("\n")
	}

	promptBuilder.WriteString("[USER MESSAGE]\n")
	promptBuilder.WriteString(userMessage)

	// Send to Claude Code
	response, err := b.ai.Chat(promptBuilder.String())
	if err != nil {
		// Check if it's an auth error
		if strings.Contains(err.Error(), "auth") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "token") {
			b.sendMessage(msg.Chat.ID, "Claude Code authentication error. Use /token to update your OAuth token.")
			return nil
		}
		return fmt.Errorf("AI error: %w", err)
	}

	// Save assistant response
	b.db.SaveMessage(conv.ID, "assistant", response)

	return b.sendMessage(msg.Chat.ID, response)
}

func (b *Bot) downloadFileToTemp(fileID, ext string) (string, error) {
	file, err := b.api.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", err
	}
	fileURL := file.Link(b.config.TelegramBotToken)
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	destPath := filepath.Join(os.TempDir(), fmt.Sprintf("telegram_%d%s", time.Now().UnixNano(), ext))
	out, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	io.Copy(out, resp.Body)
	return destPath, nil
}

func (b *Bot) sendMessage(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	_, err := b.api.Send(msg)
	if err != nil && strings.Contains(err.Error(), "can't parse entities") {
		msg.ParseMode = ""
		_, err = b.api.Send(msg)
	}
	return err
}

func (b *Bot) sendTypingAction(chatID int64) {
	action := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	b.api.Send(action)
}

// ProcessSystemEvent sends a system event to the AI and notifies admin
func (b *Bot) ProcessSystemEvent(adminID int64, context string) {
	prompt := fmt.Sprintf("[SYSTEM EVENT]\n%s\n\nRespond briefly confirming what actions you've taken (if any).", context)
	response, err := b.ai.Chat(prompt)
	if err != nil {
		log.Printf("[Bot] Failed to process system event: %v", err)
		return
	}
	if response != "" {
		b.sendMessage(adminID, response)
	}
}

// sendDocument sends a document (file) to a Telegram chat
func (b *Bot) sendDocument(chatID int64, filename string, data []byte) error {
	fileBytes := tgbotapi.FileBytes{Name: filename, Bytes: data}
	doc := tgbotapi.NewDocument(chatID, fileBytes)
	_, err := b.api.Send(doc)
	return err
}

