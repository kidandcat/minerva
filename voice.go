package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	geminiWSURL      = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	voiceCallTimeout = 5 * time.Minute
	// Telnyx API
	telnyxAPIBase = "https://api.telnyx.com/v2"
)

func buildIncomingVoicePrompt(ownerName, voiceLanguage string) string {
	langInstruction := fmt.Sprintf("Speak in %s by default, switch to the caller's language if they speak a different one", voiceLanguage)
	return fmt.Sprintf(`You are Minerva, %s's personal AI assistant. You are answering an incoming phone call.

YOUR ROLE ON THIS CALL:
- Answer professionally and warmly
- Take messages if someone wants to leave one
- Answer basic questions if you can help
- For anything important or urgent, assure them you'll notify %s immediately
- If they want to schedule something, take their details and say %s will confirm
- Never give out personal information (address, schedule, etc.)

KEY BEHAVIORS:
- %s
- Be natural and conversational - this is a real phone call
- Keep responses short and clear
- Be warm, helpful, and professional
- If unsure about something, say you'll check with %s and get back to them`, ownerName, ownerName, ownerName, langInstruction, ownerName)
}

// VoiceManager handles voice calls via Telnyx Call Control + Gemini Live API
type VoiceManager struct {
	bot                *Bot
	apiKey             string // Google API key for Gemini
	telnyxAPIKey       string
	telnyxAppID        string // Telnyx Call Control App connection_id
	telnyxPhone        string // Telnyx phone number (E.164)
	baseURL            string // Public URL for webhooks
	ownerName          string
	defaultCountryCode string
	geminiModel        string
	geminiVoice        string
	voiceLanguage      string
	activeCalls        sync.Map // callControlID → *voiceSession
	pendingCalls       sync.Map // callControlID → system prompt override for outbound calls
}

// voiceSession tracks an active voice call
type voiceSession struct {
	callControlID string
	streamID      string
	from          string
	outbound      bool   // true for outbound calls
	systemPrompt  string // custom system prompt for outbound calls
	startTime     time.Time
	geminiConn    *websocket.Conn
	telnyxConn    *websocket.Conn
	mu            sync.Mutex
	closed        bool
	done          chan struct{} // signals session completion for timeout goroutine
	// Transcription collected during the call
	transcript []transcriptEntry
}

type transcriptEntry struct {
	Speaker string // "user" or "assistant"
	Text    string
}

// Telnyx WebSocket media stream message types
type telnyxStreamMsg struct {
	Event          string `json:"event"`
	SequenceNumber string `json:"sequence_number,omitempty"`
	StreamID       string `json:"stream_id,omitempty"`
	Start          *struct {
		CallControlID string `json:"call_control_id"`
		MediaFormat   struct {
			Encoding   string `json:"encoding"`
			SampleRate int    `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"media_format"`
	} `json:"start,omitempty"`
	Media *struct {
		Track     string `json:"track"`
		Chunk     string `json:"chunk"`
		Timestamp string `json:"timestamp"`
		Payload   string `json:"payload"`
	} `json:"media,omitempty"`
}

// Telnyx Call Control webhook event envelope
type telnyxWebhookEvent struct {
	Data struct {
		ID        string `json:"id"`
		EventType string `json:"event_type"`
		Payload   struct {
			CallControlID string `json:"call_control_id"`
			CallLegID     string `json:"call_leg_id"`
			CallSessionID string `json:"call_session_id"`
			ConnectionID  string `json:"connection_id"`
			From          string `json:"from"`
			To            string `json:"to"`
			Direction     string `json:"direction"`
			State         string `json:"state"`
			ClientState   string `json:"client_state,omitempty"`
			HangupCause   string `json:"hangup_cause,omitempty"`
			HangupSource  string `json:"hangup_source,omitempty"`
		} `json:"payload"`
	} `json:"data"`
}

// Gemini Live API message types
type geminiSetupMsg struct {
	Setup struct {
		Model              string          `json:"model"`
		GenerationConfig   geminiGenConfig `json:"generationConfig"`
		SystemInstruction  geminiContent   `json:"systemInstruction"`
		InputAudioTranscription  *struct{} `json:"inputAudioTranscription,omitempty"`
		OutputAudioTranscription *struct{} `json:"outputAudioTranscription,omitempty"`
	} `json:"setup"`
}

type geminiGenConfig struct {
	ResponseModalities string          `json:"responseModalities"`
	SpeechConfig       geminiSpeechCfg `json:"speechConfig"`
}

type geminiSpeechCfg struct {
	VoiceConfig struct {
		PrebuiltVoiceConfig struct {
			VoiceName string `json:"voiceName"`
		} `json:"prebuiltVoiceConfig"`
	} `json:"voiceConfig"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string      `json:"text,omitempty"`
	InlineData *geminiBlob `json:"inlineData,omitempty"`
}

type geminiBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiRealtimeInput struct {
	RealtimeInput struct {
		MediaChunks []geminiBlob `json:"mediaChunks"`
	} `json:"realtimeInput"`
}

// geminiServerMsg is the server response (parsed generically)
type geminiServerMsg struct {
	SetupComplete *json.RawMessage `json:"setupComplete,omitempty"`
	ServerContent *struct {
		ModelTurn *struct {
			Parts []geminiPart `json:"parts"`
		} `json:"modelTurn,omitempty"`
		TurnComplete        bool `json:"turnComplete,omitempty"`
		InputTranscription  *struct {
			Text string `json:"text"`
		} `json:"inputTranscription,omitempty"`
		OutputTranscription *struct {
			Text string `json:"text"`
		} `json:"outputTranscription,omitempty"`
	} `json:"serverContent,omitempty"`
}

func NewVoiceManager(bot *Bot, googleAPIKey, telnyxAPIKey, telnyxAppID, telnyxPhone, baseURL, ownerName, defaultCountryCode, geminiModel, geminiVoice, voiceLanguage string) *VoiceManager {
	return &VoiceManager{
		bot:                bot,
		apiKey:             googleAPIKey,
		telnyxAPIKey:       telnyxAPIKey,
		telnyxAppID:        telnyxAppID,
		telnyxPhone:        telnyxPhone,
		baseURL:            baseURL,
		ownerName:          ownerName,
		defaultCountryCode: defaultCountryCode,
		geminiModel:        geminiModel,
		geminiVoice:        geminiVoice,
		voiceLanguage:      voiceLanguage,
	}
}

// HandleWebhook handles POST /voice/webhook - Telnyx Call Control events
func (v *VoiceManager) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading body", http.StatusBadRequest)
		return
	}

	var event telnyxWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("[Voice] Failed to parse Telnyx webhook: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	log.Printf("[Voice] Telnyx event: %s (call_control_id=%s)",
		event.Data.EventType, event.Data.Payload.CallControlID)

	switch event.Data.EventType {
	case "call.initiated":
		if event.Data.Payload.Direction == "incoming" {
			v.handleIncomingCall(event)
		}
		// For outbound calls, we just wait for call.answered

	case "call.answered":
		// Outbound call was answered — streaming was configured at dial time
		log.Printf("[Voice] Call answered: %s", event.Data.Payload.CallControlID)

	case "streaming.started":
		log.Printf("[Voice] Streaming started for call: %s", event.Data.Payload.CallControlID)

	case "streaming.failed":
		log.Printf("[Voice] Streaming failed for call: %s", event.Data.Payload.CallControlID)
		v.bot.sendMessage(v.bot.config.AdminID, "❌ Error: streaming de audio falló en la llamada")

	case "call.hangup":
		log.Printf("[Voice] Call hangup: cause=%s source=%s",
			event.Data.Payload.HangupCause, event.Data.Payload.HangupSource)
		if val, ok := v.activeCalls.Load(event.Data.Payload.CallControlID); ok {
			session := val.(*voiceSession)
			v.closeSession(session)
		}
	}

	// Always respond 200 to Telnyx
	w.WriteHeader(http.StatusOK)
}

// handleIncomingCall answers an incoming call and sets up media streaming
func (v *VoiceManager) handleIncomingCall(event telnyxWebhookEvent) {
	callControlID := event.Data.Payload.CallControlID
	from := event.Data.Payload.From

	log.Printf("[Voice] Incoming call from %s (ID: %s)", from, callControlID)

	// Notify admin
	v.bot.sendMessage(v.bot.config.AdminID, fmt.Sprintf("📞 *Llamada entrante*\nDe: %s\nMinerva (Gemini Live) contestando...", from))

	// Build WebSocket URL for media stream
	wsURL := strings.Replace(v.baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/voice/ws"

	// Answer the call with streaming configuration
	// Use L16 at 16kHz — matches Gemini's input format, no transcoding needed
	answerReq := map[string]any{
		"stream_url":                          wsURL,
		"stream_track":                        "inbound_track",
		"stream_bidirectional_mode":           "rtp",
		"stream_bidirectional_codec":          "L16",
		"stream_bidirectional_sampling_rate":  16000,
		"stream_bidirectional_target_legs":    "opposite",
		"send_silence_when_idle":              true,
		"client_state":                        base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"from":"%s","direction":"incoming"}`, from))),
	}

	if err := v.telnyxCallAction(callControlID, "answer", answerReq); err != nil {
		log.Printf("[Voice] Failed to answer call: %v", err)
		v.bot.sendMessage(v.bot.config.AdminID, "❌ Error contestando llamada")
	}
}

// HandleMediaStream handles WebSocket /voice/ws - bridges Telnyx audio ↔ Gemini Live
func (v *VoiceManager) HandleMediaStream(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Voice] WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Set read deadline for the initial handshake
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	log.Printf("[Voice] Telnyx media stream connected")

	session := &voiceSession{
		telnyxConn: conn,
		startTime:  time.Now(),
		done:       make(chan struct{}),
	}

	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil {
			if !session.closed {
				log.Printf("[Voice] Telnyx WS read error: %v", err)
			}
			break
		}

		var msg telnyxStreamMsg
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("[Voice] Failed to parse Telnyx message: %v", err)
			continue
		}

		switch msg.Event {
		case "connected":
			log.Printf("[Voice] Telnyx stream protocol connected")

		case "start":
			if msg.Start == nil {
				continue
			}
			session.streamID = msg.StreamID
			session.callControlID = msg.Start.CallControlID

			// Decode client_state to get call metadata
			if val, ok := v.activeCalls.Load(session.callControlID); ok {
				// Session already exists from outbound call setup
				existing := val.(*voiceSession)
				existing.mu.Lock()
				existing.telnyxConn = conn
				existing.streamID = msg.StreamID
				existing.startTime = time.Now()
				existing.done = session.done
				existing.mu.Unlock()
				session = existing
			} else {
				// New incoming call session
				session.from = "unknown"
				// Try to extract from pending calls
				if prompt, ok := v.pendingCalls.LoadAndDelete(session.callControlID); ok {
					session.outbound = true
					session.systemPrompt = prompt.(string)
				}
			}

			log.Printf("[Voice] Stream started: streamID=%s callControlID=%s encoding=%s rate=%d",
				session.streamID, session.callControlID,
				msg.Start.MediaFormat.Encoding, msg.Start.MediaFormat.SampleRate)

			v.activeCalls.Store(session.callControlID, session)

			// Connect to Gemini Live API
			if err := v.connectGemini(session); err != nil {
				log.Printf("[Voice] Failed to connect Gemini: %v", err)
				v.bot.sendMessage(v.bot.config.AdminID, "❌ Error conectando con Gemini Live para la llamada")
				return
			}

			// Start reading from Gemini and forwarding to Telnyx
			go v.geminiToTelnyx(session)

			// Start call timeout (5 minutes max) — exits when session ends or timeout fires
			go func() {
				timer := time.NewTimer(voiceCallTimeout)
				defer timer.Stop()
				select {
				case <-timer.C:
					session.mu.Lock()
					closed := session.closed
					session.mu.Unlock()
					if !closed {
						log.Printf("[Voice] Call timeout reached (%s), hanging up", voiceCallTimeout)
						v.hangUp(session.callControlID)
						v.closeSession(session)
					}
				case <-session.done:
					// Session ended before timeout — goroutine exits cleanly
				}
			}()

		case "media":
			if msg.Media == nil {
				continue
			}
			session.mu.Lock()
			geminiConn := session.geminiConn
			closed := session.closed
			session.mu.Unlock()
			if closed || geminiConn == nil {
				continue
			}
			// Telnyx sends L16 16kHz PCM — forward directly to Gemini (also expects PCM 16kHz)
			v.forwardToGemini(session, msg.Media.Payload)

		case "stop":
			log.Printf("[Voice] Stream stopped: callControlID=%s", session.callControlID)
			v.closeSession(session)
			return
		}
	}

	v.closeSession(session)
}

// connectGemini opens a WebSocket to Gemini Live and sends the setup message
func (v *VoiceManager) connectGemini(session *voiceSession) error {
	wsURL := fmt.Sprintf("%s?key=%s", geminiWSURL, v.apiKey)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("gemini dial: %w", err)
	}

	session.mu.Lock()
	session.geminiConn = conn
	session.mu.Unlock()

	// Build setup message
	prompt := buildIncomingVoicePrompt(v.ownerName, v.voiceLanguage)
	if session.systemPrompt != "" {
		prompt = session.systemPrompt
	}

	var setup geminiSetupMsg
	setup.Setup.Model = v.geminiModel
	setup.Setup.GenerationConfig.ResponseModalities = "audio"
	setup.Setup.GenerationConfig.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName = v.geminiVoice
	setup.Setup.SystemInstruction = geminiContent{
		Parts: []geminiPart{{Text: prompt}},
	}
	setup.Setup.InputAudioTranscription = &struct{}{}
	setup.Setup.OutputAudioTranscription = &struct{}{}

	if err := conn.WriteJSON(setup); err != nil {
		conn.Close()
		return fmt.Errorf("gemini setup write: %w", err)
	}

	// Wait for setupComplete
	_, msg, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("gemini setup response: %w", err)
	}

	var resp geminiServerMsg
	if err := json.Unmarshal(msg, &resp); err != nil {
		conn.Close()
		return fmt.Errorf("gemini setup parse: %w", err)
	}

	if resp.SetupComplete == nil {
		conn.Close()
		return fmt.Errorf("gemini: expected setupComplete, got: %s", string(msg))
	}

	log.Printf("[Voice] Gemini Live session established")
	return nil
}

// forwardToGemini sends Telnyx L16 16kHz audio directly to Gemini (no transcoding needed)
func (v *VoiceManager) forwardToGemini(session *voiceSession, payload string) {
	// Telnyx sends L16 (PCM 16-bit signed LE) at 16kHz — Gemini expects the same format
	// No transcoding needed, just forward the base64 payload
	input := geminiRealtimeInput{}
	input.RealtimeInput.MediaChunks = []geminiBlob{
		{
			MimeType: "audio/pcm;rate=16000",
			Data:     payload,
		},
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.geminiConn == nil {
		return
	}

	if err := session.geminiConn.WriteJSON(input); err != nil {
		log.Printf("[Voice] Failed to send audio to Gemini: %v", err)
	}
}

// geminiToTelnyx reads audio from Gemini and forwards to Telnyx
func (v *VoiceManager) geminiToTelnyx(session *voiceSession) {
	defer func() {
		log.Printf("[Voice] Gemini reader goroutine exiting")
	}()

	for {
		session.mu.Lock()
		closed := session.closed
		geminiConn := session.geminiConn
		session.mu.Unlock()

		if closed || geminiConn == nil {
			return
		}

		_, msg, err := geminiConn.ReadMessage()
		if err != nil {
			session.mu.Lock()
			isClosed := session.closed
			session.mu.Unlock()
			if !isClosed {
				log.Printf("[Voice] Gemini WS read error: %v", err)
			}
			return
		}

		var resp geminiServerMsg
		if err := json.Unmarshal(msg, &resp); err != nil {
			log.Printf("[Voice] Failed to parse Gemini message: %v", err)
			continue
		}

		if resp.ServerContent == nil {
			continue
		}

		// Handle transcriptions
		if resp.ServerContent.InputTranscription != nil && resp.ServerContent.InputTranscription.Text != "" {
			text := resp.ServerContent.InputTranscription.Text
			log.Printf("[Voice] User said: %s", text)
			session.mu.Lock()
			session.transcript = append(session.transcript, transcriptEntry{Speaker: "user", Text: text})
			session.mu.Unlock()
		}

		if resp.ServerContent.OutputTranscription != nil && resp.ServerContent.OutputTranscription.Text != "" {
			text := resp.ServerContent.OutputTranscription.Text
			log.Printf("[Voice] Minerva said: %s", text)
			session.mu.Lock()
			session.transcript = append(session.transcript, transcriptEntry{Speaker: "assistant", Text: text})
			session.mu.Unlock()
		}

		// Handle audio output
		if resp.ServerContent.ModelTurn != nil {
			for _, part := range resp.ServerContent.ModelTurn.Parts {
				if part.InlineData != nil && strings.HasPrefix(part.InlineData.MimeType, "audio/pcm") {
					v.forwardToTelnyx(session, part.InlineData.Data)
				}
			}
		}

		if resp.ServerContent.TurnComplete {
			log.Printf("[Voice] Gemini turn complete")
		}
	}
}

// forwardToTelnyx converts Gemini PCM 24kHz audio to L16 16kHz and sends to Telnyx
func (v *VoiceManager) forwardToTelnyx(session *voiceSession, pcmB64 string) {
	// Decode base64 PCM from Gemini (24kHz 16-bit LE)
	pcmBytes, err := base64.StdEncoding.DecodeString(pcmB64)
	if err != nil {
		log.Printf("[Voice] Failed to decode Gemini audio: %v", err)
		return
	}

	// Bytes → PCM 16-bit samples
	pcmSamples := bytesToPCM(pcmBytes)

	// Resample 24kHz → 16kHz (Telnyx expects L16 16kHz)
	pcm16k := resampleLinear(pcmSamples, 24000, 16000)

	// PCM samples → bytes (little-endian)
	outBytes := pcmToBytes(pcm16k)

	// Encode as base64 for Telnyx
	outB64 := base64.StdEncoding.EncodeToString(outBytes)

	// Send to Telnyx as bidirectional media
	telnyxMsg := map[string]any{
		"event": "media",
		"media": map[string]string{
			"payload": outB64,
		},
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.telnyxConn == nil {
		return
	}

	if err := session.telnyxConn.WriteJSON(telnyxMsg); err != nil {
		log.Printf("[Voice] Failed to send audio to Telnyx: %v", err)
	}
}

// MakeCall initiates an outbound call via Telnyx with Gemini Live voice
func (v *VoiceManager) MakeCall(to, purpose string) (string, error) {
	if v.telnyxPhone == "" {
		return "", fmt.Errorf("no Telnyx phone number configured")
	}
	if v.telnyxAppID == "" {
		return "", fmt.Errorf("no Telnyx app ID configured")
	}

	// Normalize phone number
	if !strings.HasPrefix(to, "+") {
		to = v.defaultCountryCode + strings.TrimPrefix(to, "0")
	}

	// Build WebSocket URL for media stream
	wsURL := strings.Replace(v.baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/voice/ws"

	// Build outbound system prompt
	outboundPrompt := fmt.Sprintf(`You are Minerva, a personal AI assistant for %s. You are making an outbound phone call on their behalf.

YOUR TASK FOR THIS CALL:
%s

Key behaviors:
- Speak in %s (unless the other person speaks a different language)
- Be natural and conversational, like a real phone call
- Keep responses concise - this is a phone call, not a chat
- Be warm, professional, and polite
- When the person answers, introduce yourself briefly
- Then proceed with your task
- If you accomplish the task or get the information needed, thank them and end the call politely
- If you encounter issues, explain you'll let %s know and end the call`, v.ownerName, purpose, v.voiceLanguage, v.ownerName)

	// Create outbound call via Telnyx API with streaming configuration
	callReq := map[string]any{
		"connection_id": v.telnyxAppID,
		"to":            to,
		"from":          v.telnyxPhone,
		"webhook_url":   v.baseURL + "/voice/webhook",
		"stream_url":    wsURL,
		"stream_track":  "inbound_track",
		"stream_bidirectional_mode":           "rtp",
		"stream_bidirectional_codec":          "L16",
		"stream_bidirectional_sampling_rate":  16000,
		"stream_bidirectional_target_legs":    "opposite",
		"send_silence_when_idle":              true,
		"timeout_secs":                        60,
		"time_limit_secs":                     int(voiceCallTimeout.Seconds()),
	}

	reqBody, _ := json.Marshal(callReq)
	req, err := http.NewRequest("POST", telnyxAPIBase+"/calls", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.telnyxAPIKey)

	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to initiate call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		Data struct {
			CallControlID string `json:"call_control_id"`
			CallSessionID string `json:"call_session_id"`
			CallLegID     string `json:"call_leg_id"`
			IsAlive       bool   `json:"is_alive"`
		} `json:"data"`
		Errors []struct {
			Code   string `json:"code"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w (body: %s)", err, string(body))
	}
	if len(result.Errors) > 0 {
		return "", fmt.Errorf("Telnyx error: %s - %s", result.Errors[0].Title, result.Errors[0].Detail)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("Telnyx API error (status %d): %s", resp.StatusCode, string(body))
	}

	callControlID := result.Data.CallControlID
	log.Printf("[Voice] Outbound call initiated: ID=%s, To=%s", callControlID, to)

	// Pre-create session for the outbound call
	session := &voiceSession{
		callControlID: callControlID,
		from:          to,
		outbound:      true,
		systemPrompt:  outboundPrompt,
		startTime:     time.Now(),
		done:          make(chan struct{}),
	}
	v.activeCalls.Store(callControlID, session)
	v.pendingCalls.Store(callControlID, outboundPrompt)

	return callControlID, nil
}

// hangUp terminates a call via Telnyx API
func (v *VoiceManager) hangUp(callControlID string) {
	if err := v.telnyxCallAction(callControlID, "hangup", map[string]any{}); err != nil {
		log.Printf("[Voice] Failed to hang up call %s: %v", callControlID, err)
	}
}

// telnyxCallAction performs a call control action via the Telnyx API
func (v *VoiceManager) telnyxCallAction(callControlID, action string, params map[string]any) error {
	url := fmt.Sprintf("%s/calls/%s/actions/%s", telnyxAPIBase, callControlID, action)

	reqBody, _ := json.Marshal(params)
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.telnyxAPIKey)

	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Telnyx API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// closeSession cleans up a voice session
func (v *VoiceManager) closeSession(session *voiceSession) {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return
	}
	session.closed = true
	geminiConn := session.geminiConn
	doneCh := session.done
	session.mu.Unlock()

	// Signal the timeout goroutine to exit
	if doneCh != nil {
		select {
		case <-doneCh:
			// Already closed
		default:
			close(doneCh)
		}
	}

	if geminiConn != nil {
		geminiConn.Close()
	}

	if session.callControlID != "" {
		v.activeCalls.Delete(session.callControlID)
	}

	duration := time.Since(session.startTime).Round(time.Second)
	log.Printf("[Voice] Call ended: from=%s duration=%s", session.from, duration)

	// Generate call summary and notify admin
	go v.generateSummary(session, duration)
}

// generateSummary creates a summary of the call and sends it to the admin
func (v *VoiceManager) generateSummary(session *voiceSession, duration time.Duration) {
	session.mu.Lock()
	transcript := make([]transcriptEntry, len(session.transcript))
	copy(transcript, session.transcript)
	session.mu.Unlock()

	if len(transcript) == 0 {
		v.bot.sendMessage(v.bot.config.AdminID, fmt.Sprintf(
			"📞 *Llamada finalizada*\nDe: %s\nDuración: %s\n\n_Sin transcripción disponible_",
			session.from, duration))
		return
	}

	// Build transcript text
	var sb strings.Builder
	for _, entry := range transcript {
		speaker := "Llamante"
		if entry.Speaker == "assistant" {
			speaker = "Minerva"
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", speaker, entry.Text))
	}

	// Ask Minerva AI to summarize
	summaryPrompt := []ChatMessage{
		{
			Role:    "system",
			Content: "Eres un asistente que resume llamadas telefónicas. Genera un resumen conciso en español. Si Minerva prometió devolver la llamada, indica '⚠️ CALLBACK NECESARIO' al principio.",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Resume esta llamada telefónica. Indica quién llamó, qué querían y cualquier acción necesaria:\n\n%s", sb.String()),
		},
	}

	response, err := v.bot.ai.Chat(summaryPrompt, "", nil)
	if err != nil {
		log.Printf("[Voice] Failed to generate summary: %v", err)
		v.bot.sendMessage(v.bot.config.AdminID, fmt.Sprintf(
			"📞 *Llamada finalizada*\nDe: %s\nDuración: %s\n\n*Transcripción:*\n%s",
			session.from, duration, truncateForTelegram(sb.String(), 500)))
		return
	}

	summary := ""
	if response.Message != nil {
		if content, ok := response.Message.Content.(string); ok {
			summary = content
		}
	}

	// Send summary to Telegram
	v.bot.sendMessage(v.bot.config.AdminID, fmt.Sprintf(
		"📞 *Llamada finalizada*\nDe: %s\nDuración: %s\n\n*Resumen:*\n%s",
		session.from, duration, summary))

	// Pass the summary to the brain so it has context about the call
	callContext := fmt.Sprintf("[LLAMADA TELEFÓNICA COMPLETADA]\nDe: %s\nDuración: %s\nResumen: %s\n\nSi hay acciones pendientes (callbacks, recordatorios, tareas), créalas ahora. Responde brevemente confirmando qué acciones has tomado (si alguna).",
		session.from, duration, summary)
	go v.bot.ProcessSystemEvent(v.bot.config.AdminID, callContext)
}

func truncateForTelegram(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
