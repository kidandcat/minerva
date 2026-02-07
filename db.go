package main

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps sql.DB
type DB struct {
	*sql.DB
}

// User represents a Telegram user
type User struct {
	ID           int64
	Username     string
	FirstName    string
	SystemPrompt string
	Approved     bool
	CreatedAt    time.Time
}

// Conversation represents a chat conversation
type Conversation struct {
	ID        int64
	UserID    int64
	Title     string
	Active    bool
	CreatedAt time.Time
}

// Message represents a chat message
type Message struct {
	ID             int64
	ConversationID int64
	Role           string
	Content        string
	ToolCalls      *string
	CreatedAt      time.Time
}

// Reminder represents a scheduled reminder
type Reminder struct {
	ID        int64
	UserID    int64
	Message   string
	RemindAt  time.Time
	Target    string // "user" or "ai"
	Status    string // "pending", "fired", "done"
	CreatedAt time.Time
}

// Memory represents user's persistent memory (max 2000 chars)
type Memory struct {
	UserID    int64
	Content   string
	UpdatedAt time.Time
}

// Task represents a long-running background task
type Task struct {
	ID          string
	UserID      int64
	Description string
	Status      string // running, completed, failed, cancelled
	PID         int
	Output      string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// InitDB opens the database and runs migrations
func InitDB(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db := &DB{sqlDB}

	if err := db.runMigrations(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return db, nil
}

func (db *DB) runMigrations() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY,
		username TEXT,
		first_name TEXT,
		system_prompt TEXT,
		approved BOOLEAN DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS conversations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		title TEXT,
		active BOOLEAN DEFAULT TRUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		conversation_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		tool_calls TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id)
	);

	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		description TEXT NOT NULL,
		status TEXT DEFAULT 'running',
		pid INTEGER,
		output TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		message TEXT NOT NULL,
		remind_at DATETIME NOT NULL,
		target TEXT DEFAULT 'user',
		sent BOOLEAN DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		description TEXT NOT NULL,
		status TEXT DEFAULT 'running',
		pid INTEGER,
		output TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS memory (
		user_id INTEGER PRIMARY KEY,
		content TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE INDEX IF NOT EXISTS idx_conversations_user_active ON conversations(user_id, active);
	CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_reminders_pending ON reminders(sent, remind_at);
	`

	_, err := db.Exec(schema)
	if err != nil {
		return err
	}

	// Migration: add status column to reminders (replaces sent boolean)
	var colCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('reminders') WHERE name='status'`).Scan(&colCount)
	if err != nil {
		return err
	}
	if colCount == 0 {
		_, err = db.Exec(`ALTER TABLE reminders ADD COLUMN status TEXT DEFAULT 'pending'`)
		if err != nil {
			return err
		}
		_, err = db.Exec(`UPDATE reminders SET status = CASE WHEN sent = TRUE THEN 'done' ELSE 'pending' END`)
		if err != nil {
			return err
		}
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_reminders_status ON reminders(status, remind_at)`)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetOrCreateUser retrieves or creates a user
func (db *DB) GetOrCreateUser(telegramID int64, username, firstName string) (*User, bool, error) {
	user := &User{}
	var systemPrompt sql.NullString

	err := db.QueryRow(`
		SELECT id, username, first_name, system_prompt, approved, created_at
		FROM users WHERE id = ?
	`, telegramID).Scan(&user.ID, &user.Username, &user.FirstName, &systemPrompt, &user.Approved, &user.CreatedAt)

	if err == sql.ErrNoRows {
		_, err := db.Exec(`
			INSERT INTO users (id, username, first_name) VALUES (?, ?, ?)
		`, telegramID, username, firstName)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create user: %w", err)
		}
		return db.GetOrCreateUser(telegramID, username, firstName)
	} else if err != nil {
		return nil, false, fmt.Errorf("failed to query user: %w", err)
	}

	if systemPrompt.Valid {
		user.SystemPrompt = systemPrompt.String
	}

	isNew := user.CreatedAt.After(time.Now().Add(-5 * time.Second))
	return user, isNew, nil
}

// ApproveUser marks a user as approved
func (db *DB) ApproveUser(userID int64) error {
	_, err := db.Exec(`UPDATE users SET approved = TRUE WHERE id = ?`, userID)
	return err
}

// GetUser retrieves a user by ID
func (db *DB) GetUser(userID int64) (*User, error) {
	user := &User{}
	var systemPrompt sql.NullString

	err := db.QueryRow(`
		SELECT id, username, first_name, system_prompt, approved, created_at
		FROM users WHERE id = ?
	`, userID).Scan(&user.ID, &user.Username, &user.FirstName, &systemPrompt, &user.Approved, &user.CreatedAt)

	if err != nil {
		return nil, err
	}

	if systemPrompt.Valid {
		user.SystemPrompt = systemPrompt.String
	}

	return user, nil
}

// UpdateUserSystemPrompt updates the user's system prompt
func (db *DB) UpdateUserSystemPrompt(userID int64, prompt string) error {
	var p any = prompt
	if prompt == "" {
		p = nil
	}
	_, err := db.Exec(`UPDATE users SET system_prompt = ? WHERE id = ?`, p, userID)
	return err
}

// GetActiveConversation gets the active conversation or creates a new one
func (db *DB) GetActiveConversation(userID int64) (*Conversation, error) {
	conv := &Conversation{}
	var title sql.NullString

	err := db.QueryRow(`
		SELECT id, user_id, title, active, created_at
		FROM conversations
		WHERE user_id = ? AND active = TRUE
		ORDER BY created_at DESC LIMIT 1
	`, userID).Scan(&conv.ID, &conv.UserID, &title, &conv.Active, &conv.CreatedAt)

	if err == sql.ErrNoRows {
		return db.CreateConversation(userID, "")
	} else if err != nil {
		return nil, fmt.Errorf("failed to query active conversation: %w", err)
	}

	if title.Valid {
		conv.Title = title.String
	}

	return conv, nil
}

// CreateConversation creates a new conversation
func (db *DB) CreateConversation(userID int64, title string) (*Conversation, error) {
	// Deactivate existing conversations
	_, err := db.Exec(`UPDATE conversations SET active = FALSE WHERE user_id = ? AND active = TRUE`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to deactivate conversations: %w", err)
	}

	var titlePtr any = nil
	if title != "" {
		titlePtr = title
	}

	result, err := db.Exec(`
		INSERT INTO conversations (user_id, title, active) VALUES (?, ?, TRUE)
	`, userID, titlePtr)
	if err != nil {
		return nil, fmt.Errorf("failed to create conversation: %w", err)
	}

	id, _ := result.LastInsertId()
	return &Conversation{
		ID:        id,
		UserID:    userID,
		Title:     title,
		Active:    true,
		CreatedAt: time.Now(),
	}, nil
}

// GetConversationMessages retrieves recent messages for context
func (db *DB) GetConversationMessages(convID int64, limit int) ([]Message, error) {
	rows, err := db.Query(`
		SELECT id, conversation_id, role, content, tool_calls, created_at
		FROM messages
		WHERE conversation_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, convID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.ConversationID, &msg.Role, &msg.Content, &msg.ToolCalls, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		messages = append([]Message{msg}, messages...) // Prepend to reverse order
	}

	return messages, rows.Err()
}

// SaveMessage saves a message to the database
func (db *DB) SaveMessage(convID int64, role, content string, toolCalls *string) error {
	_, err := db.Exec(`
		INSERT INTO messages (conversation_id, role, content, tool_calls) VALUES (?, ?, ?, ?)
	`, convID, role, content, toolCalls)
	return err
}

// GetUserConversations retrieves conversations for a user
func (db *DB) GetUserConversations(userID int64, limit int) ([]Conversation, error) {
	rows, err := db.Query(`
		SELECT c.id, c.user_id, c.title, c.active, c.created_at,
			(SELECT COUNT(*) FROM messages WHERE conversation_id = c.id) as msg_count
		FROM conversations c
		WHERE c.user_id = ?
		ORDER BY c.created_at DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query conversations: %w", err)
	}
	defer rows.Close()

	var conversations []Conversation
	for rows.Next() {
		var conv Conversation
		var title sql.NullString
		var msgCount int
		if err := rows.Scan(&conv.ID, &conv.UserID, &title, &conv.Active, &conv.CreatedAt, &msgCount); err != nil {
			return nil, fmt.Errorf("failed to scan conversation: %w", err)
		}
		if title.Valid {
			conv.Title = title.String
		} else {
			conv.Title = fmt.Sprintf("Conversation #%d (%d msgs)", conv.ID, msgCount)
		}
		conversations = append(conversations, conv)
	}

	return conversations, rows.Err()
}

// GetPendingReminders retrieves pending reminders that are due
func (db *DB) GetPendingReminders() ([]Reminder, error) {
	rows, err := db.Query(`
		SELECT id, user_id, message, remind_at, target, COALESCE(status, 'pending'), created_at
		FROM reminders
		WHERE (status = 'pending' OR (status IS NULL AND sent = FALSE)) AND remind_at <= ?
	`, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to query pending reminders: %w", err)
	}
	defer rows.Close()

	var reminders []Reminder
	for rows.Next() {
		var r Reminder
		var target sql.NullString
		if err := rows.Scan(&r.ID, &r.UserID, &r.Message, &r.RemindAt, &target, &r.Status, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan reminder: %w", err)
		}
		r.Target = "user"
		if target.Valid {
			r.Target = target.String
		}
		reminders = append(reminders, r)
	}

	return reminders, rows.Err()
}

// MarkReminderFired marks a reminder as fired (being processed by brain)
func (db *DB) MarkReminderFired(reminderID int64) error {
	_, err := db.Exec(`UPDATE reminders SET status = 'fired', sent = TRUE WHERE id = ?`, reminderID)
	return err
}

// RescheduleReminder resets a reminder to pending with a new time
func (db *DB) RescheduleReminder(reminderID int64, newTime time.Time) error {
	_, err := db.Exec(`UPDATE reminders SET remind_at = ?, status = 'pending', sent = FALSE WHERE id = ?`, newTime, reminderID)
	return err
}

// DismissReminder marks a reminder as done (soft delete)
func (db *DB) DismissReminder(reminderID int64) error {
	_, err := db.Exec(`UPDATE reminders SET status = 'done', sent = TRUE WHERE id = ?`, reminderID)
	return err
}

// GetUserReminders retrieves active reminders for a user (pending + fired)
func (db *DB) GetUserReminders(userID int64) ([]Reminder, error) {
	rows, err := db.Query(`
		SELECT id, user_id, message, remind_at, target, COALESCE(status, 'pending'), created_at
		FROM reminders
		WHERE user_id = ? AND COALESCE(status, CASE WHEN sent THEN 'done' ELSE 'pending' END) IN ('pending', 'fired')
		ORDER BY remind_at ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query user reminders: %w", err)
	}
	defer rows.Close()

	var reminders []Reminder
	for rows.Next() {
		var r Reminder
		var target sql.NullString
		if err := rows.Scan(&r.ID, &r.UserID, &r.Message, &r.RemindAt, &target, &r.Status, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan reminder: %w", err)
		}
		r.Target = "user"
		if target.Valid {
			r.Target = target.String
		}
		reminders = append(reminders, r)
	}

	return reminders, rows.Err()
}

// GetUserMemory retrieves the user's memory
func (db *DB) GetUserMemory(userID int64) (string, error) {
	var content sql.NullString
	err := db.QueryRow(`SELECT content FROM memory WHERE user_id = ?`, userID).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get memory: %w", err)
	}
	if content.Valid {
		return content.String, nil
	}
	return "", nil
}

// UpdateUserMemory updates the user's memory (max 2000 chars)
func (db *DB) UpdateUserMemory(userID int64, content string) error {
	if len(content) > 2000 {
		return fmt.Errorf("memory content exceeds 2000 characters (got %d)", len(content))
	}
	_, err := db.Exec(`
		INSERT INTO memory (user_id, content, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_id) DO UPDATE SET content = ?, updated_at = CURRENT_TIMESTAMP
	`, userID, content, content)
	return err
}

// CreateTask creates a new task
func (db *DB) CreateTask(id string, userID int64, description string, pid int) error {
	_, err := db.Exec(`
		INSERT INTO tasks (id, user_id, description, status, pid) VALUES (?, ?, ?, 'running', ?)
	`, id, userID, description, pid)
	return err
}

// GetTask retrieves a task by ID
func (db *DB) GetTask(id string) (*Task, error) {
	task := &Task{}
	var output sql.NullString
	var completedAt sql.NullTime

	err := db.QueryRow(`
		SELECT id, user_id, description, status, pid, output, created_at, completed_at
		FROM tasks WHERE id = ?
	`, id).Scan(&task.ID, &task.UserID, &task.Description, &task.Status, &task.PID, &output, &task.CreatedAt, &completedAt)

	if err != nil {
		return nil, err
	}

	if output.Valid {
		task.Output = output.String
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	return task, nil
}

// GetRunningTasks retrieves all running tasks
func (db *DB) GetRunningTasks() ([]Task, error) {
	rows, err := db.Query(`
		SELECT id, user_id, description, status, pid, output, created_at, completed_at
		FROM tasks WHERE status = 'running'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		var output sql.NullString
		var completedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.UserID, &t.Description, &t.Status, &t.PID, &output, &t.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if output.Valid {
			t.Output = output.String
		}
		if completedAt.Valid {
			t.CompletedAt = &completedAt.Time
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// GetUserTasks retrieves tasks for a user
func (db *DB) GetUserTasks(userID int64) ([]Task, error) {
	rows, err := db.Query(`
		SELECT id, user_id, description, status, pid, output, created_at, completed_at
		FROM tasks WHERE user_id = ?
		ORDER BY created_at DESC LIMIT 10
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		var output sql.NullString
		var completedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.UserID, &t.Description, &t.Status, &t.PID, &output, &t.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if output.Valid {
			t.Output = output.String
		}
		if completedAt.Valid {
			t.CompletedAt = &completedAt.Time
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// UpdateTaskStatus updates a task's status and optionally its output
func (db *DB) UpdateTaskStatus(id, status, output string) error {
	if status == "completed" || status == "failed" || status == "cancelled" {
		_, err := db.Exec(`
			UPDATE tasks SET status = ?, output = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?
		`, status, output, id)
		return err
	}
	_, err := db.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, status, id)
	return err
}

// UpdateTaskPID updates a task's process ID
func (db *DB) UpdateTaskPID(id string, pid int) error {
	_, err := db.Exec(`UPDATE tasks SET pid = ? WHERE id = ?`, pid, id)
	return err
}
