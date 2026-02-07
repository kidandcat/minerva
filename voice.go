package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	geminiWSURL       = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	geminiModel       = "models/gemini-2.5-flash-native-audio-latest"
	geminiVoice       = "Aoede"
	voiceCallTimeout  = 5 * time.Minute
	systemPromptVoice = `You are Minerva, a personal AI assistant for Jairo. You are speaking on a phone call.

Key behaviors:
- Respond naturally and conversationally, as if talking on the phone
- Keep responses concise and clear - this is a voice conversation, not text
- Speak in Spanish by default (Jairo's native language), but switch to English if the caller speaks English
- Don't use special characters, markdown, or formatting - your output will be converted to speech
- Be helpful, friendly, and efficient
- If someone asks who you are, say you're Minerva, Jairo's AI assistant
- You can help with general questions, take messages, and provide information
- If it seems important or urgent, tell them you'll let Jairo know
- Be warm and personable`
)

// VoiceManager handles voice calls via Twilio Media Streams + Gemini Live API
type VoiceManager struct {
	bot            *Bot
	apiKey         string
	baseURL        string // Public URL (e.g., https://home.jairo.cloud)
	twilioSID      string
	twilioToken    string
	twilioPhone    string
	activeCalls    sync.Map
	pendingCalls   sync.Map // callSID → system prompt override for outbound calls
}

// voiceSession tracks an active voice call
type voiceSession struct {
	callSID      string
	streamSID    string
	from         string
	outbound     bool   // true for outbound calls
	systemPrompt string // custom system prompt for outbound calls
	startTime    time.Time
	geminiConn   *websocket.Conn
	twilioConn   *websocket.Conn
	mu           sync.Mutex
	closed       bool
	// Transcription collected during the call
	transcript []transcriptEntry
}

type transcriptEntry struct {
	Speaker string // "user" or "assistant"
	Text    string
}

// Twilio Media Streams message types
type twilioStreamMsg struct {
	Event          string `json:"event"`
	SequenceNumber string `json:"sequenceNumber,omitempty"`
	StreamSID      string `json:"streamSid,omitempty"`
	Start          *struct {
		StreamSID        string            `json:"streamSid"`
		AccountSID       string            `json:"accountSid"`
		CallSID          string            `json:"callSid"`
		Tracks           []string          `json:"tracks"`
		CustomParameters map[string]string `json:"customParameters"`
		MediaFormat      struct {
			Encoding   string `json:"encoding"`
			SampleRate int    `json:"sampleRate"`
			Channels   int    `json:"channels"`
		} `json:"mediaFormat"`
	} `json:"start,omitempty"`
	Media *struct {
		Track     string `json:"track"`
		Chunk     string `json:"chunk"`
		Timestamp string `json:"timestamp"`
		Payload   string `json:"payload"`
	} `json:"media,omitempty"`
	Stop *struct {
		AccountSID string `json:"accountSid"`
		CallSID    string `json:"callSid"`
	} `json:"stop,omitempty"`
}

// Gemini Live API message types
type geminiSetupMsg struct {
	Setup struct {
		Model            string            `json:"model"`
		GenerationConfig geminiGenConfig   `json:"generationConfig"`
		SystemInstruction geminiContent    `json:"systemInstruction"`
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
	Text       string          `json:"text,omitempty"`
	InlineData *geminiBlob     `json:"inlineData,omitempty"`
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

func NewVoiceManager(bot *Bot, apiKey, baseURL, twilioSID, twilioToken, twilioPhone string) *VoiceManager {
	return &VoiceManager{
		bot:         bot,
		apiKey:      apiKey,
		baseURL:     baseURL,
		twilioSID:   twilioSID,
		twilioToken: twilioToken,
		twilioPhone: twilioPhone,
	}
}

// HandleIncoming handles POST /voice/incoming - returns TwiML for Twilio Media Streams
func (v *VoiceManager) HandleIncoming(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	from := r.FormValue("From")
	callSID := r.FormValue("CallSid")

	log.Printf("[Voice] Incoming call from %s (SID: %s)", from, callSID)

	// Notify admin
	v.bot.sendMessage(v.bot.config.AdminID, fmt.Sprintf("📞 *Llamada entrante*\nDe: %s\nMinerva (Gemini Live) contestando...", from))

	// Build WebSocket URL for media stream
	wsURL := strings.Replace(v.baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/voice/ws"

	twiml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Response>
    <Connect>
        <Stream url="%s">
            <Parameter name="from" value="%s" />
            <Parameter name="call_sid" value="%s" />
        </Stream>
    </Connect>
</Response>`, wsURL, url.QueryEscape(from), callSID)

	w.Header().Set("Content-Type", "application/xml")
	w.Write([]byte(twiml))
}

// HandleMediaStream handles WebSocket /voice/ws - bridges Twilio audio ↔ Gemini Live
func (v *VoiceManager) HandleMediaStream(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Voice] WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("[Voice] Twilio media stream connected")

	session := &voiceSession{
		twilioConn: conn,
		startTime:  time.Now(),
	}

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[Voice] Twilio WS read error: %v", err)
			break
		}

		var msg twilioStreamMsg
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("[Voice] Failed to parse Twilio message: %v", err)
			continue
		}

		switch msg.Event {
		case "connected":
			log.Printf("[Voice] Twilio stream protocol connected")

		case "start":
			if msg.Start == nil {
				continue
			}
			session.streamSID = msg.Start.StreamSID
			session.callSID = msg.Start.CallSID
			if from, ok := msg.Start.CustomParameters["from"]; ok {
				session.from = from
			}

			// Check if this is an outbound call with a custom system prompt
			if dir, ok := msg.Start.CustomParameters["direction"]; ok && dir == "outbound" {
				session.outbound = true
				if prompt, ok := v.pendingCalls.LoadAndDelete(session.callSID); ok {
					session.systemPrompt = prompt.(string)
				}
			}

			log.Printf("[Voice] Stream started: streamSID=%s callSID=%s from=%s outbound=%v",
				session.streamSID, session.callSID, session.from, session.outbound)

			v.activeCalls.Store(session.callSID, session)

			// Connect to Gemini Live API
			if err := v.connectGemini(session); err != nil {
				log.Printf("[Voice] Failed to connect Gemini: %v", err)
				v.bot.sendMessage(v.bot.config.AdminID, "❌ Error conectando con Gemini Live para la llamada")
				return
			}

			// Start reading from Gemini and forwarding to Twilio
			go v.geminiToTwilio(session)

			// Start call timeout (5 minutes max)
			go func() {
				timer := time.NewTimer(voiceCallTimeout)
				defer timer.Stop()
				<-timer.C
				session.mu.Lock()
				closed := session.closed
				session.mu.Unlock()
				if !closed {
					log.Printf("[Voice] Call timeout reached (%s), closing session", voiceCallTimeout)
					v.closeSession(session)
					// Close Twilio WebSocket to end the call
					conn.Close()
				}
			}()

		case "media":
			if msg.Media == nil || session.geminiConn == nil {
				continue
			}
			// Forward audio to Gemini: mu-law 8kHz → PCM 16kHz
			v.forwardToGemini(session, msg.Media.Payload)

		case "stop":
			log.Printf("[Voice] Stream stopped: callSID=%s", session.callSID)
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

	session.geminiConn = conn

	// Build setup message
	prompt := systemPromptVoice
	if session.systemPrompt != "" {
		prompt = session.systemPrompt
	}

	var setup geminiSetupMsg
	setup.Setup.Model = geminiModel
	setup.Setup.GenerationConfig.ResponseModalities = "audio"
	setup.Setup.GenerationConfig.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName = geminiVoice
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

// forwardToGemini converts Twilio mu-law 8kHz audio to PCM 16kHz and sends to Gemini
func (v *VoiceManager) forwardToGemini(session *voiceSession, payload string) {
	// Decode base64 mu-law
	mulawData, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		log.Printf("[Voice] Failed to decode Twilio audio: %v", err)
		return
	}

	// mu-law → PCM 16-bit
	pcmSamples := mulawDecodeChunk(mulawData)

	// Resample 8kHz → 16kHz
	pcm16k := resampleLinear(pcmSamples, 8000, 16000)

	// PCM samples → bytes (little-endian)
	pcmBytes := pcmToBytes(pcm16k)

	// Encode as base64 for Gemini
	pcmB64 := base64.StdEncoding.EncodeToString(pcmBytes)

	// Send to Gemini
	input := geminiRealtimeInput{}
	input.RealtimeInput.MediaChunks = []geminiBlob{
		{
			MimeType: "audio/pcm;rate=16000",
			Data:     pcmB64,
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

// geminiToTwilio reads audio from Gemini and forwards to Twilio
func (v *VoiceManager) geminiToTwilio(session *voiceSession) {
	defer func() {
		log.Printf("[Voice] Gemini reader goroutine exiting")
	}()

	for {
		session.mu.Lock()
		if session.closed {
			session.mu.Unlock()
			return
		}
		geminiConn := session.geminiConn
		session.mu.Unlock()

		if geminiConn == nil {
			return
		}

		_, msg, err := geminiConn.ReadMessage()
		if err != nil {
			if !session.closed {
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
					v.forwardToTwilio(session, part.InlineData.Data)
				}
			}
		}

		if resp.ServerContent.TurnComplete {
			log.Printf("[Voice] Gemini turn complete")
		}
	}
}

// forwardToTwilio converts Gemini PCM 24kHz audio to mu-law 8kHz and sends to Twilio
func (v *VoiceManager) forwardToTwilio(session *voiceSession, pcmB64 string) {
	// Decode base64 PCM
	pcmBytes, err := base64.StdEncoding.DecodeString(pcmB64)
	if err != nil {
		log.Printf("[Voice] Failed to decode Gemini audio: %v", err)
		return
	}

	// Bytes → PCM 16-bit samples
	pcmSamples := bytesToPCM(pcmBytes)

	// Resample 24kHz → 8kHz
	pcm8k := resampleLinear(pcmSamples, 24000, 8000)

	// PCM → mu-law
	mulawData := mulawEncodeChunk(pcm8k)

	// Encode as base64 for Twilio
	mulawB64 := base64.StdEncoding.EncodeToString(mulawData)

	// Send to Twilio
	twilioMsg := map[string]any{
		"event":     "media",
		"streamSid": session.streamSID,
		"media": map[string]string{
			"payload": mulawB64,
		},
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.twilioConn == nil {
		return
	}

	if err := session.twilioConn.WriteJSON(twilioMsg); err != nil {
		log.Printf("[Voice] Failed to send audio to Twilio: %v", err)
	}
}

// MakeCall initiates an outbound call via Twilio with Gemini Live voice
func (v *VoiceManager) MakeCall(to, greeting string) (string, error) {
	if v.twilioPhone == "" {
		return "", fmt.Errorf("no Twilio phone number configured")
	}

	// Normalize phone number
	if !strings.HasPrefix(to, "+") {
		to = "+34" + strings.TrimPrefix(to, "0")
	}

	// Build TwiML that points to our outbound endpoint
	wsURL := strings.Replace(v.baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/voice/ws"

	twiml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Response>
    <Connect>
        <Stream url="%s">
            <Parameter name="from" value="minerva-outbound" />
            <Parameter name="direction" value="outbound" />
        </Stream>
    </Connect>
</Response>`, wsURL)

	// Build outbound system prompt with greeting
	outboundPrompt := fmt.Sprintf(`You are Minerva, a personal AI assistant for Jairo. You are making an outbound phone call.

IMPORTANT: When the person answers, immediately say: "%s"

Key behaviors:
- Speak in Spanish
- Keep responses concise and natural
- You are calling on behalf of Jairo
- Be warm and professional`, greeting)

	// Make the call via Twilio API
	callURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Calls.json", v.twilioSID)

	data := url.Values{}
	data.Set("To", to)
	data.Set("From", v.twilioPhone)
	data.Set("Twiml", twiml)

	req, err := http.NewRequest("POST", callURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.SetBasicAuth(v.twilioSID, v.twilioToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to initiate call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		SID     string `json:"sid"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w (body: %s)", err, string(body))
	}
	if resp.StatusCode >= 400 || result.Code != 0 {
		return "", fmt.Errorf("Twilio error (code %d): %s", result.Code, result.Message)
	}

	log.Printf("[Voice] Outbound call initiated: SID=%s, To=%s", result.SID, to)

	// Store the outbound system prompt so HandleMediaStream can use it
	v.pendingCalls.Store(result.SID, outboundPrompt)

	return result.SID, nil
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
	session.mu.Unlock()

	if geminiConn != nil {
		geminiConn.Close()
	}

	if session.callSID != "" {
		v.activeCalls.Delete(session.callSID)
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
			Content: "Eres un asistente que resume llamadas telefónicas. Genera un resumen conciso en español. Si Minerva prometió que Jairo devolvería la llamada, indica '⚠️ CALLBACK NECESARIO' al principio.",
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

	v.bot.sendMessage(v.bot.config.AdminID, fmt.Sprintf(
		"📞 *Llamada finalizada*\nDe: %s\nDuración: %s\n\n*Resumen:*\n%s",
		session.from, duration, summary))
}
