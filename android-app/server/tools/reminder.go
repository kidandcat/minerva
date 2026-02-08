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
	Target   string `json:"target"`
}

type DeleteReminderArgs struct {
	ID int64 `json:"id"`
}

type RescheduleReminderArgs struct {
	ID       int64  `json:"id"`
	RemindAt string `json:"remind_at"`
}

func CreateReminder(db *sql.DB, userID int64, arguments string) (string, error) {
	var args CreateReminderArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Message == "" {
		return "", fmt.Errorf("message is required")
	}
	if args.RemindAt == "" {
		return "", fmt.Errorf("remind_at is required")
	}

	remindAt, err := time.Parse(time.RFC3339, args.RemindAt)
	if err != nil {
		return "", fmt.Errorf("invalid remind_at format (use ISO8601): %w", err)
	}

	target := "user"
	if args.Target != "" {
		target = args.Target
	}

	result, err := db.Exec(
		"INSERT INTO reminders (user_id, message, remind_at, target, status) VALUES (?, ?, ?, ?, 'pending')",
		userID, args.Message, remindAt.Format(time.RFC3339), target,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create reminder: %w", err)
	}

	id, _ := result.LastInsertId()
	response := map[string]any{
		"success":   true,
		"id":        id,
		"message":   args.Message,
		"remind_at": remindAt.Format(time.RFC3339),
		"target":    target,
	}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}

func ListReminders(db *sql.DB, userID int64) (string, error) {
	rows, err := db.Query(`
		SELECT id, message, remind_at, target, status
		FROM reminders WHERE user_id = ? AND status IN ('pending', 'fired')
		ORDER BY remind_at ASC
	`, userID)
	if err != nil {
		return "", fmt.Errorf("failed to list reminders: %w", err)
	}
	defer rows.Close()

	var reminders []map[string]any
	for rows.Next() {
		var id int64
		var message, remindAt, target, status string
		if err := rows.Scan(&id, &message, &remindAt, &target, &status); err != nil {
			return "", err
		}
		reminders = append(reminders, map[string]any{
			"id": id, "message": message, "remind_at": remindAt,
			"target": target, "status": status,
		})
	}

	response := map[string]any{"success": true, "reminders": reminders, "count": len(reminders)}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}

func DeleteReminder(db *sql.DB, userID int64, arguments string) (string, error) {
	var args DeleteReminderArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	result, err := db.Exec("UPDATE reminders SET status = 'done' WHERE id = ? AND user_id = ?", args.ID, userID)
	if err != nil {
		return "", fmt.Errorf("failed to delete reminder: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return "", fmt.Errorf("reminder not found")
	}

	response := map[string]any{"success": true, "message": "Reminder dismissed"}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}

func RescheduleReminder(db *sql.DB, userID int64, arguments string) (string, error) {
	var args RescheduleReminderArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	newTime, err := time.Parse(time.RFC3339, args.RemindAt)
	if err != nil {
		return "", fmt.Errorf("invalid remind_at format: %w", err)
	}

	result, err := db.Exec(
		"UPDATE reminders SET remind_at = ?, status = 'pending' WHERE id = ? AND user_id = ?",
		newTime.Format(time.RFC3339), args.ID, userID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to reschedule: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return "", fmt.Errorf("reminder not found")
	}

	response := map[string]any{"success": true, "message": "Reminder rescheduled", "new_time": newTime.Format(time.RFC3339)}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}
