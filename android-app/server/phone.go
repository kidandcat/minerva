package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type PhoneBridge struct {
	bot            *Bot
	voiceManager   *VoiceManager
	upgrader       websocket.Upgrader
	conn           *websocket.Conn
	session        *phoneSession
	send           chan []byte
	pendingPurpose string // stored from MakeCall for outgoing call prompt
	mu             sync.Mutex
}

type phoneSession struct {
	bridge      *PhoneBridge
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
		send:         make(chan []byte, 256),
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

	pb.pendingPurpose = purpose

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

	audioInCount = 0
	audioOutCount = 0
	log.Printf("[PhoneBridge] Call started: %s from %s (%s)", callID, msg.From, msg.Direction)

	// Connect to Gemini immediately (don't wait for call_active) so it's ready
	// by the time the other party picks up
	session := pb.session
	purpose := pb.pendingPurpose
	pb.pendingPurpose = ""
	var prompt string
	if session.direction == "outgoing" {
		prompt = fmt.Sprintf("%s\n\nThis is an outgoing call to %s. Purpose: %s",
			systemPromptVoice, msg.To, purpose)
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

	go pb.connectGemini(session, prompt)
}


// enableIncallMusicMixer enables routing of MultiMedia1 audio into the active voice call
// via the Qualcomm MSM audio HAL mixer control.
func enableIncallMusicMixer(enable bool) {
	val := "0"
	if enable {
		val = "1"
	}
	// Enable both MultiMedia1 and MultiMedia2 for incall music routing.
	// mixer_paths.xml uses MultiMedia2 (832), but we enable both to be safe.
	// Incall_Music_2 variants (827,828) are for the second voice session.
	controls := []int{831, 832, 827, 828}
	for _, ctrl := range controls {
		cmd := exec.Command("su", "-c", fmt.Sprintf("tinymix %d %s", ctrl, val))
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[PhoneBridge] tinymix %d incall_music %s failed: %v (%s)", ctrl, val, err, string(out))
		}
	}
	log.Printf("[PhoneBridge] Incall_Music mixers %s (831,832,827,828)", map[bool]string{true: "ENABLED", false: "DISABLED"}[enable])
}


func (pb *PhoneBridge) handleCallActive() {
	pb.mu.Lock()
	session := pb.session
	pb.mu.Unlock()

	if session == nil {
		log.Printf("[PhoneBridge] call_active but no session")
		return
	}

	log.Printf("[PhoneBridge] Call active: %s", session.callID)

	// Enable incall music mixer to route our playback audio into the voice call uplink
	enableIncallMusicMixer(true)
}

var audioInCount int
var audioOutCount int

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
		audioInCount++
		if audioInCount <= 5 || audioInCount%100 == 0 {
			log.Printf("[PhoneBridge] Audio IN dropped #%d: conn=%v, closed=%v", audioInCount, geminiConn != nil, closed)
		}
		return
	}

	audioInCount++
	pcmData, err := base64.StdEncoding.DecodeString(base64Audio)
	if err != nil {
		log.Printf("[PhoneBridge] Failed to decode audio: %v", err)
		return
	}

	// Log audio energy to verify it's not silence
	if audioInCount == 1 || audioInCount%100 == 0 {
		rms := pcmRMS(pcmData)
		log.Printf("[PhoneBridge] Audio IN #%d: %d bytes, RMS=%.1f", audioInCount, len(pcmData), rms)
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

	// Disable call audio mixers
	go enableIncallMusicMixer(false)

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

	// No clientContent text here - let Gemini respond to actual audio via realtimeInput.
	// Mixing clientContent text with realtimeInput audio breaks Gemini's audio mode.

	session.mu.Lock()
	session.geminiConn = geminiConn
	session.mu.Unlock()

	log.Printf("[PhoneBridge] Gemini connected for call %s", session.callID)

	go pb.geminiToPhone(session)
}

// pcmRMS calculates the RMS (root mean square) energy of 16-bit little-endian PCM audio.
// Returns 0 for silence, ~32768 for max amplitude.
func pcmRMS(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	samples := len(pcm) / 2
	var sumSq float64
	for i := 0; i < samples; i++ {
		sample := int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2]))
		sumSq += float64(sample) * float64(sample)
	}
	return math.Sqrt(sumSq / float64(samples))
}

func (pb *PhoneBridge) geminiToPhone(session *phoneSession) {
	log.Printf("[PhoneBridge] geminiToPhone started for %s", session.callID)
	defer func() {
		session.mu.Lock()
		wasClosed := session.closed
		if !session.closed {
			session.closed = true
			if session.geminiConn != nil {
				session.geminiConn.Close()
			}
		}
		session.mu.Unlock()
		log.Printf("[PhoneBridge] geminiToPhone exiting for %s (wasClosed=%v)", session.callID, wasClosed)
	}()

	geminiMsgCount := 0

	for {
		session.mu.Lock()
		geminiConn := session.geminiConn
		closed := session.closed
		session.mu.Unlock()

		if closed || geminiConn == nil {
			log.Printf("[PhoneBridge] geminiToPhone loop exit: closed=%v, conn=%v", closed, geminiConn != nil)
			return
		}

		var serverMsg geminiServerMsg
		if err := geminiConn.ReadJSON(&serverMsg); err != nil {
			if !session.closed {
				log.Printf("[PhoneBridge] Gemini read error: %v", err)
			}
			return
		}

		geminiMsgCount++

		// Log message type for debugging
		msgType := "unknown"
		if serverMsg.ServerContent != nil {
			if serverMsg.ServerContent.ModelTurn != nil {
				msgType = "modelTurn"
			} else if serverMsg.ServerContent.TurnComplete != nil {
				msgType = "turnComplete"
			} else if serverMsg.ServerContent.InputTranscription != nil {
				msgType = "inputTranscription"
			} else if serverMsg.ServerContent.OutputTranscription != nil {
				msgType = "outputTranscription"
			} else {
				msgType = "serverContent(other)"
			}
		} else if serverMsg.ToolCall != nil {
			msgType = "toolCall"
		}
		if geminiMsgCount <= 10 || geminiMsgCount%50 == 0 {
			log.Printf("[PhoneBridge] Gemini msg #%d: type=%s", geminiMsgCount, msgType)
		}

		// Handle audio response from Gemini
		if serverMsg.ServerContent != nil && serverMsg.ServerContent.ModelTurn != nil {
			for _, part := range serverMsg.ServerContent.ModelTurn.Parts {
				if part.InlineData != nil && part.InlineData.Data != "" {
					audioOutCount++
					if audioOutCount == 1 || audioOutCount%50 == 0 {
						log.Printf("[PhoneBridge] Audio OUT from Gemini: %d chunks", audioOutCount)
					}
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
			log.Printf("[PhoneBridge] Input transcription: %s", serverMsg.ServerContent.InputTranscription.Text)
			session.mu.Lock()
			session.transcript = append(session.transcript, transcriptEntry{
				Speaker: "user",
				Text:    serverMsg.ServerContent.InputTranscription.Text,
			})
			session.mu.Unlock()
		}

		// Handle transcript from assistant side
		if serverMsg.ServerContent != nil && serverMsg.ServerContent.OutputTranscription != nil && serverMsg.ServerContent.OutputTranscription.Text != "" {
			log.Printf("[PhoneBridge] Output transcription: %s", serverMsg.ServerContent.OutputTranscription.Text)
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
