package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type CreateReminderArgs struct {
	Message  string `json:"message"`
	RemindAt string `json:"remind_at"`
	Target   string `json:"target"` // "user" or "ai", default "user"
}

type DeleteReminderArgs struct {
	ID int64 `json:"id"`
}

type ReminderResult struct {
	ID        int64  `json:"id"`
	Message   string `json:"message"`
	RemindAt  string `json:"remind_at"`
	CreatedAt string `json:"created_at"`
}

func CreateReminder(db *sql.DB, userID int64, arguments string) (string, error) {
	var args CreateReminderArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	remindAt, err := time.Parse(time.RFC3339, args.RemindAt)
	if err != nil {
		return "", fmt.Errorf("invalid remind_at format, use ISO8601 (e.g., 2024-01-15T14:30:00Z): %w", err)
	}

	if remindAt.Before(time.Now()) {
		return "", fmt.Errorf("remind_at must be in the future")
	}

	// Default target to "user"
	target := args.Target
	if target == "" {
		target = "user"
	}
	if target != "user" && target != "ai" {
		return "", fmt.Errorf("target must be 'user' or 'ai'")
	}

	result, err := db.Exec(
		"INSERT INTO reminders (user_id, message, remind_at, target) VALUES (?, ?, ?, ?)",
		userID, args.Message, remindAt, target,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create reminder: %w", err)
	}

	id, _ := result.LastInsertId()

	targetDesc := "you directly"
	if target == "ai" {
		targetDesc = "me (AI) to process"
	}

	response := map[string]any{
		"success": true,
		"id":      id,
		"target":  target,
		"message": fmt.Sprintf("Reminder created for %s, will be sent to %s", remindAt.Format("Jan 2, 2006 at 15:04"), targetDesc),
	}

	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}

func ListReminders(db *sql.DB, userID int64) (string, error) {
	rows, err := db.Query(`
		SELECT id, message, remind_at, created_at
		FROM reminders
		WHERE user_id = ? AND sent = FALSE
		ORDER BY remind_at ASC
	`, userID)
	if err != nil {
		return "", fmt.Errorf("failed to list reminders: %w", err)
	}
	defer rows.Close()

	var reminders []ReminderResult
	for rows.Next() {
		var r ReminderResult
		var remindAt, createdAt time.Time
		if err := rows.Scan(&r.ID, &r.Message, &remindAt, &createdAt); err != nil {
			return "", fmt.Errorf("failed to scan reminder: %w", err)
		}
		r.RemindAt = remindAt.Format(time.RFC3339)
		r.CreatedAt = createdAt.Format(time.RFC3339)
		reminders = append(reminders, r)
	}

	response := map[string]any{
		"success":   true,
		"reminders": reminders,
		"count":     len(reminders),
	}

	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}

func DeleteReminder(db *sql.DB, userID int64, arguments string) (string, error) {
	var args DeleteReminderArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	result, err := db.Exec(
		"DELETE FROM reminders WHERE id = ? AND user_id = ?",
		args.ID, userID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to delete reminder: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return "", fmt.Errorf("reminder not found")
	}

	response := map[string]any{
		"success": true,
		"message": "Reminder deleted",
	}

	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}
