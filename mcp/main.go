package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

// MCP JSON-RPC types
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var db *sql.DB
var userID int64 = 1 // Default user ID for Minerva

func main() {
	log.SetOutput(os.Stderr)

	// Open database
	dbPath := os.Getenv("MINERVA_DB")
	if dbPath == "" {
		dbPath = "./minerva.db"
	}

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Read from stdin, write to stdout
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			log.Printf("Failed to parse request: %v", err)
			continue
		}

		resp := handleRequest(req)
		out, _ := json.Marshal(resp)
		fmt.Println(string(out))
	}
}

func handleRequest(req Request) Response {
	switch req.Method {
	case "initialize":
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "minerva-mcp",
					"version": "1.0.0",
				},
			},
		}

	case "tools/list":
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": getTools(),
			},
		}

	case "tools/call":
		var params ToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errorResponse(req.ID, -32602, "Invalid params")
		}
		result, err := callTool(params.Name, params.Arguments)
		if err != nil {
			return Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"content": []TextContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
					"isError": true,
				},
			}
		}
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []TextContent{{Type: "text", Text: result}},
			},
		}

	default:
		return errorResponse(req.ID, -32601, "Method not found")
	}
}

func errorResponse(id any, code int, message string) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}
}

func getTools() []Tool {
	return []Tool{
		{
			Name:        "update_memory",
			Description: "Update the user's persistent memory. Use this to store important information about the user that should be remembered across conversations.",
			InputSchema: map[string]any{
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
		{
			Name:        "get_memory",
			Description: "Get the user's current persistent memory",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

func callTool(name string, args map[string]any) (string, error) {
	switch name {
	case "update_memory":
		content, _ := args["content"].(string)
		if content == "" {
			return "", fmt.Errorf("content is required")
		}
		_, err := db.Exec(
			"INSERT OR REPLACE INTO user_memory (user_id, content, updated_at) VALUES (?, ?, datetime('now'))",
			userID, content,
		)
		if err != nil {
			return "", err
		}
		return "Memory updated", nil

	case "get_memory":
		var content string
		err := db.QueryRow("SELECT content FROM user_memory WHERE user_id = ?", userID).Scan(&content)
		if err == sql.ErrNoRows {
			return "No memory stored yet", nil
		}
		if err != nil {
			return "", err
		}
		return content, nil

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

