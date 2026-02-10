package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

// ScheduledTask represents a task scheduled for autonomous execution
type ScheduledTask struct {
	ID          int64
	Description string
	ScheduledAt time.Time
	AgentName   string
	WorkingDir  string
	Status      string // pending, running, completed, failed
	Result      string
	CreatedAt   time.Time
	Recurring   string // none, daily, weekly, monthly
	LastRunAt   *time.Time
}

// InitScheduleTable creates the scheduled_tasks table
func (db *DB) InitScheduleTable() error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS scheduled_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			description TEXT NOT NULL,
			scheduled_at DATETIME NOT NULL,
			agent_name TEXT NOT NULL DEFAULT '',
			working_dir TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			result TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			recurring TEXT NOT NULL DEFAULT 'none',
			last_run_at DATETIME
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create scheduled_tasks table: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_status ON scheduled_tasks(status, scheduled_at)`)
	return err
}

// CreateScheduledTask creates a new scheduled task
func (db *DB) CreateScheduledTask(description string, scheduledAt time.Time, agentName, workingDir, recurring string) (int64, error) {
	if recurring == "" {
		recurring = "none"
	}

	result, err := db.Exec(`
		INSERT INTO scheduled_tasks (description, scheduled_at, agent_name, working_dir, recurring)
		VALUES (?, ?, ?, ?, ?)
	`, description, scheduledAt.Format(time.RFC3339), agentName, workingDir, recurring)
	if err != nil {
		return 0, fmt.Errorf("failed to create scheduled task: %w", err)
	}

	id, _ := result.LastInsertId()
	return id, nil
}

// GetPendingScheduledTasks retrieves pending tasks that are due
func (db *DB) GetPendingScheduledTasks() ([]ScheduledTask, error) {
	rows, err := db.Query(`
		SELECT id, description, scheduled_at, agent_name, working_dir, status, result, created_at, recurring, last_run_at
		FROM scheduled_tasks
		WHERE status = 'pending' AND scheduled_at <= ?
	`, time.Now().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("failed to query pending scheduled tasks: %w", err)
	}
	defer rows.Close()

	return scanScheduledTasks(rows)
}

// GetScheduledTasks retrieves all active scheduled tasks (pending + running)
func (db *DB) GetScheduledTasks() ([]ScheduledTask, error) {
	rows, err := db.Query(`
		SELECT id, description, scheduled_at, agent_name, working_dir, status, result, created_at, recurring, last_run_at
		FROM scheduled_tasks
		WHERE status IN ('pending', 'running')
		ORDER BY scheduled_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query scheduled tasks: %w", err)
	}
	defer rows.Close()

	return scanScheduledTasks(rows)
}

// GetScheduledTask retrieves a single scheduled task by ID
func (db *DB) GetScheduledTask(id int64) (*ScheduledTask, error) {
	var t ScheduledTask
	var scheduledAtStr, createdAtStr string
	var result sql.NullString
	var lastRunAt sql.NullString

	err := db.QueryRow(`
		SELECT id, description, scheduled_at, agent_name, working_dir, status, result, created_at, recurring, last_run_at
		FROM scheduled_tasks WHERE id = ?
	`, id).Scan(&t.ID, &t.Description, &scheduledAtStr, &t.AgentName, &t.WorkingDir, &t.Status, &result, &createdAtStr, &t.Recurring, &lastRunAt)
	if err != nil {
		return nil, err
	}

	t.ScheduledAt, _ = time.Parse(time.RFC3339, scheduledAtStr)
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	if result.Valid {
		t.Result = result.String
	}
	if lastRunAt.Valid {
		parsed, _ := time.Parse(time.RFC3339, lastRunAt.String)
		t.LastRunAt = &parsed
	}

	return &t, nil
}

// UpdateScheduledTaskStatus updates a task's status and optionally its result
func (db *DB) UpdateScheduledTaskStatus(id int64, status, result string) error {
	if status == "completed" || status == "failed" {
		_, err := db.Exec(`
			UPDATE scheduled_tasks SET status = ?, result = ?, last_run_at = ? WHERE id = ?
		`, status, result, time.Now().Format(time.RFC3339), id)
		return err
	}
	_, err := db.Exec(`UPDATE scheduled_tasks SET status = ? WHERE id = ?`, status, id)
	return err
}

// RescheduleTask reschedules a recurring task for its next run
func (db *DB) RescheduleTask(id int64, nextRun time.Time) error {
	_, err := db.Exec(`
		UPDATE scheduled_tasks SET scheduled_at = ?, status = 'pending' WHERE id = ?
	`, nextRun.Format(time.RFC3339), id)
	return err
}

// DeleteScheduledTask deletes a scheduled task
func (db *DB) DeleteScheduledTask(id int64) error {
	result, err := db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete scheduled task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("scheduled task not found")
	}
	return nil
}

// NextRecurringTime calculates the next run time for a recurring task
func NextRecurringTime(current time.Time, recurring string) time.Time {
	switch recurring {
	case "daily":
		return current.Add(24 * time.Hour)
	case "weekly":
		return current.Add(7 * 24 * time.Hour)
	case "monthly":
		return current.AddDate(0, 1, 0)
	default:
		return time.Time{}
	}
}

// Scheduler runs scheduled tasks
type Scheduler struct {
	db       *DB
	bot      *Bot
	agentHub *AgentHub
	stop     chan struct{}
}

// NewScheduler creates a new scheduler
func NewScheduler(db *DB, bot *Bot, agentHub *AgentHub) *Scheduler {
	return &Scheduler{
		db:       db,
		bot:      bot,
		agentHub: agentHub,
		stop:     make(chan struct{}),
	}
}

// Start begins the scheduler loop
func (s *Scheduler) Start() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		log.Println("Scheduler started")

		for {
			select {
			case <-ticker.C:
				s.checkTasks()
			case <-s.stop:
				return
			}
		}
	}()
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	close(s.stop)
}

func (s *Scheduler) checkTasks() {
	tasks, err := s.db.GetPendingScheduledTasks()
	if err != nil {
		log.Printf("[Scheduler] Error getting pending tasks: %v", err)
		return
	}

	for _, task := range tasks {
		// Mark as running first to prevent double-processing
		if err := s.db.UpdateScheduledTaskStatus(task.ID, "running", ""); err != nil {
			log.Printf("[Scheduler] Failed to mark task %d as running: %v", task.ID, err)
			continue
		}

		go s.executeTask(task)
	}
}

func (s *Scheduler) executeTask(task ScheduledTask) {
	log.Printf("[Scheduler] Executing task %d: %s (agent: %s, dir: %s)", task.ID, task.Description, task.AgentName, task.WorkingDir)

	// Notify user about task starting
	s.bot.sendMessage(s.bot.config.AdminID, fmt.Sprintf("⏰ Scheduled task starting:\n*%s*\nAgent: %s", task.Description, task.AgentName))

	if s.agentHub == nil {
		s.db.UpdateScheduledTaskStatus(task.ID, "failed", "agent hub not available")
		s.bot.sendMessage(s.bot.config.AdminID, fmt.Sprintf("❌ Scheduled task failed: %s\nError: agent hub not available", task.Description))
		s.handleRecurring(task)
		return
	}

	// Check if agent is connected
	agents := s.agentHub.ListAgents()
	agentFound := false
	for _, a := range agents {
		if a["name"] == task.AgentName {
			agentFound = true
			break
		}
	}

	if !agentFound {
		errMsg := fmt.Sprintf("agent '%s' not connected", task.AgentName)
		s.db.UpdateScheduledTaskStatus(task.ID, "failed", errMsg)
		s.bot.sendMessage(s.bot.config.AdminID, fmt.Sprintf("❌ Scheduled task failed: %s\nError: %s", task.Description, errMsg))
		s.handleRecurring(task)
		return
	}

	// Send task to agent
	taskID, err := s.agentHub.SendTask(task.AgentName, task.Description, task.WorkingDir)
	if err != nil {
		s.db.UpdateScheduledTaskStatus(task.ID, "failed", err.Error())
		s.bot.sendMessage(s.bot.config.AdminID, fmt.Sprintf("❌ Scheduled task failed: %s\nError: %v", task.Description, err))
		s.handleRecurring(task)
		return
	}

	// Task sent successfully - mark completed (result will come async via agent result handler)
	s.db.UpdateScheduledTaskStatus(task.ID, "completed", fmt.Sprintf("dispatched as agent task %s", taskID))
	log.Printf("[Scheduler] Task %d dispatched as agent task %s", task.ID, taskID)

	s.handleRecurring(task)
}

func (s *Scheduler) handleRecurring(task ScheduledTask) {
	if task.Recurring == "none" || task.Recurring == "" {
		return
	}

	nextRun := NextRecurringTime(task.ScheduledAt, task.Recurring)
	if nextRun.IsZero() {
		return
	}

	// If next run is in the past (e.g., task was delayed), advance until it's in the future
	for nextRun.Before(time.Now()) {
		nextRun = NextRecurringTime(nextRun, task.Recurring)
	}

	newID, err := s.db.CreateScheduledTask(task.Description, nextRun, task.AgentName, task.WorkingDir, task.Recurring)
	if err != nil {
		log.Printf("[Scheduler] Failed to reschedule recurring task %d: %v", task.ID, err)
		return
	}

	log.Printf("[Scheduler] Recurring task %d rescheduled as %d for %s", task.ID, newID, nextRun.Format(time.RFC3339))
}

func scanScheduledTasks(rows *sql.Rows) ([]ScheduledTask, error) {
	var tasks []ScheduledTask
	for rows.Next() {
		var t ScheduledTask
		var scheduledAtStr, createdAtStr string
		var result sql.NullString
		var lastRunAt sql.NullString

		if err := rows.Scan(&t.ID, &t.Description, &scheduledAtStr, &t.AgentName, &t.WorkingDir, &t.Status, &result, &createdAtStr, &t.Recurring, &lastRunAt); err != nil {
			return nil, fmt.Errorf("failed to scan scheduled task: %w", err)
		}

		t.ScheduledAt, _ = time.Parse(time.RFC3339, scheduledAtStr)
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		if result.Valid {
			t.Result = result.String
		}
		if lastRunAt.Valid {
			parsed, _ := time.Parse(time.RFC3339, lastRunAt.String)
			t.LastRunAt = &parsed
		}

		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
