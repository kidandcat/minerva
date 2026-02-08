package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type UpdateMemoryArgs struct {
	Content string `json:"content"`
}

func GetMemory(db *sql.DB, userID int64) (string, error) {
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

func UpdateMemory(db *sql.DB, userID int64, arguments string) (string, error) {
	var args UpdateMemoryArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Content == "" {
		return "", fmt.Errorf("content is required")
	}
	if len(args.Content) > 2000 {
		return "", fmt.Errorf("content exceeds 2000 characters (got %d)", len(args.Content))
	}

	_, err := db.Exec(`
		INSERT INTO memory (user_id, content, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_id) DO UPDATE SET content = ?, updated_at = CURRENT_TIMESTAMP
	`, userID, args.Content, args.Content)
	if err != nil {
		return "", fmt.Errorf("failed to update memory: %w", err)
	}

	response := map[string]any{"success": true, "message": "Memory updated", "length": len(args.Content)}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}
