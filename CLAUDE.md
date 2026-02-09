# Minerva - Personal AI Assistant

## Architecture

Go monolith. Single binary serves the Telegram bot, HTTP webhooks, and WebSocket endpoints.

- **AI Backend**: Claude CLI (`claude -p --continue --dangerously-skip-permissions`)
- **Database**: SQLite at configured `DATABASE_PATH`
- **Server Port**: configurable via `WEBHOOK_PORT` (default: 8080)
- **Telegram Bot**: Long-polling, single admin user

## Build & Deploy

```bash
# Build server (MUST run from repo root, NOT from agent/ subdir)
go build -o minerva .

# Build for Linux (cross-compile)
GOOS=linux GOARCH=amd64 go build -o minerva-server .

# Build agent (separate binary, different package)
cd agent && go build -o minerva-agent .
```

**CRITICAL: Building from wrong directory deploys the wrong binary.**
- Server: `go build .` from repo root
- Agent: `cd agent && go build .`
- **Always verify** after deploy: logs must show `server.go` and `webhook.go` lines, NOT `client.go`
- If you see `client.go` in server logs → you deployed the agent binary as the server

## Environment Variables (.env)

See `.env.example` for the full list. Key variables:

| Variable | Required | Description |
|----------|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Yes | Telegram bot token from @BotFather |
| `ADMIN_ID` | Yes | Telegram user ID (int64) |
| `BASE_URL` | No | Public URL for webhooks (required for voice calls) |
| `OWNER_NAME` | No | Owner's name (used in voice prompts) |
| `DEFAULT_COUNTRY_CODE` | No | Default country code (default: +1) |
| `DATABASE_PATH` | No | SQLite path (default: `./minerva.db`) |
| `WEBHOOK_PORT` | No | HTTP server port (default: 8080) |
| `FROM_EMAIL` | No | Email sender address |
| `AGENT_PASSWORD` | No | Password for agent WebSocket auth |

## CLI Commands

```bash
minerva                                                    # Run Telegram bot (default)
minerva reminder create "text" --at "2024-02-06T10:00:00Z" # Create reminder
minerva reminder list                                      # List pending reminders
minerva reminder delete <id>                               # Dismiss reminder
minerva reminder reschedule <id> --at "time"               # Reschedule reminder
minerva memory get [key]                                   # Get user memory
minerva memory set "content"                               # Set user memory
minerva send "message"                                     # Send Telegram message to admin
minerva context                                            # Get recent conversation context
minerva email send <to> --subject "subj" --body "body"     # Send email via Resend
minerva call <number> "purpose"                            # Make phone call (Twilio)
minerva phone list                                         # List connected Android phones
minerva phone call <number> "purpose"                      # Call via Android phone bridge
minerva agent list                                         # List connected agents
minerva agent run <name> "prompt" [--dir /path]            # Run task on agent
```

## AI Brain (Claude CLI)

The AI brain runs as `claude -p --continue --dangerously-skip-permissions --output-format text` in `./workspace`.
Claude CLI reads `workspace/CLAUDE.md` automatically, which contains all tool instructions.

**CRITICAL**: The `--continue` flag is **mandatory**. Without it, each invocation is a fresh
session with no memory of previous tool calls, causing Claude to fabricate responses instead
of actually executing commands.

**Important**: Claude CLI cannot use custom tool definitions (ToolCalls). Instead, all Minerva
tools are exposed as CLI commands that Claude executes via its built-in Bash tool.

## Webhook Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/health` | GET | Health check |
| `/voice/incoming` | POST | Twilio incoming call webhook |
| `/voice/ws` | GET | Twilio Media Streams WebSocket |
| `/voice/call` | POST | Initiate outbound call `{"to":"...", "purpose":"..."}` |
| `/phone/ws` | GET | Android phone bridge WebSocket |
| `/phone/list` | GET | List connected Android devices |
| `/phone/call` | POST | Call via Android bridge `{"to":"...", "purpose":"..."}` |
| `/agent` | GET | Agent WebSocket endpoint |
| `/agent/list` | GET | List connected agents |
| `/agent/run` | POST | Run task on agent `{"agent":"...", "prompt":"...", "dir":"..."}` |
| `/email/webhook` | POST | Resend inbound email webhook (Svix-signed) |

## Reminders System

Status flow: `pending` -> `fired` -> `done`
- All fired reminders go through AI brain for autonomous processing
- Brain decides whether to reschedule (recurring tasks, follow-ups)
- Only the user can dismiss reminders via `/reminders` or `delete_reminder` tool

## Voice Calls (Gemini Live)

- Model: `models/gemini-2.5-flash-native-audio-latest`
- API: `v1beta` WebSocket (BidiGenerateContent)
- Audio pipeline: Twilio mu-law 8kHz <-> Gemini PCM 16kHz input / 24kHz output
- 5-minute call timeout
- Auto-generates call summary -> Telegram notification + system event

## Key Files

| File | Purpose |
|------|---------|
| `main.go` | CLI routing + bot startup |
| `config.go` | Environment config loading |
| `server.go` | Server lifecycle management |
| `bot.go` | Telegram bot logic + commands |
| `ai.go` | Claude CLI client (`claude -p`) |
| `db.go` | SQLite database layer |
| `tools.go` | Tool definitions + executor |
| `voice.go` | Gemini Live voice (Twilio integration) |
| `phone.go` | Android phone bridge |
| `twilio.go` | Twilio ConversationRelay |
| `webhook.go` | HTTP webhook handlers |
| `agents.go` | Agent hub (remote Claude Code) |
| `relay_client.go` | Encrypted relay client |
| `task_runner.go` | Background task management |
| `tools/` | Tool implementations (email, reminder, memory, code, notes) |
| `android-app/` | Standalone Android deployment (separate module) |
