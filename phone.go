package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PhoneBridge manages Android phone connections
type PhoneBridge struct {
	bot           *Bot
	voiceManager  *VoiceManager
	devices       sync.Map // deviceID -> *PhoneDevice
	upgrader      websocket.Upgrader
}

// PhoneDevice represents a connected Android device
type PhoneDevice struct {
	ID           string
	DeviceType   string
	conn         *websocket.Conn
	bridge       *PhoneBridge
	session      *phoneSession
	send         chan []byte
	connected    time.Time
	mu           sync.Mutex
}

// phoneSession tracks an active phone call via Android bridge
type phoneSession struct {
	device      *PhoneDevice
	callID      string
	direction   string // "incoming" or "outgoing"
	from        string
	callerName  string
	startTime   time.Time
	geminiConn  *websocket.Conn
	transcript  []transcriptEntry
	closed      bool
	mu          sync.Mutex
}

// PhoneMessage represents messages to/from Android device
type PhoneMessage struct {
	Type       string `json:"type"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceType string `json:"device_type,omitempty"`
	Direction  string `json:"direction,omitempty"`
	From       string `json:"from,omitempty"`
	CallerName string `json:"caller_name,omitempty"`
	Data       string `json:"data,omitempty"`
	Command    string `json:"command,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	To         string `json:"to,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
}

// NewPhoneBridge creates a new phone bridge manager
func NewPhoneBridge(bot *Bot, voiceManager *VoiceManager) *PhoneBridge {
	return &PhoneBridge{
		bot:          bot,
		voiceManager: voiceManager,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// HandleWebSocket handles Android phone bridge WebSocket connections
func (p *PhoneBridge) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Phone] WebSocket upgrade error: %v", err)
		return
	}

	device := &PhoneDevice{
		conn:      conn,
		bridge:    p,
		send:      make(chan []byte, 64),
		connected: time.Now(),
	}

	go device.readPump()
	go device.writePump()
}

// ListDevices returns connected phone devices
func (p *PhoneBridge) ListDevices() []map[string]any {
	var devices []map[string]any
	p.devices.Range(func(key, value any) bool {
		device := value.(*PhoneDevice)
		devices = append(devices, map[string]any{
			"id":         device.ID,
			"type":       device.DeviceType,
			"connected":  device.connected.Format(time.RFC3339),
			"in_call":    device.session != nil,
		})
		return true
	})
	return devices
}

// MakeCall instructs an Android device to make an outgoing call
func (p *PhoneBridge) MakeCall(deviceID, to, purpose string) error {
	var device *PhoneDevice

	if deviceID != "" {
		if d, ok := p.devices.Load(deviceID); ok {
			device = d.(*PhoneDevice)
		}
	} else {
		// Use first available device
		p.devices.Range(func(key, value any) bool {
			device = value.(*PhoneDevice)
			return false // stop after first
		})
	}

	if device == nil {
		return fmt.Errorf("no phone device available")
	}

	msg := PhoneMessage{
		Type:    "make_call",
		To:      to,
		Purpose: purpose,
		CallID:  fmt.Sprintf("out_%d", time.Now().UnixNano()),
	}

	data, _ := json.Marshal(msg)
	device.send <- data

	log.Printf("[Phone] Requested call to %s via device %s", to, device.ID)
	return nil
}

func (d *PhoneDevice) readPump() {
	defer func() {
		d.bridge.unregisterDevice(d)
		d.conn.Close()
	}()

	d.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	d.conn.SetPongHandler(func(string) error {
		d.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})

	for {
		_, message, err := d.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[Phone] Read error: %v", err)
			}
			return
		}

		d.conn.SetReadDeadline(time.Now().Add(120 * time.Second))

		var msg PhoneMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("[Phone] Invalid message: %v", err)
			continue
		}

		d.handleMessage(msg)
	}
}

func (d *PhoneDevice) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		d.conn.Close()
	}()

	for {
		select {
		case message, ok := <-d.send:
			d.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				d.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := d.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			d.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := d.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (d *PhoneDevice) handleMessage(msg PhoneMessage) {
	switch msg.Type {
	case "register":
		d.ID = msg.DeviceID
		d.DeviceType = msg.DeviceType
		d.bridge.registerDevice(d)

	case "call_start":
		d.handleCallStart(msg)

	case "call_active":
		d.handleCallActive()

	case "audio":
		d.handleAudio(msg.Data)

	case "call_end":
		d.handleCallEnd()
	}
}

func (d *PhoneDevice) handleCallStart(msg PhoneMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.session != nil {
		log.Printf("[Phone] Already in a call, ignoring new call")
		return
	}

	d.session = &phoneSession{
		device:     d,
		callID:     msg.CallID,
		direction:  msg.Direction,
		from:       msg.From,
		callerName: msg.CallerName,
		startTime:  time.Now(),
	}

	log.Printf("[Phone] Call started: direction=%s from=%s name=%s",
		msg.Direction, msg.From, msg.CallerName)

	// Notify admin
	direction := "entrante"
	if msg.Direction == "outgoing" {
		direction = "saliente"
	}
	d.bridge.bot.sendMessage(d.bridge.bot.config.AdminID,
		fmt.Sprintf("📱 Llamada %s de %s (%s)", direction, msg.From, msg.CallerName))
}

func (d *PhoneDevice) handleCallActive() {
	d.mu.Lock()
	session := d.session
	d.mu.Unlock()

	if session == nil {
		return
	}

	// Connect to Gemini Live
	ownerName := "the owner"
	if d.bridge.bot.config != nil {
		ownerName = d.bridge.bot.config.OwnerName
	}
	var prompt string
	if session.direction == "incoming" {
		prompt = buildIncomingVoicePrompt(ownerName)
	} else {
		prompt = fmt.Sprintf(`You are Minerva, %s's AI assistant. You are making an outbound call.

Key behaviors:
- Speak in Spanish
- Be natural and conversational
- Introduce yourself briefly
- Be warm and professional`, ownerName)
	}

	if err := d.connectGemini(session, prompt); err != nil {
		log.Printf("[Phone] Failed to connect Gemini: %v", err)
		d.bridge.bot.sendMessage(d.bridge.bot.config.AdminID,
			fmt.Sprintf("❌ Error conectando con Gemini: %v", err))
		return
	}

	log.Printf("[Phone] Gemini connected for call")
}

func (d *PhoneDevice) handleAudio(base64Audio string) {
	d.mu.Lock()
	session := d.session
	d.mu.Unlock()

	if session == nil || session.geminiConn == nil {
		return
	}

	// Decode PCM from Android (16kHz 16-bit mono)
	pcmData, err := base64.StdEncoding.DecodeString(base64Audio)
	if err != nil {
		return
	}

	// Send to Gemini (expects PCM 16kHz)
	audioMsg := geminiRealtimeInput{
		RealtimeInput: struct {
			MediaChunks []geminiBlob `json:"mediaChunks"`
		}{
			MediaChunks: []geminiBlob{{
				MimeType: "audio/pcm;rate=16000",
				Data:     base64.StdEncoding.EncodeToString(pcmData),
			}},
		},
	}

	session.mu.Lock()
	geminiConn := session.geminiConn
	session.mu.Unlock()

	if geminiConn != nil {
		geminiConn.WriteJSON(audioMsg)
	}
}

func (d *PhoneDevice) handleCallEnd() {
	d.mu.Lock()
	session := d.session
	d.session = nil
	d.mu.Unlock()

	if session == nil {
		return
	}

	session.mu.Lock()
	session.closed = true
	geminiConn := session.geminiConn
	session.mu.Unlock()

	if geminiConn != nil {
		geminiConn.Close()
	}

	duration := time.Since(session.startTime).Round(time.Second)
	log.Printf("[Phone] Call ended: from=%s duration=%s", session.from, duration)

	// Generate summary
	go d.generateSummary(session, duration)
}

func (d *PhoneDevice) connectGemini(session *phoneSession, prompt string) error {
	vm := d.bridge.voiceManager
	if vm == nil {
		return fmt.Errorf("voice manager not available")
	}

	wsURL := fmt.Sprintf("%s?key=%s", geminiWSURL, vm.apiKey)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("gemini dial: %w", err)
	}

	session.mu.Lock()
	session.geminiConn = conn
	session.mu.Unlock()

	// Send setup message
	var setup geminiSetupMsg
	setup.Setup.Model = geminiModel
	setup.Setup.GenerationConfig.ResponseModalities = "audio"
	setup.Setup.GenerationConfig.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName = geminiVoice
	setup.Setup.SystemInstruction = geminiContent{
		Parts: []geminiPart{{Text: prompt}},
	}

	if err := conn.WriteJSON(setup); err != nil {
		conn.Close()
		return fmt.Errorf("gemini setup: %w", err)
	}

	// Wait for setup response
	var response map[string]interface{}
	if err := conn.ReadJSON(&response); err != nil {
		conn.Close()
		return fmt.Errorf("gemini setup response: %w", err)
	}

	// Start reading Gemini responses
	go d.geminiToPhone(session)

	return nil
}

func (d *PhoneDevice) geminiToPhone(session *phoneSession) {
	session.mu.Lock()
	conn := session.geminiConn
	session.mu.Unlock()

	if conn == nil {
		return
	}

	for {
		session.mu.Lock()
		closed := session.closed
		session.mu.Unlock()

		if closed {
			return
		}

		var msg geminiServerMsg
		if err := conn.ReadJSON(&msg); err != nil {
			if !session.closed {
				log.Printf("[Phone] Gemini read error: %v", err)
			}
			return
		}

		// Handle audio response
		if msg.ServerContent != nil && msg.ServerContent.ModelTurn != nil {
			for _, part := range msg.ServerContent.ModelTurn.Parts {
				if part.InlineData != nil && strings.HasPrefix(part.InlineData.MimeType, "audio/") {
					// Gemini sends 24kHz PCM, need to resample to 16kHz for Android
					pcmData, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
					if err != nil {
						continue
					}

					// Resample 24kHz to 16kHz
					resampled := resample24to16(pcmData)
					base64Audio := base64.StdEncoding.EncodeToString(resampled)

					// Send to Android
					audioMsg := PhoneMessage{
						Type: "audio",
						Data: base64Audio,
					}
					data, _ := json.Marshal(audioMsg)

					select {
					case d.send <- data:
					default:
						log.Printf("[Phone] Send buffer full, dropping audio")
					}
				}

				// Track transcript
				if part.Text != "" {
					session.mu.Lock()
					session.transcript = append(session.transcript, transcriptEntry{
						Speaker: "assistant",
						Text:    part.Text,
					})
					session.mu.Unlock()
				}
			}
		}
	}
}

func (d *PhoneDevice) generateSummary(session *phoneSession, duration time.Duration) {
	session.mu.Lock()
	transcript := make([]transcriptEntry, len(session.transcript))
	copy(transcript, session.transcript)
	session.mu.Unlock()

	if len(transcript) == 0 {
		d.bridge.bot.sendMessage(d.bridge.bot.config.AdminID, fmt.Sprintf(
			"📱 *Llamada finalizada*\nDe: %s\nDuración: %s\n\n_Sin transcripción disponible_",
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

	// Ask AI to summarize
	summaryPrompt := []ChatMessage{
		{
			Role:    "system",
			Content: "Eres un asistente que resume llamadas telefónicas. Genera un resumen conciso en español.",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Resume esta llamada telefónica:\n\n%s", sb.String()),
		},
	}

	response, err := d.bridge.bot.ai.Chat(summaryPrompt, "", nil)
	if err != nil {
		log.Printf("[Phone] Failed to generate summary: %v", err)
		return
	}

	summary := ""
	if response.Message != nil {
		if content, ok := response.Message.Content.(string); ok {
			summary = content
		}
	}

	d.bridge.bot.sendMessage(d.bridge.bot.config.AdminID, fmt.Sprintf(
		"📱 *Llamada finalizada*\nDe: %s\nDuración: %s\n\n*Resumen:*\n%s",
		session.from, duration, summary))

	// Pass to brain for follow-up actions
	callContext := fmt.Sprintf("[LLAMADA TELEFÓNICA (Android) COMPLETADA]\nDe: %s\nDuración: %s\nResumen: %s\n\nSi hay acciones pendientes, créalas ahora.",
		session.from, duration, summary)
	go d.bridge.bot.ProcessSystemEvent(d.bridge.bot.config.AdminID, callContext)
}

func (p *PhoneBridge) registerDevice(device *PhoneDevice) {
	p.devices.Store(device.ID, device)
	log.Printf("[Phone] Device '%s' (%s) connected", device.ID, device.DeviceType)

	p.bot.sendMessage(p.bot.config.AdminID,
		fmt.Sprintf("📱 Teléfono '%s' conectado", device.ID))
}

func (p *PhoneBridge) unregisterDevice(device *PhoneDevice) {
	if device.ID != "" {
		p.devices.Delete(device.ID)
		log.Printf("[Phone] Device '%s' disconnected", device.ID)

		p.bot.sendMessage(p.bot.config.AdminID,
			fmt.Sprintf("📱 Teléfono '%s' desconectado", device.ID))
	}

	// Clean up any active session
	device.mu.Lock()
	if device.session != nil {
		device.session.mu.Lock()
		device.session.closed = true
		if device.session.geminiConn != nil {
			device.session.geminiConn.Close()
		}
		device.session.mu.Unlock()
	}
	device.mu.Unlock()

	close(device.send)
}

// resample24to16 resamples 24kHz PCM to 16kHz (simple decimation)
func resample24to16(input []byte) []byte {
	// 24kHz to 16kHz = 2:3 ratio
	// Simple approach: take 2 samples for every 3
	samples := len(input) / 2 // 16-bit samples
	outSamples := samples * 2 / 3
	output := make([]byte, outSamples*2)

	for i := 0; i < outSamples; i++ {
		srcIdx := (i * 3 / 2) * 2
		if srcIdx+1 < len(input) {
			output[i*2] = input[srcIdx]
			output[i*2+1] = input[srcIdx+1]
		}
	}

	return output
}
