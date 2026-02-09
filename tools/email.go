package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type SendEmailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type ResendRequest struct {
	From    string `json:"from"`
	To      []string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

type ResendResponse struct {
	ID string `json:"id"`
}

type ResendError struct {
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
	Name       string `json:"name"`
}

var resendAPIKey string
var fromEmail = "Minerva <minerva@example.com>"

func SetResendAPIKey(key string) {
	resendAPIKey = key
}

func SetFromEmail(email string) {
	fromEmail = email
}

func SendEmail(arguments string) (string, error) {
	var args SendEmailArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.To == "" {
		return "", fmt.Errorf("'to' email address is required")
	}
	if args.Subject == "" {
		return "", fmt.Errorf("'subject' is required")
	}
	if args.Body == "" {
		return "", fmt.Errorf("'body' is required")
	}

	if resendAPIKey == "" {
		return "", fmt.Errorf("Resend API key not configured")
	}

	// Prepare request
	reqBody := ResendRequest{
		From:    fromEmail,
		To:      []string{args.To},
		Subject: args.Subject,
		Text:    args.Body,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Send request
	req, err := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+resendAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		var resendErr ResendError
		json.Unmarshal(body, &resendErr)
		return "", fmt.Errorf("Resend API error: %s", resendErr.Message)
	}

	var resendResp ResendResponse
	if err := json.Unmarshal(body, &resendResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	response := map[string]any{
		"success": true,
		"id":      resendResp.ID,
		"message": fmt.Sprintf("Email sent to %s", args.To),
	}

	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}
