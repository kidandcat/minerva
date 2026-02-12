package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot represents the Telegram bot
type Bot struct {
	api            *tgbotapi.BotAPI
	db             *DB
	ai             *AIClient
	config         *Config
	running        bool
	voiceManager   *VoiceManager
	taskRunner     *TaskRunner
	agentHub       *AgentHub
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
	// Register bot commands menu
	commands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Mostrar bienvenida y ayuda"},
		tgbotapi.BotCommand{Command: "clear", Description: "Limpiar contexto de conversación"},
		tgbotapi.BotCommand{Command: "token", Description: "Actualizar OAuth token de Claude"},
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

		// Handle callback queries (button clicks)
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
			// Handle commands async
			go func(upd tgbotapi.Update) {
				if err := b.handleCommand(upd); err != nil {
					log.Printf("Command error: %v", err)
					b.sendMessage(upd.Message.Chat.ID, fmt.Sprintf("Error: %v", err))
				}
			}(update)
			continue
		}

		// Handle messages async
		go func(upd tgbotapi.Update) {
			if err := b.handleMessage(upd); err != nil {
				log.Printf("Message error: %v", err)
				b.sendMessage(upd.Message.Chat.ID, fmt.Sprintf("Error: %v", err))
			}
		}(update)
	}
}

// Stop stops the bot
func (b *Bot) Stop() {
	b.running = false
	b.api.StopReceivingUpdates()
}

// ProcessSystemEvent processes a system event (like call summaries) through the AI brain
// This allows the brain to take actions based on system events (create reminders, follow up, etc.)
func (b *Bot) ProcessSystemEvent(userID int64, eventMessage string) error {
	// Get user
	user, _, err := b.db.GetOrCreateUser(userID, "", "")
	if err != nil {
		return err
	}

	// Get active conversation
	conv, err := b.db.GetActiveConversation(user.ID)
	if err != nil {
		return err
	}

	// Load context
	dbMessages, err := b.db.GetConversationMessages(conv.ID, b.config.MaxContextMessages)
	if err != nil {
		return err
	}

	var messages []ChatMessage
	for _, m := range dbMessages {
		messages = append(messages, ChatMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	// Add the system event as a user message
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: eventMessage,
	})

	// Save the event message
	b.db.SaveMessage(conv.ID, "user", eventMessage, nil)

	// Chat with AI
	response, err := b.chatWithAI(messages, user.SystemPrompt, user.ID, conv.ID)
	if err != nil {
		return fmt.Errorf("AI error: %w", err)
	}

	// Save assistant response
	b.db.SaveMessage(conv.ID, "assistant", response.Content, nil)

	// Send AI response to user
	return b.sendMessage(userID, response.Content)
}

// handleCallback handles inline button callbacks
func (b *Bot) handleCallback(callback *tgbotapi.CallbackQuery) error {
	data := callback.Data

	// Parse callback data: "approve:USER_ID", "reject:USER_ID", or "kill_task:TASK_ID"
	parts := strings.Split(data, ":")
	if len(parts) != 2 {
		return nil
	}

	action := parts[0]

	switch action {
	case "kill_task":
		taskID := parts[1]
		return b.handleKillTask(callback, taskID)

	case "approve":
		userID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return err
		}
		return b.handleApprove(callback, userID)

	case "reject":
		userID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return err
		}
		return b.handleReject(callback, userID)
	}

	return nil
}

// handleKillTask kills a running agent task
func (b *Bot) handleKillTask(callback *tgbotapi.CallbackQuery, taskID string) error {
	// Kill the task
	if err := b.agentHub.KillTask(taskID); err != nil {
		b.api.Send(tgbotapi.NewCallback(callback.ID, fmt.Sprintf("Error: %v", err)))
		return err
	}

	// Update the message to remove the button and show it was killed
	editMsg := tgbotapi.NewEditMessageText(
		callback.Message.Chat.ID,
		callback.Message.MessageID,
		fmt.Sprintf("%s\n\n⛔ *Killed by user*", callback.Message.Text),
	)
	editMsg.ParseMode = "Markdown"
	if _, err := b.api.Send(editMsg); err != nil {
		log.Printf("Failed to update kill message: %v", err)
	}

	// Answer callback
	b.api.Send(tgbotapi.NewCallback(callback.ID, "Task killed"))
	return nil
}

// handleApprove handles user approval
func (b *Bot) handleApprove(callback *tgbotapi.CallbackQuery, userID int64) error {
	// Approve the user
	if err := b.db.ApproveUser(userID); err != nil {
		return err
	}

	// Get user info
	user, err := b.db.GetUser(userID)
	if err != nil {
		return err
	}

	// Update the admin message
	editMsg := tgbotapi.NewEditMessageText(
		callback.Message.Chat.ID,
		callback.Message.MessageID,
		fmt.Sprintf("✅ *Approved*\n\nID: `%d`\nUsername: @%s\nName: %s",
			user.ID, user.Username, user.FirstName),
	)
	editMsg.ParseMode = "Markdown"
	b.api.Send(editMsg)

	// Notify the user
	b.sendMessage(userID, "✅ You have been approved! You can now use Minerva. Send /start to begin.")

	// Answer callback
	b.api.Send(tgbotapi.NewCallback(callback.ID, "User approved"))

	return nil
}

// handleReject handles user rejection
func (b *Bot) handleReject(callback *tgbotapi.CallbackQuery, userID int64) error {
	// Just remove the buttons (don't delete user, they can try again)
	user, err := b.db.GetUser(userID)
	if err != nil {
		return err
	}

	// Update the admin message to show rejected
	editMsg := tgbotapi.NewEditMessageText(
		callback.Message.Chat.ID,
		callback.Message.MessageID,
		fmt.Sprintf("❌ *Rejected*\n\nID: `%d`\nUsername: @%s\nName: %s",
			user.ID, user.Username, user.FirstName),
	)
	editMsg.ParseMode = "Markdown"
	b.api.Send(editMsg)

	// Answer callback
	b.api.Send(tgbotapi.NewCallback(callback.ID, "Request rejected"))

	return nil
}

// isAdmin checks if a user is the admin
func (b *Bot) isAdmin(userID int64) bool {
	return b.config.AdminID != 0 && userID == b.config.AdminID
}

// checkUserApproval checks if user is approved and handles pending requests
func (b *Bot) checkUserApproval(msg *tgbotapi.Message) (*User, bool, error) {
	user, isNew, err := b.db.GetOrCreateUser(msg.From.ID, msg.From.UserName, msg.From.FirstName)
	if err != nil {
		return nil, false, err
	}

	// Admin is always approved
	if b.isAdmin(user.ID) {
		if !user.Approved {
			b.db.ApproveUser(user.ID)
			user.Approved = true
		}
		return user, true, nil
	}

	// If no admin configured, reject everyone (security: misconfigured deploy must not be open)
	if b.config.AdminID == 0 {
		b.sendMessage(msg.Chat.ID, "Bot not configured. ADMIN_ID is required.")
		return user, false, nil
	}

	// User is approved
	if user.Approved {
		return user, true, nil
	}

	// New user - send approval request to admin
	if isNew {
		b.sendApprovalRequest(user)
		b.sendMessage(msg.Chat.ID, "⏳ Your access request has been sent to the administrator. Please wait for approval.")
	} else {
		// Existing unapproved user - remind them
		b.sendMessage(msg.Chat.ID, "⏳ Your access request is pending administrator approval.")
	}

	return user, false, nil
}

// sendApprovalRequest sends an approval request to the admin
func (b *Bot) sendApprovalRequest(user *User) {
	if b.config.AdminID == 0 {
		return
	}

	text := fmt.Sprintf("🆕 *New User Request*\n\nID: `%d`\nUsername: @%s\nName: %s",
		user.ID, user.Username, user.FirstName)

	msg := tgbotapi.NewMessage(b.config.AdminID, text)
	msg.ParseMode = "Markdown"

	// Add inline keyboard with approve/reject buttons
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Approve", fmt.Sprintf("approve:%d", user.ID)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Reject", fmt.Sprintf("reject:%d", user.ID)),
		),
	)
	msg.ReplyMarkup = keyboard

	if _, err := b.api.Send(msg); err != nil {
		log.Printf("Failed to send approval request to admin: %v", err)
	}
}

func (b *Bot) handleCommand(update tgbotapi.Update) error {
	msg := update.Message
	command := msg.Command()
	args := msg.CommandArguments()

	log.Printf("Command /%s from %d", command, msg.From.ID)

	// Check approval for all commands except start
	user, approved, err := b.checkUserApproval(msg)
	if err != nil {
		return err
	}

	// Allow /start for everyone (to show pending message)
	if command == "start" {
		if !approved {
			return nil // Message already sent by checkUserApproval
		}
		return b.handleStart(msg)
	}

	// All other commands require approval
	if !approved {
		return nil
	}

	switch command {
	case "clear":
		return b.handleClear(msg, user)
	case "token":
		return b.handleToken(msg, args)
	default:
		return b.sendMessage(msg.Chat.ID, "Unknown command. Use /start for help.")
	}
}

func (b *Bot) handleStart(msg *tgbotapi.Message) error {
	welcome := `*Minerva - Personal AI Assistant*

Envíame un mensaje para chatear.

*Comandos:*
/clear - Limpiar contexto de conversación
/token <token> - Actualizar OAuth token de Claude`

	return b.sendMessage(msg.Chat.ID, welcome)
}

func (b *Bot) handleClear(msg *tgbotapi.Message, user *User) error {
	conv, err := b.db.GetActiveConversation(user.ID)
	if err != nil {
		return b.sendMessage(msg.Chat.ID, "Error al obtener conversación.")
	}

	if err := b.db.ClearConversationMessages(conv.ID); err != nil {
		return b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
	}

	return b.sendMessage(msg.Chat.ID, "Contexto limpiado.")
}

func (b *Bot) handleToken(msg *tgbotapi.Message, args string) error {
	if !b.isAdmin(msg.From.ID) {
		return b.sendMessage(msg.Chat.ID, "Only the admin can update the token.")
	}

	if args == "" {
		return b.sendMessage(msg.Chat.ID, "Usage: /token <oauth_token>")
	}

	// Update in running process
	os.Setenv("CLAUDE_CODE_OAUTH_TOKEN", args)

	// Persist to .env file
	if err := updateEnvFile("CLAUDE_CODE_OAUTH_TOKEN", args); err != nil {
		return b.sendMessage(msg.Chat.ID, fmt.Sprintf("Token set in memory but failed to persist to .env: %v", err))
	}

	return b.sendMessage(msg.Chat.ID, "OAuth token updated.")
}

func (b *Bot) handleMessage(update tgbotapi.Update) error {
	msg := update.Message
	userMessage := msg.Text
	caption := msg.Caption

	// Use caption if no text (for photos/documents)
	if userMessage == "" && caption != "" {
		userMessage = caption
	}
	if userMessage == "" {
		userMessage = "[Image]"
	}

	log.Printf("Message from %d: %s", msg.From.ID, truncate(userMessage, 50))

	// Check approval
	user, approved, err := b.checkUserApproval(msg)
	if err != nil {
		return err
	}
	if !approved {
		return nil
	}

	b.sendTypingAction(msg.Chat.ID)

	// Get or create conversation
	conv, err := b.db.GetActiveConversation(user.ID)
	if err != nil {
		return err
	}

	// Load context
	dbMessages, err := b.db.GetConversationMessages(conv.ID, b.config.MaxContextMessages)
	if err != nil {
		return err
	}

	// Convert to ChatMessage format
	var messages []ChatMessage
	for _, m := range dbMessages {
		cm := ChatMessage{
			Role:    m.Role,
			Content: m.Content,
		}
		if m.ToolCalls != nil {
			json.Unmarshal([]byte(*m.ToolCalls), &cm.ToolCalls)
		}
		messages = append(messages, cm)
	}

	// Handle file attachments: download to temp and include path in prompt
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		filePath, err := b.downloadFileToTemp(photo.FileID, ".jpg")
		if err != nil {
			log.Printf("Failed to download photo: %v", err)
		} else {
			if userMessage == "" {
				userMessage = "Analyze this image:"
			}
			userMessage = fmt.Sprintf("%s %s", userMessage, filePath)
		}
	}

	if msg.Document != nil {
		ext := filepath.Ext(msg.Document.FileName)
		if ext == "" {
			ext = ".bin"
		}
		filePath, err := b.downloadFileToTemp(msg.Document.FileID, ext)
		if err != nil {
			log.Printf("Failed to download document: %v", err)
		} else {
			if userMessage == "" {
				userMessage = fmt.Sprintf("Analyze this file (%s):", msg.Document.FileName)
			}
			userMessage = fmt.Sprintf("%s %s", userMessage, filePath)
		}
	}

	// Add user message
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: userMessage,
	})

	// Save user message (text only for DB)
	if err := b.db.SaveMessage(conv.ID, "user", userMessage, nil); err != nil {
		return err
	}

	// Chat with AI
	response, err := b.chatWithAI(messages, user.SystemPrompt, user.ID, conv.ID)
	if err != nil {
		return fmt.Errorf("AI error: %w", err)
	}

	// Save assistant response
	if err := b.db.SaveMessage(conv.ID, "assistant", response.Content, nil); err != nil {
		log.Printf("Failed to save assistant message: %v", err)
	}

	return b.sendMessage(msg.Chat.ID, response.Content)
}

// downloadFileToTemp downloads a file from Telegram to a temp file and returns the path
func (b *Bot) downloadFileToTemp(fileID, ext string) (string, error) {
	file, err := b.api.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("failed to get file: %w", err)
	}

	fileURL := file.Link(b.config.TelegramBotToken)

	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	destPath := filepath.Join(os.TempDir(), fmt.Sprintf("telegram_%d%s", time.Now().UnixNano(), ext))
	out, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}

	return destPath, nil
}

// AIResponse contains the AI response and model used
type AIResponse struct {
	Content string
	Model   string
}

func (b *Bot) chatWithAI(messages []ChatMessage, systemPrompt string, userID, convID int64) (*AIResponse, error) {
	// Inject user memory into system prompt
	memory, err := b.db.GetUserMemory(userID)
	if err != nil {
		log.Printf("Failed to get user memory: %v", err)
	}
	if memory != "" {
		systemPrompt = systemPrompt + "\n\n[USER MEMORY - Important information about this user]\n" + memory
	}

	// Inject connected agents and their projects
	if b.agentHub != nil {
		agentInfo := b.getAgentProjectsContext()
		if agentInfo != "" {
			systemPrompt = systemPrompt + "\n\n" + agentInfo
		}
	}

	b.sendTypingAction(userID)

	result, err := b.ai.Chat(messages, systemPrompt, nil)
	if err != nil {
		return nil, err
	}

	content, _ := result.Message.Content.(string)
	return &AIResponse{Content: content, Model: result.Model}, nil
}

func (b *Bot) sendMessage(chatID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	chunks := splitMessage(text, 4096)
	for _, chunk := range chunks {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = "Markdown"
		_, err := b.api.Send(msg)
		if err != nil && strings.Contains(err.Error(), "can't parse entities") {
			msg.ParseMode = ""
			_, err = b.api.Send(msg)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// splitMessage splits text into chunks of at most maxLen characters,
// preferring to break at newlines, then at spaces, to avoid cutting mid-sentence.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}
		chunk := text[:maxLen]
		// Prefer breaking at last newline
		if idx := strings.LastIndex(chunk, "\n"); idx > maxLen/4 {
			chunks = append(chunks, text[:idx])
			text = text[idx+1:]
		} else if idx := strings.LastIndex(chunk, " "); idx > maxLen/4 {
			// Fall back to last space
			chunks = append(chunks, text[:idx])
			text = text[idx+1:]
		} else {
			// No good break point, hard cut
			chunks = append(chunks, chunk)
			text = text[maxLen:]
		}
	}
	return chunks
}

func (b *Bot) sendDocument(chatID int64, filename string, data []byte) error {
	fileBytes := tgbotapi.FileBytes{Name: filename, Bytes: data}
	doc := tgbotapi.NewDocument(chatID, fileBytes)
	_, err := b.api.Send(doc)
	return err
}

func (b *Bot) sendTypingAction(chatID int64) {
	action := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	b.api.Send(action)
}

// sendAgentTaskStartedMessage sends a confirmation message with Kill button when an agent task starts
func (b *Bot) sendAgentTaskStartedMessage(taskID, agentName, prompt string) {
	if b.config.AdminID == 0 {
		return
	}

	// Truncate prompt for display
	displayPrompt := prompt
	if len(displayPrompt) > 100 {
		displayPrompt = displayPrompt[:100] + "..."
	}

	text := fmt.Sprintf("🚀 *Agent task started*\n\nAgent: `%s`\nTask ID: `%s`\nPrompt: %s",
		agentName, taskID, displayPrompt)

	msg := tgbotapi.NewMessage(b.config.AdminID, text)
	msg.ParseMode = "Markdown"

	// Add Kill button (red/dangerous style via emoji)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⛔ Kill", fmt.Sprintf("kill_task:%s", taskID)),
		),
	)
	msg.ReplyMarkup = keyboard

	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Failed to send agent task started message: %v", err)
		return
	}

	// Store the message ID in the task so we can update it later
	if err := b.agentHub.UpdateTaskMessage(taskID, sentMsg.MessageID, b.config.AdminID); err != nil {
		log.Printf("Failed to update task message ID: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// getAgentProjectsContext returns a formatted string with connected agents and their projects
func (b *Bot) getAgentProjectsContext() string {
	if b.agentHub == nil {
		return ""
	}

	agents := b.agentHub.ListAgents()
	if len(agents) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[CONNECTED AGENTS AND PROJECTS]\n")
	sb.WriteString("You can use the run_claude tool to execute tasks on these agents.\n\n")

	for _, agent := range agents {
		agentName, _ := agent["name"].(string)
		workDir, _ := agent["workDir"].(string)
		if agentName == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("Agent: %s (working dir: %s)\n", agentName, workDir))

		// Get projects with a short timeout
		projects, err := b.agentHub.GetProjects(agentName, 5*time.Second)
		if err != nil {
			sb.WriteString("  Projects: (failed to retrieve)\n")
		} else if len(projects) == 0 {
			sb.WriteString("  Projects: (none found)\n")
		} else {
			sb.WriteString("  Projects:\n")
			for _, proj := range projects {
				sb.WriteString(fmt.Sprintf("    - %s\n", proj))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
