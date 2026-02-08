# Minerva - Personal AI Assistant

## Architecture

Go monolith deployed to VPS (51.254.142.231) via `scp` + `systemctl restart minerva`.
Reverse proxy: Caddy at `/etc/caddy/Caddyfile`, domain: `home.jairo.cloud` -> `localhost:8081`.

- **AI Backend**: OpenRouter API (multi-model with fallback)
- **Database**: SQLite at configured `DATABASE_PATH`
- **Server Port**: 8081 (env: `WEBHOOK_PORT`)
- **Telegram Bot**: Long-polling, single admin user

## Build & Deploy

```bash
# Build for VPS
GOOS=linux GOARCH=amd64 go build -o /tmp/minerva-server .

# Deploy
scp /tmp/minerva-server ubuntu@51.254.142.231:/tmp/minerva-server
ssh ubuntu@51.254.142.231 'sudo cp /tmp/minerva-server /home/ubuntu/minerva && sudo systemctl restart minerva'
```

## Environment Variables (.env)

| Variable | Required | Description |
|----------|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Yes | Telegram bot token from @BotFather |
| `ADMIN_ID` | Yes | Telegram user ID (int64) |
| `OPENROUTER_API_KEY` | Yes | OpenRouter API key |
| `DATABASE_PATH` | No | SQLite path (default: `./minerva.db`) |
| `MODELS` | No | Comma-separated model priority list |
| `MAX_CONTEXT_MESSAGES` | No | Context window size (default: 20) |
| `RESEND_API_KEY` | No | Resend API key for sending emails |
| `RESEND_WEBHOOK_SECRET` | No | Svix webhook signing secret for receiving emails |
| `WEBHOOK_PORT` | No | HTTP server port (default: 8080) |
| `TWILIO_ACCOUNT_SID` | No | Twilio SID for voice calls |
| `TWILIO_AUTH_TOKEN` | No | Twilio auth token |
| `TWILIO_PHONE_NUMBER` | No | Twilio caller ID |
| `GOOGLE_API_KEY` | No | Google API key for Gemini Live voice |
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

## AI Tools (available to the bot)

| Tool | Description |
|------|-------------|
| `create_reminder` | Create time-based reminders (target: user or ai) |
| `list_reminders` | List pending reminders |
| `delete_reminder` | Dismiss a reminder (only on user request) |
| `reschedule_reminder` | Reschedule a fired reminder |
| `update_memory` | Store persistent user info (2000 char limit) |
| `run_code` | Execute JavaScript in sandboxed goja VM (5s timeout) |
| `send_email` | Send email via Resend API |
| `make_call` | Initiate outbound phone call (Twilio + Gemini Live) |
| `create_task` | Launch background Claude Code task |
| `get_task_progress` | Check background task status |
| `run_claude` | Run task on remote Claude Code agent |
| `list_claude_projects` | List projects on connected agents |

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
| `ai.go` | OpenRouter API client (multi-model) |
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
