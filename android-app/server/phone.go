package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type PhoneBridge struct {
	bot          *Bot
	voiceManager *VoiceManager
	devices      sync.Map // deviceID -> *PhoneDevice
	upgrader     websocket.Upgrader
}

type PhoneDevice struct {
	ID         string
	DeviceType string
	conn       *websocket.Conn
	bridge     *PhoneBridge
	session    *phoneSession
	send       chan []byte
	connected  time.Time
	mu         sync.Mutex
}

type phoneSession struct {
	device     *PhoneDevice
	callID     string
	direction  string // "incoming" or "outgoing"
	from       string
	callerName string
	startTime  time.Time
	geminiConn *websocket.Conn
	transcript []transcriptEntry
	closed     bool
	mu         sync.Mutex
}

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

func NewPhoneBridge(bot *Bot, voiceManager *VoiceManager) *PhoneBridge {
	return &PhoneBridge{
		bot:          bot,
		voiceManager: voiceManager,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}

func (pb *PhoneBridge) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := pb.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[PhoneBridge] WebSocket upgrade error: %v", err)
		return
	}

	device := &PhoneDevice{
		conn:      conn,
		bridge:    pb,
		send:      make(chan []byte, 64),
		connected: time.Now(),
	}

	go device.readPump()
	go device.writePump()
}

func (pb *PhoneBridge) ListDevices() []map[string]any {
	var devices []map[string]any
	pb.devices.Range(func(key, value any) bool {
		dev := value.(*PhoneDevice)
		dev.mu.Lock()
		info := map[string]any{
			"id":         dev.ID,
			"type":       dev.DeviceType,
			"connected":  dev.connected.Format(time.RFC3339),
			"has_active": dev.session != nil,
		}
		dev.mu.Unlock()
		devices = append(devices, info)
		return true
	})
	return devices
}

func (pb *PhoneBridge) MakeCall(deviceID, to, purpose string) error {
	val, ok := pb.devices.Load(deviceID)
	if !ok {
		return fmt.Errorf("device %s not found", deviceID)
	}
	dev := val.(*PhoneDevice)

	msg := PhoneMessage{
		Type:    "make_call",
		To:      to,
		Purpose: purpose,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal make_call: %w", err)
	}

	select {
	case dev.send <- data:
		return nil
	default:
		return fmt.Errorf("device %s send buffer full", deviceID)
	}
}

func (pb *PhoneBridge) registerDevice(device *PhoneDevice) {
	pb.devices.Store(device.ID, device)
	log.Printf("[PhoneBridge] Device registered: %s (%s)", device.ID, device.DeviceType)
}

func (pb *PhoneBridge) unregisterDevice(device *PhoneDevice) {
	if device.ID != "" {
		pb.devices.Delete(device.ID)
		log.Printf("[PhoneBridge] Device unregistered: %s", device.ID)
	}

	device.mu.Lock()
	session := device.session
	device.mu.Unlock()

	if session != nil {
		device.handleCallEnd()
	}
}

// readPump reads messages from the WebSocket connection.
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
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[PhoneBridge] Read error from %s: %v", d.ID, err)
			}
			return
		}

		var msg PhoneMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("[PhoneBridge] Invalid message from %s: %v", d.ID, err)
			continue
		}

		d.handleMessage(msg)
	}
}

// writePump writes messages to the WebSocket connection with ping keepalive.
func (d *PhoneDevice) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		d.conn.Close()
	}()

	for {
		select {
		case message, ok := <-d.send:
			if !ok {
				d.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			d.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := d.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("[PhoneBridge] Write error to %s: %v", d.ID, err)
				return
			}
		case <-ticker.C:
			d.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := d.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[PhoneBridge] Ping error to %s: %v", d.ID, err)
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

		resp := PhoneMessage{Type: "registered"}
		data, _ := json.Marshal(resp)
		select {
		case d.send <- data:
		default:
		}

	case "call_start":
		d.handleCallStart(msg)

	case "call_active":
		d.handleCallActive()

	case "audio":
		d.handleAudio(msg.Data)

	case "call_end":
		d.handleCallEnd()

	default:
		log.Printf("[PhoneBridge] Unknown message type from %s: %s", d.ID, msg.Type)
	}
}

func (d *PhoneDevice) handleCallStart(msg PhoneMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.session != nil {
		log.Printf("[PhoneBridge] Device %s already has active session, closing old one", d.ID)
		d.mu.Unlock()
		d.handleCallEnd()
		d.mu.Lock()
	}

	callID := msg.CallID
	if callID == "" {
		callID = fmt.Sprintf("phone-%s-%d", d.ID, time.Now().UnixMilli())
	}

	d.session = &phoneSession{
		device:     d,
		callID:     callID,
		direction:  msg.Direction,
		from:       msg.From,
		callerName: msg.CallerName,
		startTime:  time.Now(),
		transcript: make([]transcriptEntry, 0),
	}

	log.Printf("[PhoneBridge] Call started on %s: %s from %s (%s)", d.ID, callID, msg.From, msg.Direction)

	d.bridge.bot.ProcessSystemEvent(d.bridge.bot.config.AdminID,
		fmt.Sprintf("Phone call %s on device %s: %s call from %s (%s)",
			callID, d.ID, msg.Direction, msg.From, msg.CallerName))
}

func (d *PhoneDevice) handleCallActive() {
	d.mu.Lock()
	session := d.session
	d.mu.Unlock()

	if session == nil {
		log.Printf("[PhoneBridge] call_active but no session on device %s", d.ID)
		return
	}

	var prompt string
	if session.direction == "outgoing" && session.from != "" {
		prompt = fmt.Sprintf("%s\n\nThis is an outgoing call to %s. Purpose: %s",
			systemPromptVoice, session.from, session.callerName)
	} else {
		prompt = systemPromptVoice
		if session.callerName != "" {
			prompt = fmt.Sprintf("%s\n\nThe caller is %s (number: %s).",
				systemPromptVoice, session.callerName, session.from)
		} else if session.from != "" {
			prompt = fmt.Sprintf("%s\n\nIncoming call from %s.",
				systemPromptVoice, session.from)
		}
	}

	d.connectGemini(session, prompt)
}

func (d *PhoneDevice) handleAudio(base64Audio string) {
	d.mu.Lock()
	session := d.session
	d.mu.Unlock()

	if session == nil {
		return
	}

	session.mu.Lock()
	geminiConn := session.geminiConn
	closed := session.closed
	session.mu.Unlock()

	if geminiConn == nil || closed {
		return
	}

	pcmData, err := base64.StdEncoding.DecodeString(base64Audio)
	if err != nil {
		log.Printf("[PhoneBridge] Failed to decode audio from %s: %v", d.ID, err)
		return
	}

	// Send PCM 16kHz to Gemini
	input := geminiRealtimeInput{
		RealtimeInput: struct {
			MediaChunks []geminiBlob `json:"mediaChunks"`
		}{
			MediaChunks: []geminiBlob{
				{
					MimeType: "audio/pcm;rate=16000",
					Data:     base64.StdEncoding.EncodeToString(pcmData),
				},
			},
		},
	}

	session.mu.Lock()
	if !session.closed {
		err = geminiConn.WriteJSON(input)
	}
	session.mu.Unlock()

	if err != nil {
		log.Printf("[PhoneBridge] Failed to send audio to Gemini for %s: %v", d.ID, err)
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

	duration := time.Since(session.startTime)
	log.Printf("[PhoneBridge] Call ended on %s: %s (duration: %s)", d.ID, session.callID, duration.Round(time.Second))

	go d.generateSummary(session, duration)
}

func (d *PhoneDevice) connectGemini(session *phoneSession, prompt string) {
	geminiConn, err := d.bridge.voiceManager.ConnectGemini(prompt)
	if err != nil {
		log.Printf("[PhoneBridge] Failed to connect to Gemini for %s: %v", d.ID, err)
		d.bridge.bot.sendMessage(d.bridge.bot.config.AdminID,
			fmt.Sprintf("Failed to connect Gemini for phone call on %s: %v", d.ID, err))
		return
	}

	session.mu.Lock()
	session.geminiConn = geminiConn
	session.mu.Unlock()

	log.Printf("[PhoneBridge] Gemini connected for call %s on device %s", session.callID, d.ID)

	go d.geminiToPhone(session)
}

func (d *PhoneDevice) geminiToPhone(session *phoneSession) {
	defer func() {
		session.mu.Lock()
		if !session.closed {
			session.closed = true
			if session.geminiConn != nil {
				session.geminiConn.Close()
			}
		}
		session.mu.Unlock()
	}()

	for {
		session.mu.Lock()
		geminiConn := session.geminiConn
		closed := session.closed
		session.mu.Unlock()

		if closed || geminiConn == nil {
			return
		}

		var serverMsg geminiServerMsg
		if err := geminiConn.ReadJSON(&serverMsg); err != nil {
			if !session.closed {
				log.Printf("[PhoneBridge] Gemini read error for %s: %v", d.ID, err)
			}
			return
		}

		// Handle audio response from Gemini
		if serverMsg.ServerContent != nil && serverMsg.ServerContent.ModelTurn != nil {
			for _, part := range serverMsg.ServerContent.ModelTurn.Parts {
				if part.InlineData != nil && part.InlineData.Data != "" {
					// Decode Gemini's 24kHz PCM audio
					audioData, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
					if err != nil {
						log.Printf("[PhoneBridge] Failed to decode Gemini audio: %v", err)
						continue
					}

					// Resample from 24kHz to 16kHz for the phone
					resampled := resample24to16(audioData)

					// Send to phone as base64
					msg := PhoneMessage{
						Type: "audio",
						Data: base64.StdEncoding.EncodeToString(resampled),
					}
					data, _ := json.Marshal(msg)
					select {
					case d.send <- data:
					default:
						log.Printf("[PhoneBridge] Send buffer full for %s, dropping audio", d.ID)
					}
				}

				// Capture text transcript from Gemini
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

		// Handle transcript from user side
		if serverMsg.ServerContent != nil && serverMsg.ServerContent.InputTranscription != nil && serverMsg.ServerContent.InputTranscription.Text != "" {
			session.mu.Lock()
			session.transcript = append(session.transcript, transcriptEntry{
				Speaker: "user",
				Text:    serverMsg.ServerContent.InputTranscription.Text,
			})
			session.mu.Unlock()
		}

		// Handle transcript from assistant side
		if serverMsg.ServerContent != nil && serverMsg.ServerContent.OutputTranscription != nil && serverMsg.ServerContent.OutputTranscription.Text != "" {
			session.mu.Lock()
			session.transcript = append(session.transcript, transcriptEntry{
				Speaker: "assistant",
				Text:    serverMsg.ServerContent.OutputTranscription.Text,
			})
			session.mu.Unlock()
		}
	}
}

func (d *PhoneDevice) generateSummary(session *phoneSession, duration time.Duration) {
	from := session.from
	if session.callerName != "" {
		from = fmt.Sprintf("%s (%s)", session.callerName, session.from)
	}

	session.mu.Lock()
	transcript := make([]transcriptEntry, len(session.transcript))
	copy(transcript, session.transcript)
	session.mu.Unlock()

	GenerateCallSummary(d.bridge.bot, from, duration, transcript)
}
