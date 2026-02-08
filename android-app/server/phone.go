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
	upgrader     websocket.Upgrader
	conn         *websocket.Conn
	session      *phoneSession
	send         chan []byte
	mu           sync.Mutex
}

type phoneSession struct {
	bridge     *PhoneBridge
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
	Command    string `json:"command,omitempty"`
	Direction  string `json:"direction,omitempty"`
	From       string `json:"from,omitempty"`
	CallerName string `json:"caller_name,omitempty"`
	Data       string `json:"data,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	To         string `json:"to,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
}

func NewPhoneBridge(bot *Bot, voiceManager *VoiceManager) *PhoneBridge {
	return &PhoneBridge{
		bot:          bot,
		voiceManager: voiceManager,
		send:         make(chan []byte, 64),
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

	pb.mu.Lock()
	if pb.conn != nil {
		log.Printf("[PhoneBridge] Replacing existing connection")
		pb.conn.Close()
	}
	pb.conn = conn
	pb.mu.Unlock()

	log.Printf("[PhoneBridge] Connected")

	go pb.readPump()
	go pb.writePump()
}

func (pb *PhoneBridge) IsConnected() bool {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return pb.conn != nil
}

func (pb *PhoneBridge) MakeCall(to, purpose string) error {
	pb.mu.Lock()
	conn := pb.conn
	pb.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("no phone connected. The Minerva Bridge app must be running and connected via /phone")
	}

	msg := PhoneMessage{
		Type:    "command",
		Command: "make_call",
		To:      to,
		Purpose: purpose,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal make_call: %w", err)
	}

	select {
	case pb.send <- data:
		return nil
	default:
		return fmt.Errorf("send buffer full")
	}
}

func (pb *PhoneBridge) disconnect() {
	pb.mu.Lock()
	session := pb.session
	pb.session = nil
	pb.conn = nil
	pb.mu.Unlock()

	if session != nil {
		pb.handleCallEnd()
	}

	log.Printf("[PhoneBridge] Disconnected")
}

// readPump reads messages from the WebSocket connection.
func (pb *PhoneBridge) readPump() {
	defer func() {
		pb.disconnect()
	}()

	pb.mu.Lock()
	conn := pb.conn
	pb.mu.Unlock()

	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[PhoneBridge] Read error: %v", err)
			}
			return
		}

		var msg PhoneMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("[PhoneBridge] Invalid message: %v", err)
			continue
		}

		pb.handleMessage(msg)
	}
}

// writePump writes messages to the WebSocket connection with ping keepalive.
func (pb *PhoneBridge) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
	}()

	for {
		pb.mu.Lock()
		conn := pb.conn
		pb.mu.Unlock()

		if conn == nil {
			return
		}

		select {
		case message, ok := <-pb.send:
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("[PhoneBridge] Write error: %v", err)
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[PhoneBridge] Ping error: %v", err)
				return
			}
		}
	}
}

func (pb *PhoneBridge) handleMessage(msg PhoneMessage) {
	switch msg.Type {
	case "register":
		log.Printf("[PhoneBridge] Device registered")
		resp := PhoneMessage{Type: "registered"}
		data, _ := json.Marshal(resp)
		select {
		case pb.send <- data:
		default:
		}

	case "call_start":
		pb.handleCallStart(msg)

	case "call_active":
		pb.handleCallActive()

	case "audio":
		pb.handleAudio(msg.Data)

	case "call_end":
		pb.handleCallEnd()

	default:
		log.Printf("[PhoneBridge] Unknown message type: %s", msg.Type)
	}
}

func (pb *PhoneBridge) handleCallStart(msg PhoneMessage) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	if pb.session != nil {
		log.Printf("[PhoneBridge] Already has active session, closing old one")
		pb.mu.Unlock()
		pb.handleCallEnd()
		pb.mu.Lock()
	}

	callID := msg.CallID
	if callID == "" {
		callID = fmt.Sprintf("phone-%d", time.Now().UnixMilli())
	}

	pb.session = &phoneSession{
		bridge:     pb,
		callID:     callID,
		direction:  msg.Direction,
		from:       msg.From,
		callerName: msg.CallerName,
		startTime:  time.Now(),
		transcript: make([]transcriptEntry, 0),
	}

	log.Printf("[PhoneBridge] Call started: %s from %s (%s)", callID, msg.From, msg.Direction)

	pb.bot.ProcessSystemEvent(pb.bot.config.AdminID,
		fmt.Sprintf("Phone call %s: %s call from %s (%s)",
			callID, msg.Direction, msg.From, msg.CallerName))
}

func (pb *PhoneBridge) handleCallActive() {
	pb.mu.Lock()
	session := pb.session
	pb.mu.Unlock()

	if session == nil {
		log.Printf("[PhoneBridge] call_active but no session")
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

	pb.connectGemini(session, prompt)
}

func (pb *PhoneBridge) handleAudio(base64Audio string) {
	pb.mu.Lock()
	session := pb.session
	pb.mu.Unlock()

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
		log.Printf("[PhoneBridge] Failed to decode audio: %v", err)
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
		log.Printf("[PhoneBridge] Failed to send audio to Gemini: %v", err)
	}
}

func (pb *PhoneBridge) handleCallEnd() {
	pb.mu.Lock()
	session := pb.session
	pb.session = nil
	pb.mu.Unlock()

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
	log.Printf("[PhoneBridge] Call ended: %s (duration: %s)", session.callID, duration.Round(time.Second))

	go pb.generateSummary(session, duration)
}

func (pb *PhoneBridge) connectGemini(session *phoneSession, prompt string) {
	geminiConn, err := pb.voiceManager.ConnectGemini(prompt)
	if err != nil {
		log.Printf("[PhoneBridge] Failed to connect to Gemini: %v", err)
		pb.bot.sendMessage(pb.bot.config.AdminID,
			fmt.Sprintf("Failed to connect Gemini for phone call: %v", err))
		return
	}

	session.mu.Lock()
	session.geminiConn = geminiConn
	session.mu.Unlock()

	log.Printf("[PhoneBridge] Gemini connected for call %s", session.callID)

	go pb.geminiToPhone(session)
}

func (pb *PhoneBridge) geminiToPhone(session *phoneSession) {
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
				log.Printf("[PhoneBridge] Gemini read error: %v", err)
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
					case pb.send <- data:
					default:
						log.Printf("[PhoneBridge] Send buffer full, dropping audio")
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

func (pb *PhoneBridge) generateSummary(session *phoneSession, duration time.Duration) {
	from := session.from
	if session.callerName != "" {
		from = fmt.Sprintf("%s (%s)", session.callerName, session.from)
	}

	session.mu.Lock()
	transcript := make([]transcriptEntry, len(session.transcript))
	copy(transcript, session.transcript)
	session.mu.Unlock()

	GenerateCallSummary(pb.bot, from, duration, transcript)
}
