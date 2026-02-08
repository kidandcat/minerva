# Minerva Android - One-Click Deploy Plan

## Vision

Single-command install on a rooted Android phone (dedicated server, always plugged in)
that turns it into a self-sufficient Minerva instance:
- Auto-restarts on crash or reboot
- Connects to public relay for agents, webhooks, external access
- Uses **Claude Code CLI** exclusively (target: users with Claude Max plan)
- Makes and receives phone calls natively (no Twilio needed)

**This is a standalone variant.** Everything lives inside `android-app/`.
The VPS codebase and deployment remain untouched.

## Architecture

```
┌──────────────── Rooted Android Phone (Server) ────────┐
│                                                        │
│  ┌─────────────────── Termux + SSHD ────────────────┐ │
│  │                                                   │ │
│  │  ┌────────────┐  ┌───────────┐  ┌─────────────┐ │ │
│  │  │  Minerva   │  │ Claude    │  │   Node.js   │ │ │
│  │  │  (Go ARM)  │──│ Code CLI  │──│   Runtime   │ │ │
│  │  │            │  │           │  │             │ │ │
│  │  │ - Telegram │  └───────────┘  └─────────────┘ │ │
│  │  │ - AI (cli) │                                  │ │
│  │  │ - Tools    │──→ Relay Client (encrypted WS)   │ │
│  │  │ - Agents   │         │                        │ │
│  │  │ - Webhooks │         │                        │ │
│  │  │ - Phone WS │◄──┐    │                        │ │
│  │  └────────────┘   │    │                        │ │
│  │                    │    │                        │ │
│  │  SSHD (port 8022) │    │   ← Remote management  │ │
│  └────────────────────│────│────────────────────────┘ │
│                       │    │                          │
│  ┌────────────────┐  │    │                          │
│  │ Phone Bridge   │──┘    │     ┌──────────────────┐ │
│  │ (APK)          │       │     │  Magisk Module   │ │
│  │ - InCallService│       │     │  - Audio perms   │ │
│  │ - AudioBridge  │       │     │  - Priv-app      │ │
│  │ - localhost WS │       │     └──────────────────┘ │
│  └────────────────┘       │                          │
│                            │                          │
└────────────────────────────│──────────────────────────┘
                             │
                             │ Encrypted WebSocket
                             ▼
                   ┌─────────────────────┐
                   │   Relay Server      │
                   │   (Fly.io)          │
                   │                     │
                   │ - Public HTTPS URL  │──→ Resend webhooks
                   │ - Agent tunneling   │──→ Mac/VPS agents
                   │ - HTTP forwarding   │──→ Any webhook
                   └─────────────────────┘
```

## Project Structure

Everything self-contained under `android-app/`:

```
android-app/
├── PLAN.md                  # This file
├── server/                  # Go module - Minerva for Android
│   ├── go.mod
│   ├── main.go              # Entry point + CLI commands
│   ├── config.go            # Config from .env
│   ├── bot.go               # Telegram bot
│   ├── ai.go                # Claude Code CLI execution
│   ├── db.go                # SQLite
│   ├── tools.go             # Tool definitions + executor
│   ├── tools/               # Tool implementations
│   │   ├── reminder.go
│   │   ├── memory.go
│   │   └── ...
│   ├── phone.go             # Phone bridge (localhost WS)
│   ├── voice.go             # Gemini Live integration
│   ├── agents.go            # Agent hub
│   ├── relay_client.go      # Relay client
│   ├── webhook.go           # HTTP webhook server
│   └── audio.go             # Audio format conversion
├── bridge/                  # Phone Bridge APK (call interception)
│   ├── app/
│   │   ├── src/main/
│   │   │   ├── java/.../
│   │   │   │   ├── CallHandler.kt     # InCallService
│   │   │   │   ├── AudioBridge.kt     # Audio capture/playback
│   │   │   │   ├── MinervaClient.kt   # WebSocket to localhost
│   │   │   │   └── MainActivity.kt    # Minimal status UI
│   │   │   └── AndroidManifest.xml
│   │   └── build.gradle.kts
│   └── settings.gradle.kts
├── magisk/                  # Magisk module for permissions
│   ├── module.prop
│   ├── customize.sh
│   └── META-INF/...
├── install.sh               # One-click install (runs from Mac)
├── termux-setup.sh          # Runs inside Termux via SSH
└── scripts/
    ├── boot-minerva.sh      # Termux:Boot auto-start
    └── minerva-watchdog.sh  # Crash recovery wrapper
```

## Component Details

### 1. Minerva Go Binary (server/)

Self-contained Go module. Copies relevant code from main codebase, adapted for Android:

**AI: Claude Code CLI only.**
```go
func executeClaude(prompt string, workDir string, timeout time.Duration) (string, error) {
    cmd := exec.CommandContext(ctx, "claude",
        "-p",                              // print mode
        "--dangerously-skip-permissions",   // non-interactive
        "--output-format", "text",
        prompt)
    cmd.Dir = workDir
    // CLAUDE_CODE_OAUTH_TOKEN from env
}
```

No OpenRouter, no configurable backend. Simple.

**Telegram commands:**
- Standard: messages go to Claude Code
- `/token <new_oauth_token>` - Update Claude Code OAuth token, save to .env, restart
- `/status` - Show phone status (uptime, battery, network, active calls)
- `/agents` - List connected agents
- `/call <number> "purpose"` - Make outbound call via phone

**Token renewal flow:**
1. Claude Code returns auth error → Minerva notifies admin on Telegram
2. Admin generates new token on laptop: `claude login` → copy token
3. Admin sends `/token sk-ant-oat01-...` on Telegram
4. Minerva updates .env, restarts Claude Code with new token
5. Confirms success on Telegram

### 2. Claude Code in Termux

```bash
pkg install nodejs-lts    # Node.js LTS (includes npm)
npm install -g @anthropic-ai/claude-code
```

Auth via `CLAUDE_CODE_OAUTH_TOKEN` env var in `.env` file.

### 3. Phone Bridge APK (bridge/)

Lightweight APK that handles native phone calls:

- **CallHandler** (InCallService): Receives call events from Android Telecom
- **AudioBridge**: Captures call audio via `CAPTURE_AUDIO_OUTPUT` (Magisk permission)
- **MinervaClient**: WebSocket client to `ws://localhost:8081/phone`
- **Auto-start**: BroadcastReceiver for BOOT_COMPLETED
- **Auto-connect**: Always connected to localhost Minerva

**Call flow (incoming):**
```
Incoming call → Android Telecom → CallHandler
→ CallHandler notifies MinervaClient: {"type":"call_start","from":"+34..."}
→ CallHandler answers call
→ AudioBridge starts capturing (16kHz PCM mono)
→ Audio chunks → MinervaClient → ws://localhost:8081/phone: {"type":"audio","data":"base64"}
→ Minerva connects to Gemini Live, streams audio
→ Gemini responds with audio
→ Minerva sends back: {"type":"audio","data":"base64"}
→ AudioBridge plays response into call speaker
→ Caller hears AI response
→ Call ends → Minerva generates summary → Telegram notification
```

**Call flow (outbound via Telegram):**
```
Admin sends /call +34600123456 "Ask about the appointment"
→ Minerva sends to bridge: {"type":"command","command":"make_call","to":"+34..."}
→ Bridge initiates call via Android Telecom
→ Same audio flow as incoming
→ Gemini uses custom system prompt with purpose
```

### 4. SSH Access

Termux SSHD on port 8022 for remote management:

```bash
# From Mac:
ssh -p 8022 phone    # (after adding to ~/.ssh/config)
```

Allows:
- Debugging and log inspection
- Manual Claude Code testing
- Config updates
- Binary updates without re-running full install

### 5. Relay Server (needs webhook extension)

Existing relay at `/Users/jairo/minerva-relay/` deployed on Fly.io (`minerva-relay`).

**Currently supports:**
- Brain WebSocket (`/brain/ws?key=SECRET`) - Minerva connects here
- Agent WebSocket (`/agent/ws?key=SECRET`) - Agents connect here
- HTTP proxy (`/` with `X-Minerva-Key` header) - Authenticated HTTP forwarding
- AES-256-GCM encryption on all messages

**Problem:** External services (Resend, etc.) can't set `X-Minerva-Key` header.
They need a simple URL to POST to.

**Solution: `/hook/<path>` endpoint**

When a brain connects with a key, the relay derives a deterministic webhook path:
```
webhookPath = hex(SHA256(key))[:48]  # 48 hex chars, unguessable
```

Public webhook URL becomes:
```
https://minerva-relay.fly.dev/hook/a1b2c3d4e5f6789...
```

This URL is configured in Resend (email webhooks), or any other service that
needs to reach Minerva.

**Relay changes (minerva-relay/main.go):**

```go
// New: webhook path registry (alongside brains)
type BrainRegistry struct {
    brains       map[string]*Brain    // keyHash → Brain
    webhookPaths map[string]*Brain    // webhookPath → Brain
    mu           sync.RWMutex
}

// On brain connect: register webhook path
webhookPath := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))[:48]
registry.webhookPaths[webhookPath] = brain
// Send webhook URL back to brain (optional, brain can compute it too)

// New handler: /hook/<path>
func handleWebhook(w http.ResponseWriter, r *http.Request) {
    path := strings.TrimPrefix(r.URL.Path, "/hook/")

    registry.mu.RLock()
    brain, ok := registry.webhookPaths[path]
    registry.mu.RUnlock()

    if !ok {
        http.Error(w, "Not found", 404)
        return
    }

    // Same logic as handleProxy but without key requirement
    // Forward request to brain, wait for response
}
```

**Brain-side (relay_client.go in Minerva):**
The relay client computes its own webhook path from the key and logs it at startup:
```
log.Printf("Webhook URL: https://minerva-relay.fly.dev/hook/%s", webhookPath)
```
This URL is what you configure in Resend dashboard, etc.

**Changes needed:**
1. `minerva-relay/main.go`: Add `webhookPaths` map, `/hook/` handler, register on brain connect
2. `android-app/server/relay_client.go`: Compute and log webhook URL
3. Deploy updated relay to Fly.io

### 6. Auto-start & Watchdog

**Termux:Boot** runs `~/.termux/boot/start-minerva.sh` on device boot.

**Watchdog script** (`minerva-watchdog.sh`):
```bash
#!/data/data/com.termux/files/usr/bin/bash
while true; do
    echo "[$(date)] Starting Minerva..."
    $HOME/minerva 2>&1 | tee -a $HOME/minerva-data/minerva.log
    EXIT_CODE=$?
    echo "[$(date)] Minerva exited with code $EXIT_CODE, restarting in 5s..."
    sleep 5
done
```

**Root-level protections:**
```bash
# Disable Android phantom process killer
su -c "settings put global settings_enable_monitor_phantom_procs false"

# Disable battery optimization for Termux
su -c "dumpsys deviceidle whitelist +com.termux"

# Termux wake lock (prevents CPU sleep)
termux-wake-lock
```

## Install Script (install.sh)

Runs from Mac with phone connected via USB:

```bash
./install.sh
```

### What it does:

```
Phase 1: Build
  ├── Cross-compile Go binary: GOOS=android GOARCH=arm64
  ├── Build Phone Bridge APK (gradle or pre-built)
  └── Package Magisk module (zip)

Phase 2: Push to Phone (via ADB)
  ├── Install Termux APK (F-Droid version)
  ├── Install Termux:Boot APK
  ├── Install Phone Bridge APK
  ├── Install Magisk module
  ├── Push minerva binary to Termux home
  ├── Push .env config
  ├── Push boot/watchdog scripts
  └── Push SSH public key

Phase 3: Setup Termux (via ADB shell into Termux)
  ├── Install packages: nodejs-lts openssh
  ├── Install Claude Code: npm i -g @anthropic-ai/claude-code
  ├── Configure SSHD (authorized_keys)
  ├── Enable wake lock
  ├── Disable phantom process killer
  ├── Disable battery optimization
  └── Create data directory

Phase 4: Start
  ├── Start SSHD
  ├── Start Minerva watchdog
  ├── Verify Telegram connection (send test message)
  └── Print SSH connection details
```

### Interactive prompts:

The script asks for:
1. OAuth token (or reads from env/file)
2. Telegram admin ID
3. Google API key (for Gemini Live calls)
4. Relay key (optional, for relay connection)

Everything else is auto-detected or has sane defaults.

## Implementation Phases

### Phase 1: Go Server
Create `android-app/server/` Go module with:
- Telegram bot + Claude Code CLI execution
- SQLite database
- Basic tools (reminders, memory)
- Webhook server skeleton
- Token renewal Telegram command

Test: Cross-compile for ARM64, push to Termux via ADB, verify Telegram works.

### Phase 2: Install Script
Create `install.sh` that automates the full setup:
- Build, push, configure, start
- SSH access working

Test: Factory-reset Termux, run install.sh, Minerva responds on Telegram.

### Phase 3: Phone Calls
- Build Phone Bridge APK
- Integrate with Gemini Live in server/voice.go
- Magisk module installation in script

Test: Call the phone → AI answers → summary on Telegram.

### Phase 4: Relay Webhook Extension
- Add `/hook/<path>` endpoint to `minerva-relay/main.go`
- Register webhook paths on brain connect (deterministic from key)
- Deploy updated relay to Fly.io
- Add relay client to android server with webhook URL logging

Test: `curl https://minerva-relay.fly.dev/hook/<path>` → reaches phone Minerva.

### Phase 5: Agents
- Agent hub in android server
- Mac agent connects through relay
- Relay tunnels agent WebSocket to phone

Test: Mac agent connects through relay, executes Claude Code tasks on phone.

### Phase 6: Polish
- Watchdog reliability
- Log rotation
- `/status` command (uptime, battery, agents, calls)
- Token expiration detection + Telegram notification
