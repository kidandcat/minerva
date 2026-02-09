# Minerva Security Audit

**Date**: 2026-02-09
**Scope**: Full codebase review (server, agent, android-app)
**Auditor**: Automated analysis + manual review

---

## Summary

| Severity | Count |
|----------|-------|
| Critical | 4     |
| High     | 6     |
| Medium   | 7     |
| Low      | 5     |

---

## Critical

### SEC-01: Hardcoded Relay Key in Git History

**Files**: `android-app/agent/run-agent-mac.sh`, `android-app/agent/run-agent-vps.sh`
**Lines**: Default value `21ZUY8q6DPUbC8ZzbaFoIilfCDTjJw6` in `RELAY_KEY` variable

The relay key is hardcoded as a default in shell scripts AND committed to git history across multiple commits. Even if removed from current HEAD, the key is recoverable from git history.

```bash
RELAY_KEY="${RELAY_KEY:-21ZUY8q6DPUbC8ZzbaFoIilfCDTjJw6}"
```

**Impact**: Anyone with repo access can extract the relay key, connect to the relay server, and impersonate an agent.
**Fix**: Rotate the relay key immediately. Remove the hardcoded default. Use `git filter-branch` or BFG to scrub from history if repo is public. Always load from env vars or a secrets manager.

---

### SEC-02: Broken Twilio Request Signature Validation

**File**: `twilio.go:481-527`

The Twilio signature validation uses a **completely broken custom HMAC-SHA1 implementation** that just returns the raw input data:

```go
func sha1Sum(data []byte) []byte {
    // Placeholder - in production use crypto/sha1
    return data  // ← DOES NOTHING
}
```

Additionally, the `calculateSignature` function has `sort.Strings(keys)` commented out (line 488), which means the parameter order is non-deterministic and would never match even if the hash worked.

**Impact**: Any attacker can forge Twilio webhook requests. The `/twilio/voice` and `/twilio/ws` endpoints accept spoofed calls. An attacker could trigger fake incoming calls or inject messages into active call sessions.
**Fix**: Use `crypto/hmac` + `crypto/sha1` properly, or use the official Twilio Go helper library for request validation.

---

### SEC-03: HTTP Endpoints Without Authentication

**File**: `webhook.go:66-99`

Multiple endpoints that perform sensitive actions have **zero authentication**:

| Endpoint | Risk |
|----------|------|
| `POST /voice/call` | Initiate outbound phone calls to arbitrary numbers |
| `POST /agent/run` | Execute arbitrary code on remote agents with full permissions |
| `GET /agent/list` | Enumerate connected agents and their working directories |
| `POST /phone/call` | Make calls via Android phone bridge |
| `GET /phone/list` | Enumerate connected phones |

Anyone who can reach the server (internet-exposed via Caddy) can:
- Make phone calls to any number, incurring costs
- Execute arbitrary code on all connected agents (which run `claude --dangerously-skip-permissions`)
- List all connected infrastructure

**Impact**: Complete takeover of the assistant's capabilities. Financial abuse via Twilio. Arbitrary code execution on all agent machines.
**Fix**: Add authentication middleware (API key, JWT, or at minimum a shared secret header) to all sensitive endpoints. The `/agent` WebSocket has password auth, but the REST endpoints don't.

---

### SEC-04: Claude CLI Runs with --dangerously-skip-permissions

**Files**: `ai.go:200`, `task_runner.go:66`, `agent/executor.go:33`

All Claude CLI invocations use `--dangerously-skip-permissions`, and user-controlled input (Telegram messages) flows through to the prompt:

```go
// ai.go - user Telegram messages become the prompt
cmd := exec.CommandContext(ctx, "claude", args...)
// prompt includes user message content directly

// task_runner.go - description from user becomes TASK.md content
taskContent := fmt.Sprintf(`# Task\n\n%s\n...`, description)
// Then Claude reads it with full shell access
```

Combined with SEC-03 (unauthenticated `/agent/run`), an external attacker can execute arbitrary commands on any agent machine.

**Impact**: Any Telegram user (even non-admin, see SEC-05) or HTTP caller can achieve remote code execution via prompt injection into the Claude CLI process.
**Fix**: This is by design for a personal assistant, but for multi-tenant: sandbox Claude execution, restrict file system access, and never pass untrusted input directly as prompts without sanitization.

---

## High

### SEC-05: Non-Admin Users Can Interact Before Approval

**File**: `bot.go:79-101`

The bot processes all incoming messages and commands asynchronously. While there's a user approval system (`approved` column in users table), the `GetOrCreateUser` function auto-creates user records for anyone who messages the bot. The enforcement of the approval check needs verification on every handler path.

**Impact**: If any handler path doesn't check `user.Approved`, unauthorized users could interact with the assistant.
**Fix**: Add middleware-style authentication check at the top of `handleMessage` and `handleCommand` before any processing. Ensure all code paths verify admin/approved status.

---

### SEC-06: WebSocket Endpoints Accept All Origins

**File**: `agents.go:99`, `phone.go:71`

Both the agent and phone bridge WebSocket upgraders accept all origins:

```go
CheckOrigin: func(r *http.Request) bool { return true },
```

**Impact**: Cross-site WebSocket hijacking (CSWSH). If the admin visits a malicious website, JavaScript on that page could open a WebSocket to the Minerva server and interact with the agent hub or phone bridge.
**Fix**: Validate the Origin header against allowed domains (your configured `BASE_URL`).

---

### SEC-07: Webhook Signature Bypass When Secret Not Set

**File**: `webhook.go:296-297, 348`

The email webhook signature verification is skipped entirely when no secret is configured:

```go
if w.secret == "" {
    return true // No secret configured, skip verification
}
// ...
if w.secret != "" && !w.verifySignature(...) {
```

**Impact**: If `RESEND_WEBHOOK_SECRET` is not set, anyone can send forged email webhook payloads, injecting fake emails into the system that get processed by the AI brain.
**Fix**: Require the webhook secret in production. Reject all webhook requests if no secret is configured instead of accepting them.

---

### SEC-08: Agent Working Directory Not Validated (Path Traversal)

**File**: `webhook.go:254`, `agents.go:482`, `agent/executor.go:38`

The `dir` parameter from `/agent/run` is passed through to `cmd.Dir` on the agent without validation:

```go
// webhook.go - user-provided dir passed directly
_, err := w.agentHub.SendTask(req.Agent, req.Prompt, req.Dir)

// agent/executor.go - dir becomes working directory
cmd.Dir = workDir
```

The `cmd/agent/main.go:279` does have a relative path check, but it's only in the newer cmd/agent, not in the original `agent/executor.go`.

**Impact**: An attacker could specify `dir: "/"` or `dir: "/etc"` and Claude would execute with that as the working directory, potentially accessing/modifying sensitive system files.
**Fix**: Validate `dir` against an allowlist of permitted directories, or restrict to paths under the agent's home directory.

---

### SEC-09: No Rate Limiting on Any Endpoint

**Files**: All HTTP handlers in `webhook.go`

There are no rate limits on any endpoint. The AI brain involves expensive Claude CLI calls.

**Impact**: An attacker can:
- Flood `/voice/call` to make hundreds of outbound calls
- Flood `/agent/run` to spawn many Claude processes consuming resources
- DoS the server by overwhelming the AI queue
**Fix**: Add rate limiting middleware (e.g., `golang.org/x/time/rate`), at minimum on endpoints that trigger expensive operations.

---

### SEC-10: JavaScript Code Execution Without Sandboxing

**File**: `tools/code.go:24-107`

The `RunCode` tool executes user-provided JavaScript via `goja`:

```go
vm := goja.New()
val, err := vm.RunString(args.Code)
```

While `goja` doesn't expose Node.js APIs (no `fs`, `net`, etc.), the timeout mechanism has a race condition: the goroutine that runs the code continues even after `vm.Interrupt()` is called and may not actually stop.

**Impact**: Potential DoS via CPU-intensive JavaScript. The 5-second timeout may not be reliably enforced due to the race between interrupt and execution.
**Fix**: Add memory limits. Ensure the interrupt mechanism works reliably. Consider running in a separate process with cgroup limits for multi-tenant.

---

## Medium

### SEC-11: SQL LIKE Pattern Not Escaped in Notes Search

**File**: `tools/notes.go:72`

```go
searchPattern := "%" + strings.ToLower(args.Query) + "%"
rows, err := db.Query(`...WHERE ... LIKE ? ...`, searchPattern)
```

While parameterized queries prevent SQL injection, the `%` and `_` characters in the search query are not escaped. A user searching for `%` would match all notes.

**Impact**: Low - information disclosure limited to the same user's notes. But in multi-tenant, one tenant could craft queries that return unexpected results.
**Fix**: Escape `%`, `_`, and `\` in the search pattern with `ESCAPE` clause.

---

### SEC-12: Sensitive Data Logged in Plain Text

**Files**: Multiple

Several log statements include sensitive information:
- `voice.go:316` - Google API key in WebSocket URL (logged on connection)
- `server.go:90` - Agent password presence logged
- `twilio.go:66` - Phone numbers logged for incoming calls
- `bot.go:37` - Bot username logged (minor)

**Impact**: If logs are accessed by unauthorized parties, credentials and PII are exposed.
**Fix**: Redact API keys, tokens, and phone numbers in log output. Use structured logging with field-level redaction.

---

### SEC-13: Email Webhook Processes Unvalidated Email Content

**File**: `webhook.go:370-440`

Incoming email content (from, subject, body) is passed directly to the AI brain as a system event. The email could contain prompt injection attacks:

```go
eventMsg := fmt.Sprintf("[NEW EMAIL RECEIVED]\nFrom: %s\nTo: %s\nSubject: %s\n\n%s",
    payload.Data.From, toAddrs, payload.Data.Subject, payload.Data.Text)
```

**Impact**: An attacker could send a crafted email that, when processed by the AI brain, causes it to execute unintended tool calls (send messages, make calls, create reminders, etc.).
**Fix**: Sanitize email content before passing to the AI. Consider marking external content with clear delimiters that the AI is instructed to treat as untrusted.

---

### SEC-14: Twilio TwiML Injection

**File**: `twilio.go:88-97`

The incoming call handler interpolates the `from` phone number into TwiML XML:

```go
twiml := fmt.Sprintf(`...
    <Parameter name="from" value="%s" />
...`, wsURL, callSID, url.QueryEscape(from))
```

While `url.QueryEscape` is used for `from`, the `callSID` is not escaped and comes from Twilio's POST form data. If an attacker can spoof the Twilio webhook (see SEC-02), they could inject arbitrary XML.

**Impact**: TwiML injection could redirect calls, play arbitrary audio, or connect to malicious WebSocket endpoints.
**Fix**: Fix the Twilio signature validation (SEC-02). Use proper XML escaping for all interpolated values (`html.EscapeString`).

---

### SEC-15: Lock File Race Condition

**File**: `main.go:38`

The lock file uses `0644` permissions, allowing any user on the system to read the PID:

```go
f, err := os.OpenFile(getLockFile(), os.O_CREATE|os.O_RDWR, 0644)
```

**Impact**: Minor - PID disclosure. An attacker with local access could read the PID.
**Fix**: Use `0600` permissions for the lock file.

---

### SEC-16: HMAC Comparison Vulnerable to Timing Attack

**File**: `webhook.go:320`

The Svix webhook signature comparison uses `hmac.Equal` on base64-encoded strings rather than decoded bytes:

```go
if hmac.Equal([]byte(sig), []byte(expectedSig)) {
```

While `hmac.Equal` is constant-time, comparing base64 strings instead of raw bytes adds an unnecessary layer that could theoretically leak information about signature length.

**Impact**: Very low practical risk, but not following best practices.
**Fix**: Compare decoded signature bytes directly.

---

### SEC-17: Android Deploy Script Writes .env to Temp Directory

**File**: `android-app/scripts/deploy-to-phone.sh:147-149`

The deploy script writes credentials to a temp file before pushing to the phone:

```bash
echo "$ENV_CONTENT" > "$BUILD_DIR/.env"
adb push "$BUILD_DIR/.env" "$TERMUX_TMP/.env"
```

**Impact**: Credentials temporarily exist as plaintext in `/tmp` on the build machine, potentially readable by other users.
**Fix**: Use a temp file with restricted permissions (`mktemp` + `chmod 600`). Clean up after push.

---

## Low

### SEC-18: No TLS on Internal HTTP Server

**File**: `webhook.go:103`

The server listens on plain HTTP. TLS is terminated at Caddy.

```go
return http.ListenAndServe(addr, nil)
```

**Impact**: Traffic between Caddy and the Go server is unencrypted on localhost. Low risk since it's on the same machine, but a compromised process could sniff traffic.
**Fix**: Acceptable for localhost-only. Document that the server MUST be behind a reverse proxy and should never be exposed directly.

---

### SEC-19: Default WebhookPort Could Conflict

**File**: `config.go` - default port 8080 vs actual port 8081

The default port in code is 8080 but the actual deployment uses 8081 (via env var). This mismatch could cause confusion during initial setup.

**Impact**: Misconfiguration risk. An unintended service might bind to 8080.
**Fix**: Document the port requirement clearly. Consider failing startup if the port is already in use.

---

### SEC-20: No Request Size Limits

**Files**: `webhook.go`, HTTP handlers

No `http.MaxBytesReader` or equivalent is used on any endpoint. Large request bodies are read entirely into memory.

**Impact**: DoS via large payloads. An attacker could send multi-GB POST bodies.
**Fix**: Add `http.MaxBytesReader` to all handlers that read request bodies. A reasonable limit would be 10MB for email webhooks, 1MB for other endpoints.

---

### SEC-21: Agent Connection Has No Heartbeat Timeout

**File**: `agents.go`

Connected agents don't have a heartbeat/keepalive mechanism to detect stale connections.

**Impact**: Phantom agents in the agent list. Tasks sent to stale agents would hang until timeout.
**Fix**: Implement WebSocket ping/pong with a configurable timeout (e.g., 30s).

---

### SEC-22: SQLite Not Using WAL Mode or Connection Limits

**File**: `db.go:77`

The SQLite database is opened without enabling WAL mode or setting connection pool limits:

```go
sqlDB, err := sql.Open("sqlite", path)
```

**Impact**: Under concurrent load (multiple goroutines), SQLite could deadlock or return "database is locked" errors.
**Fix**: Enable WAL mode (`PRAGMA journal_mode=WAL`) and set `SetMaxOpenConns(1)` for write serialization.

---

## Multi-Tenant Assessment

### Current State

Minerva is designed as a **single-user personal assistant**. Multi-tenancy support would require significant changes:

### What Exists
- `users` table with `id`, `approved` status
- Most DB queries filter by `user_id`
- Admin ID check in Telegram bot handlers

### What's Missing for Multi-Tenant

| Component | Gap | Effort |
|-----------|-----|--------|
| **AI Brain** | Single Claude CLI session shared via `--continue`. All users would share the same conversation state. | High - Need per-tenant CLI sessions or API-based isolation |
| **Workspace** | Single `./workspace` directory for Claude. No per-user isolation. | Medium - Create per-user workspace dirs |
| **Agent Hub** | Agents are global - any authenticated user could run tasks on any agent. No tenant scoping. | High - Agents need tenant assignment |
| **Reminders** | `GetPendingReminders()` in `db.go:398` fetches ALL pending reminders, not per-user. The reminder checker fires all due reminders through a single brain. | Medium - Add user_id filter, per-user brain routing |
| **Voice Calls** | All calls go to `AdminID`. No per-user call routing. | Medium |
| **Email** | Emails processed via single admin context. | Medium |
| **Phone Bridge** | Global - any user could trigger calls via any connected phone. | Medium |
| **Task Runner** | Tasks run in shared directory space. | Medium - Per-user task directories |
| **HTTP Endpoints** | No user identification on HTTP requests (only Telegram has user context). | High - Add auth to all HTTP endpoints |
| **Memory** | `memory` table has `user_id` column - already tenant-aware. | Done |
| **Notes** | `notes` table has `user_id` column - already tenant-aware. | Done |
| **Conversations** | `conversations` + `messages` tables have `user_id` - already tenant-aware. | Done |

### Recommended Multi-Tenant Implementation Order

1. **Add HTTP authentication** (SEC-03) - Required regardless
2. **Per-user AI sessions** - Most critical; eliminates cross-tenant conversation leakage
3. **Scope agent hub** - Add tenant_id to agents, restrict task execution
4. **Fix reminder checker** - Filter by user_id in pending query
5. **Per-user workspace dirs** - Prevent file system cross-contamination
6. **Scope voice/phone** - Route calls per tenant
7. **Add tenant_id to all HTTP endpoints** - JWT or API key based identification

---

## Recommendations Priority Matrix

| Priority | Action | Issues |
|----------|--------|--------|
| **Immediate** | Rotate relay key, fix Twilio HMAC, add HTTP auth | SEC-01, SEC-02, SEC-03 |
| **Short-term** | Add rate limiting, validate agent dir, fix WebSocket origins | SEC-06, SEC-08, SEC-09 |
| **Medium-term** | Sanitize email content, add request size limits, fix logging | SEC-12, SEC-13, SEC-20 |
| **Long-term** | Multi-tenant architecture, sandboxed execution | Full multi-tenant assessment |
