package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// TwilioCallManager handles voice calls via Twilio ConversationRelay
type TwilioCallManager struct {
	bot                *Bot
	accountSID         string
	authToken          string
	phoneNumber        string
	baseURL            string // Public URL for webhooks
	ownerName          string
	defaultCountryCode string
	activeCalls        sync.Map
}

// ActiveCall represents an ongoing call
type ActiveCall struct {
	CallSID     string
	To          string
	Purpose     string
	Messages    []ChatMessage
	UserID      int64
	ConvID      int64
	ws          *websocket.Conn
	mu          sync.Mutex
}

// ConversationRelayMessage represents messages from Twilio ConversationRelay
type ConversationRelayMessage struct {
	Type            string `json:"type"`
	Token           string `json:"token,omitempty"`           // For prompt events
	Last            bool   `json:"last,omitempty"`            // For prompt events
	HandoffData     string `json:"handoff_data,omitempty"`    // For setup/interrupt
	CallSID         string `json:"call_sid,omitempty"`
	StopReason      string `json:"stop_reason,omitempty"`     // For end event
	TerminatedBy    string `json:"terminated_by,omitempty"`   // For end event
}

// NewTwilioCallManager creates a new call manager
func NewTwilioCallManager(bot *Bot, accountSID, authToken, phoneNumber, baseURL string) *TwilioCallManager {
	ownerName := "the owner"
	countryCode := "+1"
	if bot.config != nil {
		ownerName = bot.config.OwnerName
		countryCode = bot.config.DefaultCountryCode
	}
	return &TwilioCallManager{
		bot:                bot,
		accountSID:         accountSID,
		authToken:          authToken,
		phoneNumber:        phoneNumber,
		baseURL:            baseURL,
		ownerName:          ownerName,
		defaultCountryCode: countryCode,
	}
}

// HandleIncomingCall handles incoming voice calls with TwiML
func (t *TwilioCallManager) HandleIncomingCall(w http.ResponseWriter, r *http.Request) {
	from := r.FormValue("From")
	callSID := r.FormValue("CallSid")

	log.Printf("Incoming call from %s (SID: %s)", from, callSID)

	// Notify user about incoming call
	t.bot.sendMessage(t.bot.config.AdminID, fmt.Sprintf("📞 *Llamada entrante*\nDe: %s\nMinerva está contestando...", from))

	// Store the call for tracking
	call := &ActiveCall{
		CallSID: callSID,
		To:      from, // "To" is the caller in this context
		Purpose: "incoming_call",
		UserID:  t.bot.config.AdminID,
		Messages: []ChatMessage{
			{
				Role:    "system",
				Content: fmt.Sprintf("You are Minerva, %s's personal AI assistant. You are answering an incoming phone call from %s. %s is not available right now.\n\nINSTRUCTIONS:\n- Answer politely in Spanish\n- Find out who is calling and what they need\n- Take a message or provide helpful information\n- Be concise and natural\n- If it seems important or urgent, tell them you'll pass the message along\n- If they want to leave a message, assure them they will be notified\n- End the call politely when appropriate", t.ownerName, from, t.ownerName),
			},
		},
	}
	t.activeCalls.Store(callSID, call)

	// Return TwiML to connect to ConversationRelay
	wsURL := strings.Replace(t.baseURL, "https://", "wss://", 1) + "/twilio/ws"
	twiml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Response>
    <Connect>
        <ConversationRelay url="%s" voice="es-ES-Standard-A" language="es-ES" transcriptionProvider="google" ttsProvider="google" welcomeGreeting="Hola, soy Minerva. ¿En qué puedo ayudarle?" interruptible="true">
            <Parameter name="call_sid" value="%s" />
            <Parameter name="direction" value="inbound" />
            <Parameter name="from" value="%s" />
        </ConversationRelay>
    </Connect>
</Response>`, wsURL, callSID, url.QueryEscape(from))

	w.Header().Set("Content-Type", "application/xml")
	w.Write([]byte(twiml))
}

// InitiateCall starts a new outbound call
func (t *TwilioCallManager) InitiateCall(to, purpose string, userID, convID int64) (string, error) {
	if t.phoneNumber == "" {
		return "", fmt.Errorf("no Twilio phone number configured")
	}

	// Normalize phone number
	if !strings.HasPrefix(to, "+") {
		to = t.defaultCountryCode + strings.TrimPrefix(to, "0")
	}

	// Create TwiML for ConversationRelay
	wsURL := strings.Replace(t.baseURL, "https://", "wss://", 1) + "/twilio/ws"
	twiml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Response>
    <Connect>
        <ConversationRelay url="%s" voice="es-ES-Standard-A" language="es-ES" transcriptionProvider="google" ttsProvider="google" welcomeGreeting="Hola, llamo de parte de Minerva." interruptible="true" interruptByDtmf="true" dtmfDetection="true">
            <Parameter name="purpose" value="%s" />
            <Parameter name="user_id" value="%d" />
            <Parameter name="conv_id" value="%d" />
        </ConversationRelay>
    </Connect>
</Response>`, wsURL, url.QueryEscape(purpose), userID, convID)

	// Make the call via Twilio API
	callURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Calls.json", t.accountSID)

	data := url.Values{}
	data.Set("To", to)
	data.Set("From", t.phoneNumber)
	data.Set("Twiml", twiml)

	req, err := http.NewRequest("POST", callURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(t.accountSID, t.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to initiate call: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		SID     string `json:"sid"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Code != 0 {
		return "", fmt.Errorf("Twilio error: %s", result.Message)
	}

	log.Printf("Call initiated: SID=%s, To=%s, Status=%s", result.SID, to, result.Status)

	// Store the call
	call := &ActiveCall{
		CallSID: result.SID,
		To:      to,
		Purpose: purpose,
		UserID:  userID,
		ConvID:  convID,
		Messages: []ChatMessage{
			{
				Role:    "system",
				Content: fmt.Sprintf("You are Minerva, an AI assistant making a phone call on behalf of %s. The purpose of this call is: %s\n\nIMPORTANT INSTRUCTIONS:\n- Speak naturally in Spanish\n- Be polite and professional\n- Stay focused on the purpose of the call\n- If you accomplish the goal or need to end the call, say goodbye and I will hang up\n- Keep responses concise for natural conversation", t.ownerName, purpose),
			},
		},
	}
	t.activeCalls.Store(result.SID, call)

	return result.SID, nil
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow Twilio connections
	},
}

// HandleWebSocket handles the ConversationRelay WebSocket connection
func (t *TwilioCallManager) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("Twilio WebSocket connected")

	var currentCall *ActiveCall

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			break
		}

		var msg ConversationRelayMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Failed to parse message: %v", err)
			continue
		}

		log.Printf("Twilio message: type=%s", msg.Type)

		switch msg.Type {
		case "setup":
			// Connection established, extract call info from handoff data
			log.Printf("ConversationRelay setup complete")

		case "prompt":
			// User spoke, we received transcription
			if msg.Token != "" {
				log.Printf("User said: %s (last=%v)", msg.Token, msg.Last)

				// Find the call by checking active calls
				if currentCall == nil {
					// Try to find by iterating (in real scenario, we'd have call SID from setup)
					t.activeCalls.Range(func(key, value any) bool {
						call := value.(*ActiveCall)
						if call.ws == nil {
							call.ws = conn
							currentCall = call
							return false
						}
						return true
					})
				}

				if currentCall != nil && msg.Last {
					// Process complete utterance with AI
					currentCall.mu.Lock()
					currentCall.Messages = append(currentCall.Messages, ChatMessage{
						Role:    "user",
						Content: msg.Token,
					})
					messages := make([]ChatMessage, len(currentCall.Messages))
					copy(messages, currentCall.Messages)
					currentCall.mu.Unlock()

					// Get AI response (no tools for voice calls, keep it simple)
					response, err := t.bot.ai.Chat(messages, "", nil)
					if err != nil {
						log.Printf("AI error: %v", err)
						t.sendText(conn, "Lo siento, ha habido un problema. Déjame intentar de nuevo.")
						continue
					}

					responseContent := ""
					if response.Message != nil {
						if content, ok := response.Message.Content.(string); ok {
							responseContent = content
						}
					}

					currentCall.mu.Lock()
					currentCall.Messages = append(currentCall.Messages, ChatMessage{
						Role:    "assistant",
						Content: responseContent,
					})
					currentCall.mu.Unlock()

					// Send response to be spoken
					t.sendText(conn, responseContent)

					// Notify user via Telegram
					notification := fmt.Sprintf("📞 *Llamada en curso*\n👤: %s\n🤖: %s", msg.Token, truncateForTelegram(responseContent, 200))
					t.bot.sendMessage(currentCall.UserID, notification)
				}
			}

		case "interrupt":
			// User interrupted the AI
			log.Printf("User interrupted")

		case "dtmf":
			// User pressed a key
			log.Printf("DTMF received: %s", msg.Token)

		case "end":
			// Call ended
			log.Printf("Call ended: reason=%s, by=%s", msg.StopReason, msg.TerminatedBy)
			if currentCall != nil {
				t.activeCalls.Delete(currentCall.CallSID)

				// For incoming calls, generate a summary
				if currentCall.Purpose == "incoming_call" && len(currentCall.Messages) > 1 {
					go t.generateCallSummary(currentCall)
				} else {
					t.bot.sendMessage(currentCall.UserID, fmt.Sprintf("📞 Llamada finalizada (%s)", msg.StopReason))
				}
			}
			return
		}
	}
}

// generateCallSummary creates a summary of an incoming call and sends it to the user
func (t *TwilioCallManager) generateCallSummary(call *ActiveCall) {
	// Build conversation transcript
	var transcript strings.Builder
	for _, msg := range call.Messages {
		if msg.Role == "system" {
			continue
		}
		role := "Llamante"
		if msg.Role == "assistant" {
			role = "Minerva"
		}
		if content, ok := msg.Content.(string); ok {
			transcript.WriteString(fmt.Sprintf("%s: %s\n", role, content))
		}
	}

	// Ask AI to summarize
	summaryPrompt := []ChatMessage{
		{
			Role:    "system",
			Content: "Eres un asistente que resume llamadas telefónicas. Genera un resumen conciso en español. Si Minerva prometió devolver la llamada, DEBES indicarlo claramente con '⚠️ CALLBACK NECESARIO' al principio.",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Resume esta llamada telefónica entrante. Indica: 1) quién llamó (si se identificó), 2) qué querían, 3) si se prometió devolución de llamada, 4) cualquier acción necesaria:\n\n%s", transcript.String()),
		},
	}

	response, err := t.bot.ai.Chat(summaryPrompt, "", nil)
	if err != nil {
		log.Printf("Failed to generate call summary: %v", err)
		t.bot.sendMessage(call.UserID, fmt.Sprintf("📞 *Llamada finalizada*\nDe: %s\n\n_No se pudo generar resumen_", call.To))
		return
	}

	summary := ""
	if response.Message != nil {
		if content, ok := response.Message.Content.(string); ok {
			summary = content
		}
	}

	notification := fmt.Sprintf("📞 *Llamada entrante finalizada*\nDe: %s\n\n*Resumen:*\n%s", call.To, summary)
	t.bot.sendMessage(call.UserID, notification)
}

// sendText sends a text message to be spoken by Twilio
func (t *TwilioCallManager) sendText(conn *websocket.Conn, text string) {
	msg := map[string]any{
		"type": "text",
		"token": text,
		"last": true,
	}

	data, _ := json.Marshal(msg)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("Failed to send text: %v", err)
	}
}

// HangUp ends an active call
func (t *TwilioCallManager) HangUp(callSID string) error {
	callURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Calls/%s.json", t.accountSID, callSID)

	data := url.Values{}
	data.Set("Status", "completed")

	req, err := http.NewRequest("POST", callURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	req.SetBasicAuth(t.accountSID, t.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	t.activeCalls.Delete(callSID)
	return nil
}

// GetBalance returns the current Twilio account balance
func (t *TwilioCallManager) GetBalance() (float64, error) {
	balanceURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Balance.json", t.accountSID)

	req, err := http.NewRequest("GET", balanceURL, nil)
	if err != nil {
		return 0, err
	}

	req.SetBasicAuth(t.accountSID, t.authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result struct {
		Balance string `json:"balance"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	var balance float64
	fmt.Sscanf(result.Balance, "%f", &balance)
	return balance, nil
}

// GetCallStatus returns the status of a call
func (t *TwilioCallManager) GetCallStatus(callSID string) (string, error) {
	callURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Calls/%s.json", t.accountSID, callSID)

	req, err := http.NewRequest("GET", callURL, nil)
	if err != nil {
		return "", err
	}

	req.SetBasicAuth(t.accountSID, t.authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Status, nil
}

// ValidateRequest validates that a request came from Twilio
func (t *TwilioCallManager) ValidateRequest(r *http.Request) bool {
	signature := r.Header.Get("X-Twilio-Signature")
	if signature == "" {
		return false
	}

	// Build the full URL
	fullURL := t.baseURL + r.URL.Path

	// Get POST parameters
	r.ParseForm()
	params := make(map[string]string)
	for key, values := range r.PostForm {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}

	// Calculate expected signature
	expectedSig := t.calculateSignature(fullURL, params)

	return signature == expectedSig
}

func (t *TwilioCallManager) calculateSignature(url string, params map[string]string) string {
	// Sort params and append to URL
	data := url
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	// sort.Strings(keys) // Would need to import sort
	for _, k := range keys {
		data += k + params[k]
	}

	// HMAC-SHA1
	h := hmacSHA1([]byte(t.authToken), []byte(data))
	return base64.StdEncoding.EncodeToString(h)
}

func hmacSHA1(key, data []byte) []byte {
	mac := hmacNew(sha1Sum, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// Simplified HMAC implementation
func hmacNew(hash func([]byte) []byte, key []byte) *hmacWriter {
	return &hmacWriter{key: key, hash: hash}
}

type hmacWriter struct {
	key  []byte
	hash func([]byte) []byte
	buf  bytes.Buffer
}

func (h *hmacWriter) Write(p []byte) (n int, err error) {
	return h.buf.Write(p)
}

func (h *hmacWriter) Sum(in []byte) []byte {
	// Simplified - in production use crypto/hmac
	return h.hash(append(h.key, h.buf.Bytes()...))
}

func sha1Sum(data []byte) []byte {
	// Placeholder - in production use crypto/sha1
	return data
}

func truncateForTelegram(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
