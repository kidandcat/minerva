package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// --- Gemini Live API message types ---

type geminiSetupMsg struct {
	Setup struct {
		Model              string           `json:"model"`
		GenerationConfig   geminiGenConfig  `json:"generationConfig"`
		SystemInstruction  *geminiContent   `json:"systemInstruction,omitempty"`
		Tools              []map[string]any `json:"tools,omitempty"`
	} `json:"setup"`
}

type geminiGenConfig struct {
	ResponseModalities []string        `json:"responseModalities"`
	SpeechConfig       *geminiSpeechCfg `json:"speechConfig,omitempty"`
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

type geminiServerMsg struct {
	SetupComplete *json.RawMessage `json:"setupComplete,omitempty"`
	ServerContent *struct {
		ModelTurn *struct {
			Parts []geminiPart `json:"parts"`
		} `json:"modelTurn,omitempty"`
		TurnComplete        *bool `json:"turnComplete,omitempty"`
		InputTranscription  *struct {
			Text string `json:"text"`
		} `json:"inputTranscription,omitempty"`
		OutputTranscription *struct {
			Text string `json:"text"`
		} `json:"outputTranscription,omitempty"`
	} `json:"serverContent,omitempty"`
	ToolCall *struct {
		FunctionCalls []struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
			ID   string         `json:"id"`
		} `json:"functionCalls"`
	} `json:"toolCall,omitempty"`
}

type transcriptEntry struct {
	Speaker string
	Text    string
}

// --- VoiceManager ---

const (
	geminiWSURL       = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	geminiModel       = "models/gemini-2.5-flash-native-audio-latest"
	geminiVoice       = "Zephyr"
	callTimeout       = 5 * time.Minute
	systemPromptVoice = `You are Minerva, a personal AI assistant. You are answering an incoming phone call.

Instructions:
- Speak in Spanish by default, switch to English if the caller speaks English
- Be friendly, natural and conversational
- The owner is not available right now, so take a message
- Ask who is calling, the reason, and if they want to leave a message
- Keep responses short and concise
- If they ask when the owner will be available, say you don't know but will pass the message
- At the end of the conversation, say goodbye politely`
)

// VoiceManager manages Gemini Live sessions for phone calls via the phone bridge.
type VoiceManager struct {
	bot    *Bot
	apiKey string
}

// NewVoiceManager creates a new VoiceManager instance.
func NewVoiceManager(bot *Bot, apiKey string) *VoiceManager {
	return &VoiceManager{
		bot:    bot,
		apiKey: apiKey,
	}
}

// IncomingCallPrompt returns the system prompt for incoming calls.
func IncomingCallPrompt(from string) string {
	return fmt.Sprintf(`You are Minerva, a personal AI assistant. You are answering an incoming phone call.

Caller: %s

Instructions:
- Speak in Spanish by default, switch to English if the caller speaks English
- Be friendly, natural and conversational
- The owner is not available right now, so take a message
- Ask who is calling (if you don't know), the reason for the call, and if they want to leave a message
- Keep responses short and concise
- If they ask when the owner will be available, say you don't know but will pass the message
- At the end of the conversation, say goodbye politely`, from)
}

// ConnectGemini establishes a WebSocket connection to Gemini Live API and sends the setup message.
func (vm *VoiceManager) ConnectGemini(prompt string) (*websocket.Conn, error) {
	u, err := url.Parse(geminiWSURL)
	if err != nil {
		return nil, fmt.Errorf("parse gemini URL: %w", err)
	}
	q := u.Query()
	q.Set("key", vm.apiKey)
	u.RawQuery = q.Encode()

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial gemini: %w", err)
	}

	// Build setup message
	setup := geminiSetupMsg{}
	setup.Setup.Model = geminiModel
	setup.Setup.GenerationConfig = geminiGenConfig{
		ResponseModalities: []string{"AUDIO"},
		SpeechConfig: &geminiSpeechCfg{},
	}
	setup.Setup.GenerationConfig.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName = geminiVoice

	if prompt != "" {
		setup.Setup.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: prompt}},
		}
	}

	if err := conn.WriteJSON(setup); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send setup: %w", err)
	}

	// Wait for setup complete
	_, msg, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read setup response: %w", err)
	}

	var serverMsg geminiServerMsg
	if err := json.Unmarshal(msg, &serverMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse setup response: %w", err)
	}

	if serverMsg.SetupComplete == nil {
		conn.Close()
		return nil, fmt.Errorf("expected setupComplete, got: %s", string(msg))
	}

	log.Println("[voice] Gemini Live connected and setup complete")
	return conn, nil
}

// SendAudioToGemini sends PCM 16kHz audio data to Gemini via the WebSocket connection.
// The audio should be raw PCM 16-bit mono 16kHz bytes.
func SendAudioToGemini(geminiConn *websocket.Conn, pcmData []byte) error {
	encoded := base64.StdEncoding.EncodeToString(pcmData)

	msg := geminiRealtimeInput{}
	msg.RealtimeInput.MediaChunks = []geminiBlob{
		{
			MimeType: "audio/pcm;rate=16000",
			Data:     encoded,
		},
	}

	return geminiConn.WriteJSON(msg)
}

// ReadGeminiResponses reads from the Gemini WebSocket and forwards audio to the phone bridge.
// It returns transcript entries collected during the call.
// phoneSend is a callback that sends base64-encoded PCM 16kHz audio to the phone bridge.
func ReadGeminiResponses(geminiConn *websocket.Conn, phoneSend func(audioBase64 string) error, done chan struct{}) []transcriptEntry {
	var transcript []transcriptEntry
	var currentText strings.Builder

	for {
		select {
		case <-done:
			return transcript
		default:
		}

		_, msg, err := geminiConn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[voice] Gemini read error: %v", err)
			}
			return transcript
		}

		var serverMsg geminiServerMsg
		if err := json.Unmarshal(msg, &serverMsg); err != nil {
			log.Printf("[voice] Failed to parse Gemini message: %v", err)
			continue
		}

		if serverMsg.ServerContent != nil {
			if serverMsg.ServerContent.ModelTurn != nil {
				for _, part := range serverMsg.ServerContent.ModelTurn.Parts {
					// Handle audio output
					if part.InlineData != nil && strings.HasPrefix(part.InlineData.MimeType, "audio/pcm") {
						audioBytes, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
						if err != nil {
							log.Printf("[voice] Failed to decode Gemini audio: %v", err)
							continue
						}

						// Gemini outputs 24kHz, resample to 16kHz for phone
						resampled := resample24to16(audioBytes)

						// Encode and send to phone bridge
						encoded := base64.StdEncoding.EncodeToString(resampled)
						if err := phoneSend(encoded); err != nil {
							log.Printf("[voice] Failed to send audio to phone: %v", err)
						}
					}

					// Handle text (transcript)
					if part.Text != "" {
						currentText.WriteString(part.Text)
					}
				}
			}

			if serverMsg.ServerContent.TurnComplete != nil && *serverMsg.ServerContent.TurnComplete {
				if currentText.Len() > 0 {
					transcript = append(transcript, transcriptEntry{
						Speaker: "Minerva",
						Text:    currentText.String(),
					})
					currentText.Reset()
				}
			}
		}
	}
}

// GenerateCallSummary creates a summary of the call using the AI brain and notifies the admin.
func GenerateCallSummary(bot *Bot, from string, duration time.Duration, transcript []transcriptEntry) {
	if len(transcript) == 0 {
		bot.sendMessage(bot.config.AdminID,
			fmt.Sprintf("📞 Llamada de %s (duración: %s) - sin transcripción", from, duration.Round(time.Second)))
		return
	}

	// Build transcript text
	var sb strings.Builder
	for _, entry := range transcript {
		sb.WriteString(fmt.Sprintf("%s: %s\n", entry.Speaker, entry.Text))
	}

	prompt := fmt.Sprintf(`Genera un resumen breve de esta llamada telefónica.

Llamante: %s
Duración: %s

Transcripción:
%s

Responde con un resumen conciso en español que incluya:
- Quién llamó (si se identificó)
- El motivo de la llamada
- Cualquier mensaje o información importante
- Si se necesita alguna acción`, from, duration.Round(time.Second), sb.String())

	summary, err := bot.ai.Chat(prompt)
	if err != nil {
		log.Printf("[voice] Failed to generate call summary: %v", err)
		bot.sendMessage(bot.config.AdminID,
			fmt.Sprintf("📞 Llamada de %s (duración: %s). Error generando resumen: %v", from, duration.Round(time.Second), err))
		return
	}

	// Notify admin via Telegram
	bot.sendMessage(bot.config.AdminID,
		fmt.Sprintf("📞 *Llamada finalizada*\nDe: %s\nDuración: %s\n\n%s", from, duration.Round(time.Second), summary))

	// Process as system event for follow-up actions
	bot.ProcessSystemEvent(bot.config.AdminID,
		fmt.Sprintf("Llamada telefónica de %s (duración: %s). Resumen: %s\n\nSi hay acciones pendientes, créalas.", from, duration.Round(time.Second), summary))
}
