package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type RelayClient struct {
	relayURL    string
	key         string
	localPort   int
	conn        *websocket.Conn
	cipher      cipher.AEAD
	mu          sync.Mutex
	httpClient  *http.Client
	stopCh      chan struct{}
	agentConns  map[string]*websocket.Conn
	agentConnMu sync.RWMutex
}

type RelayMessage struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"` // request, response, encrypted, agent_ws_open, agent_ws_message, agent_ws_close
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Status    int               `json:"status,omitempty"`
	Encrypted string            `json:"encrypted,omitempty"`
	AgentID   string            `json:"agent_id,omitempty"`
	WSData    string            `json:"ws_data,omitempty"`
}

func NewRelayClient(relayURL, key string, localPort int) *RelayClient {
	// Derive encryption key from secret
	hash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		log.Fatalf("Failed to create cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		log.Fatalf("Failed to create GCM: %v", err)
	}

	return &RelayClient{
		relayURL:   relayURL,
		key:        key,
		localPort:  localPort,
		cipher:     gcm,
		httpClient: &http.Client{Timeout: 25 * time.Second},
		stopCh:     make(chan struct{}),
		agentConns: make(map[string]*websocket.Conn),
	}
}

func (rc *RelayClient) encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, rc.cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := rc.cipher.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (rc *RelayClient) decrypt(encoded string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}

	nonceSize := rc.cipher.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, io.ErrShortBuffer
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return rc.cipher.Open(nil, nonce, ciphertext, nil)
}

func (rc *RelayClient) Start() {
	go rc.connectLoop()
}

func (rc *RelayClient) Stop() {
	close(rc.stopCh)
	rc.mu.Lock()
	if rc.conn != nil {
		rc.conn.Close()
	}
	rc.mu.Unlock()
}

func (rc *RelayClient) connectLoop() {
	for {
		select {
		case <-rc.stopCh:
			return
		default:
		}

		err := rc.connect()
		if err != nil {
			log.Printf("Relay connection failed: %v, retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		rc.handleMessages()
		log.Println("Relay connection lost, reconnecting...")
		time.Sleep(1 * time.Second)
	}
}

func (rc *RelayClient) connect() error {
	u, _ := url.Parse(rc.relayURL)
	u.Path = "/brain/ws"
	q := u.Query()
	q.Set("key", rc.key)
	u.RawQuery = q.Encode()

	// Convert http(s) to ws(s)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}

	rc.mu.Lock()
	rc.conn = conn
	rc.mu.Unlock()

	log.Printf("Connected to relay: %s (encrypted)", rc.relayURL)
	return nil
}

func (rc *RelayClient) handleMessages() {
	// Keepalive ticker - send ping every 15 seconds to keep connection alive on mobile networks
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// Read messages in goroutine
	msgCh := make(chan []byte)
	errCh := make(chan error)

	go func() {
		for {
			_, msg, err := rc.conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case <-ticker.C:
			// Send WebSocket ping frame to keep connection alive
			rc.mu.Lock()
			if rc.conn != nil {
				rc.conn.WriteMessage(websocket.PingMessage, nil)
			}
			rc.mu.Unlock()

		case err := <-errCh:
			log.Printf("Relay read error: %v", err)
			return

		case msg := <-msgCh:
			var envelope RelayMessage
			if err := json.Unmarshal(msg, &envelope); err != nil {
				log.Printf("Invalid relay message: %v", err)
				continue
			}

			if envelope.Type == "encrypted" && envelope.Encrypted != "" {
				// Decrypt the request
				decrypted, err := rc.decrypt(envelope.Encrypted)
				if err != nil {
					log.Printf("Decrypt error: %v", err)
					continue
				}

				var req RelayMessage
				if err := json.Unmarshal(decrypted, &req); err != nil {
					log.Printf("Invalid decrypted message: %v", err)
					continue
				}

				log.Printf("[Relay] Received message type: %s", req.Type)
				switch req.Type {
				case "request":
					go rc.handleRequest(req)
				case "agent_ws_open":
					// Run synchronously to ensure connection is ready before messages arrive
					rc.handleAgentOpen(req)
				case "agent_ws_message":
					rc.handleAgentMessage(req)
				case "agent_ws_close":
					rc.handleAgentClose(req)
				default:
					log.Printf("[Relay] Unknown message type: %s", req.Type)
				}
			}
		}
	}
}

func (rc *RelayClient) handleRequest(req RelayMessage) {
	// Forward request to local server
	localURL := fmt.Sprintf("http://127.0.0.1:%d%s", rc.localPort, req.Path)

	httpReq, err := http.NewRequest(req.Method, localURL, bytes.NewBufferString(req.Body))
	if err != nil {
		rc.sendResponse(req.ID, 500, nil, "Failed to create request")
		return
	}

	// Set headers
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	// Execute request
	resp, err := rc.httpClient.Do(httpReq)
	if err != nil {
		log.Printf("[Relay] Backend request failed: %v", err)
		rc.sendResponse(req.ID, 502, nil, "backend unavailable")
		return
	}
	defer resp.Body.Close()

	// Read response body
	body, _ := io.ReadAll(resp.Body)

	// Extract headers
	headers := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	rc.sendResponse(req.ID, resp.StatusCode, headers, string(body))
}

func (rc *RelayClient) sendResponse(id string, status int, headers map[string]string, body string) {
	resp := RelayMessage{
		ID:      id,
		Type:    "response",
		Status:  status,
		Headers: headers,
		Body:    body,
	}

	// Encrypt the response
	respBytes, _ := json.Marshal(resp)
	encrypted, err := rc.encrypt(respBytes)
	if err != nil {
		log.Printf("Encrypt error: %v", err)
		return
	}

	envelope := RelayMessage{
		Type:      "encrypted",
		Encrypted: encrypted,
	}
	data, _ := json.Marshal(envelope)

	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.conn != nil {
		rc.conn.WriteMessage(websocket.TextMessage, data)
	}
}

func (rc *RelayClient) handleAgentOpen(req RelayMessage) {
	log.Printf("[Relay] handleAgentOpen START for agent %s", req.AgentID)

	// Connect to local Minerva's agent WebSocket endpoint
	localURL := fmt.Sprintf("ws://127.0.0.1:%d/agent", rc.localPort)
	log.Printf("[Relay] Dialing local: %s", localURL)

	conn, _, err := websocket.DefaultDialer.Dial(localURL, nil)
	if err != nil {
		log.Printf("[Relay] FAILED to connect agent %s to local WS: %v", req.AgentID, err)
		rc.sendAgentClose(req.AgentID)
		return
	}

	log.Printf("[Relay] Local connection established for agent %s", req.AgentID)

	rc.agentConnMu.Lock()
	rc.agentConns[req.AgentID] = conn
	rc.agentConnMu.Unlock()

	log.Printf("[Relay] Agent %s added to agentConns map, total: %d", req.AgentID, len(rc.agentConns))

	// Read messages from local and forward to relay
	go func() {
		defer func() {
			log.Printf("[Relay] Agent %s local connection closing", req.AgentID)
			conn.Close()
			rc.agentConnMu.Lock()
			delete(rc.agentConns, req.AgentID)
			rc.agentConnMu.Unlock()
			rc.sendAgentClose(req.AgentID)
		}()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[Relay] Agent %s local read error: %v", req.AgentID, err)
				return
			}
			msgStr := string(msg)
			if len(msgStr) > 100 {
				msgStr = msgStr[:100]
			}
			log.Printf("[Relay] Agent %s local message: %s", req.AgentID, msgStr)
			rc.sendAgentMessage(req.AgentID, string(msg))
		}
	}()
}

func (rc *RelayClient) handleAgentMessage(req RelayMessage) {
	dataPreview := req.WSData
	if len(dataPreview) > 100 {
		dataPreview = dataPreview[:100]
	}
	log.Printf("[Relay] handleAgentMessage for agent %s: %s", req.AgentID, dataPreview)

	rc.agentConnMu.RLock()
	conn, ok := rc.agentConns[req.AgentID]
	connCount := len(rc.agentConns)
	rc.agentConnMu.RUnlock()

	log.Printf("[Relay] Looking for agent %s in map (total conns: %d, found: %v)", req.AgentID, connCount, ok)

	if ok && conn != nil {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(req.WSData)); err != nil {
			log.Printf("[Relay] Error writing to local agent %s: %v", req.AgentID, err)
		} else {
			log.Printf("[Relay] Forwarded message to local agent %s", req.AgentID)
		}
	} else {
		log.Printf("[Relay] No local connection for agent %s (ok=%v, conn=%v)", req.AgentID, ok, conn != nil)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (rc *RelayClient) handleAgentClose(req RelayMessage) {
	rc.agentConnMu.Lock()
	if conn, ok := rc.agentConns[req.AgentID]; ok {
		conn.Close()
		delete(rc.agentConns, req.AgentID)
	}
	rc.agentConnMu.Unlock()
	log.Printf("Agent %s disconnected via relay", req.AgentID)
}

func (rc *RelayClient) sendAgentMessage(agentID, data string) {
	msg := RelayMessage{
		Type:    "agent_ws_message",
		AgentID: agentID,
		WSData:  data,
	}
	rc.sendEncrypted(msg)
}

func (rc *RelayClient) sendAgentClose(agentID string) {
	msg := RelayMessage{
		Type:    "agent_ws_close",
		AgentID: agentID,
	}
	rc.sendEncrypted(msg)
}

func (rc *RelayClient) sendEncrypted(msg RelayMessage) {
	msgBytes, _ := json.Marshal(msg)
	encrypted, err := rc.encrypt(msgBytes)
	if err != nil {
		log.Printf("Encrypt error: %v", err)
		return
	}

	envelope := RelayMessage{
		Type:      "encrypted",
		Encrypted: encrypted,
	}
	data, _ := json.Marshal(envelope)

	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.conn != nil {
		rc.conn.WriteMessage(websocket.TextMessage, data)
	}
}
