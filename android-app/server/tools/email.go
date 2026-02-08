package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

var resendAPIKey string

func SetResendAPIKey(key string) {
	resendAPIKey = key
}

type SendEmailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func SendEmail(arguments string) (string, error) {
	if resendAPIKey == "" {
		return "", fmt.Errorf("email not configured (RESEND_API_KEY not set)")
	}

	var args SendEmailArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.To == "" {
		return "", fmt.Errorf("to is required")
	}
	if args.Subject == "" {
		return "", fmt.Errorf("subject is required")
	}
	if args.Body == "" {
		return "", fmt.Errorf("body is required")
	}

	reqBody := map[string]any{
		"from":    "Minerva <minerva@jairo.cloud>",
		"to":      []string{args.To},
		"subject": args.Subject,
		"text":    args.Body,
	}

	jsonBody, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+resendAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send email: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("resend API error (%d): %s", resp.StatusCode, string(body))
	}

	response := map[string]any{"success": true, "message": fmt.Sprintf("Email sent to %s", args.To)}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}
