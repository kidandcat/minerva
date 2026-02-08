package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SaveNoteArgs struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type SearchNotesArgs struct {
	Query string `json:"query"`
}

type NoteResult struct {
	ID        int64  `json:"id"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func SaveNote(db *sql.DB, userID int64, arguments string) (string, error) {
	var args SaveNoteArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.Content == "" {
		return "", fmt.Errorf("content cannot be empty")
	}

	var titlePtr any = nil
	if args.Title != "" {
		titlePtr = args.Title
	}

	result, err := db.Exec(
		"INSERT INTO notes (user_id, title, content) VALUES (?, ?, ?)",
		userID, titlePtr, args.Content,
	)
	if err != nil {
		return "", fmt.Errorf("failed to save note: %w", err)
	}

	id, _ := result.LastInsertId()
	response := map[string]any{"success": true, "id": id, "message": "Note saved"}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}

func SearchNotes(db *sql.DB, userID int64, arguments string) (string, error) {
	var args SearchNotesArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.Query == "" {
		return "", fmt.Errorf("query cannot be empty")
	}

	searchPattern := "%" + strings.ToLower(args.Query) + "%"
	rows, err := db.Query(`
		SELECT id, title, content, created_at
		FROM notes
		WHERE user_id = ? AND (LOWER(title) LIKE ? OR LOWER(content) LIKE ?)
		ORDER BY created_at DESC
	`, userID, searchPattern, searchPattern)
	if err != nil {
		return "", fmt.Errorf("failed to search notes: %w", err)
	}
	defer rows.Close()

	var notes []NoteResult
	for rows.Next() {
		var n NoteResult
		var title sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&n.ID, &title, &n.Content, &createdAt); err != nil {
			return "", fmt.Errorf("failed to scan note: %w", err)
		}
		if title.Valid {
			n.Title = title.String
		}
		n.CreatedAt = createdAt.Format(time.RFC3339)
		notes = append(notes, n)
	}

	response := map[string]any{"success": true, "notes": notes, "count": len(notes)}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}

func ListNotes(db *sql.DB, userID int64) (string, error) {
	rows, err := db.Query(`
		SELECT id, title, content, created_at
		FROM notes
		WHERE user_id = ?
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return "", fmt.Errorf("failed to list notes: %w", err)
	}
	defer rows.Close()

	var notes []NoteResult
	for rows.Next() {
		var n NoteResult
		var title sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&n.ID, &title, &n.Content, &createdAt); err != nil {
			return "", fmt.Errorf("failed to scan note: %w", err)
		}
		if title.Valid {
			n.Title = title.String
		}
		n.CreatedAt = createdAt.Format(time.RFC3339)
		notes = append(notes, n)
	}

	response := map[string]any{"success": true, "notes": notes, "count": len(notes)}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}
