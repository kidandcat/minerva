package main

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"
)

// upgrader is the shared WebSocket upgrader for all endpoints
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Security middleware and helpers

// localhostOnly rejects any request that doesn't come from localhost.
// Used for CLI-only endpoints that should never be exposed externally.
func localhostOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If there's an X-Forwarded-For header, it came through Caddy (external)
		if r.Header.Get("X-Forwarded-For") != "" {
			http.Error(w, `{"error": "not found"}`, http.StatusNotFound)
			return
		}
		host := r.RemoteAddr
		if !strings.HasPrefix(host, "127.0.0.1:") && !strings.HasPrefix(host, "[::1]:") {
			http.Error(w, `{"error": "not found"}`, http.StatusNotFound)
			return
		}
		next(w, r)
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Also check query param for WebSocket upgrade requests
	return r.URL.Query().Get("token")
}

// wsAuthMiddleware validates authentication BEFORE upgrading to WebSocket.
// It also checks the Origin header against allowed origins.
func wsAuthMiddleware(password string, allowedOrigins []string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check auth token
		if password != "" {
			token := extractBearerToken(r)
			if token == "" {
				http.Error(w, `{"error": "unauthorized"}`, http.StatusForbidden)
				return
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(password)) != 1 {
				http.Error(w, `{"error": "unauthorized"}`, http.StatusForbidden)
				return
			}
		}

		// Validate Origin header if allowedOrigins is configured
		if len(allowedOrigins) > 0 {
			origin := r.Header.Get("Origin")
			if origin != "" && !isAllowedOrigin(origin, allowedOrigins) {
				http.Error(w, `{"error": "origin not allowed"}`, http.StatusForbidden)
				return
			}
		}

		next(w, r)
	}
}

// telnyxWebhookAuth validates Telnyx webhook signatures using Ed25519.
// The public key is obtained from Telnyx Mission Control Portal.
func telnyxWebhookAuth(publicKeyBase64 string, next http.HandlerFunc) http.HandlerFunc {
	pubKey, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil {
		log.Printf("[Security] WARNING: invalid Telnyx public key, webhook auth disabled: %v", err)
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		signature := r.Header.Get("telnyx-signature-ed25519")
		timestamp := r.Header.Get("telnyx-timestamp")
		if signature == "" || timestamp == "" {
			log.Printf("[Security] Telnyx webhook rejected: missing signature headers for %s", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		// Read the body for verification, then restore it
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		// Build signed payload: timestamp|body
		signedPayload := timestamp + "|" + string(body)

		sig, err := base64.StdEncoding.DecodeString(signature)
		if err != nil {
			log.Printf("[Security] Telnyx webhook rejected: invalid signature encoding for %s", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		if !ed25519.Verify(ed25519.PublicKey(pubKey), []byte(signedPayload), sig) {
			log.Printf("[Security] Telnyx webhook rejected: invalid signature for %s", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next(w, r)
	}
}

func isAllowedOrigin(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(origin, a) {
			return true
		}
	}
	return false
}

// maxBodyMiddleware limits request body size to prevent DoS via memory exhaustion.
func maxBodyMiddleware(maxBytes int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next(w, r)
	}
}

// RateLimiter implements per-IP rate limiting.
type RateLimiter struct {
	visitors map[string]*visitorEntry
	mu       sync.Mutex
	rate     rate.Limit
	burst    int
}

type visitorEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter creates a rate limiter with the given requests per second and burst size.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*visitorEntry),
		rate:     rate.Limit(rps),
		burst:    burst,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) getVisitor(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.rate, rl.burst)
		rl.visitors[ip] = &visitorEntry{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	v.lastSeen = time.Now()
	return v.limiter
}

func (rl *RateLimiter) cleanup() {
	for {
		time.Sleep(5 * time.Minute)
		rl.mu.Lock()
		for ip, v := range rl.visitors {
			if time.Since(v.lastSeen) > 10*time.Minute {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// Middleware wraps a handler with rate limiting.
func (rl *RateLimiter) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		// Use X-Forwarded-For if behind a proxy (Caddy)
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			ip = strings.Split(forwarded, ",")[0]
			ip = strings.TrimSpace(ip)
		}

		limiter := rl.getVisitor(ip)
		if !limiter.Allow() {
			http.Error(w, `{"error": "rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next(w, r)
	}
}

// chainMiddleware applies multiple middleware in order (outermost first).
func chainMiddleware(handler http.HandlerFunc, middlewares ...func(http.HandlerFunc) http.HandlerFunc) http.HandlerFunc {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// parseAllowedOrigins parses the ALLOWED_ORIGINS env var (comma-separated).
func parseAllowedOrigins() []string {
	origins := os.Getenv("ALLOWED_ORIGINS")
	if origins == "" {
		return nil
	}
	parts := strings.Split(origins, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// validateDirField checks that a directory path is safe (no path traversal).
func validateDirField(dir string) error {
	if dir == "" {
		return nil
	}
	// Reject path traversal
	if strings.Contains(dir, "..") {
		return fmt.Errorf("directory path must not contain '..'")
	}
	// Must be absolute
	if !strings.HasPrefix(dir, "/") && !strings.HasPrefix(dir, "~") {
		return fmt.Errorf("directory must be an absolute path")
	}
	// Reject dangerous root paths
	dangerousPrefixes := []string{"/etc", "/proc", "/sys", "/dev", "/boot", "/sbin"}
	dirLower := strings.ToLower(dir)
	for _, prefix := range dangerousPrefixes {
		if dirLower == prefix || strings.HasPrefix(dirLower, prefix+"/") {
			return fmt.Errorf("directory %q is not allowed", dir)
		}
	}
	return nil
}

// httpClientWithTimeout returns an http.Client with a reasonable timeout.
var httpClientWithTimeout = &http.Client{Timeout: 30 * time.Second}
