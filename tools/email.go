package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SendEmailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	From    string `json:"from,omitempty"`
}

var verifiedDomains []string

func SetVerifiedDomains(domains []string) {
	verifiedDomains = domains
}

func GetVerifiedDomains() []string {
	return verifiedDomains
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
var fromEmail string

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

	// Determine sender address
	sender := fromEmail
	if sender == "" && args.From == "" {
		return "", fmt.Errorf("no sender address configured (set FROM_EMAIL in .env or provide 'from' parameter)")
	}
	if args.From != "" {
		// Extract domain from the from address (handle "Name <email>" format)
		addr := args.From
		if idx := strings.Index(addr, "<"); idx != -1 {
			addr = strings.TrimRight(addr[idx+1:], ">")
		}
		parts := strings.Split(addr, "@")
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid 'from' address: %s", args.From)
		}
		domain := strings.ToLower(parts[1])
		if len(verifiedDomains) > 0 {
			valid := false
			for _, d := range verifiedDomains {
				if domain == d {
					valid = true
					break
				}
			}
			if !valid {
				return "", fmt.Errorf("domain %q is not verified in Resend (allowed: %s)", domain, strings.Join(verifiedDomains, ", "))
			}
		}
		sender = args.From
	}

	// Prepare request
	reqBody := ResendRequest{
		From:    sender,
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

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		var resendErr ResendError
		json.Unmarshal(body, &resendErr)
		if resendErr.Message != "" {
			return "", fmt.Errorf("failed to send email (status %d): %s", resp.StatusCode, resendErr.Message)
		}
		return "", fmt.Errorf("failed to send email (status %d): %s", resp.StatusCode, string(body))
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
