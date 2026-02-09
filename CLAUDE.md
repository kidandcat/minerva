# Minerva - Personal AI Assistant

## Architecture

Go monolith deployed to VPS (51.254.142.231) via `scp` + `systemctl restart minerva`.
Reverse proxy: Caddy at `/etc/caddy/Caddyfile`, domain: `home.jairo.cloud` -> `localhost:8081`.

- **AI Backend**: Claude CLI (`claude -p --continue --dangerously-skip-permissions`)
- **Database**: SQLite at configured `DATABASE_PATH`
- **Server Port**: 8081 (env: `WEBHOOK_PORT`)
- **Telegram Bot**: Long-polling, single admin user

## Build & Deploy

```bash
# Build server for VPS (MUST run from repo root /Users/jairo/minerva, NOT from agent/ subdir)
cd /Users/jairo/minerva && GOOS=linux GOARCH=amd64 go build -o /tmp/minerva-server .

# Deploy server
scp /tmp/minerva-server ubuntu@51.254.142.231:/tmp/minerva-server
ssh ubuntu@51.254.142.231 'sudo systemctl stop minerva && sudo cp /tmp/minerva-server /home/ubuntu/minerva && sudo systemctl start minerva'

# Build Mac agent (separate binary, different package)
cd /Users/jairo/minerva/agent && go build -o /Users/jairo/minerva/agent/minerva-agent .

# Restart Mac agent (managed by launchd, do NOT launch manually with nohup)
launchctl unload ~/Library/LaunchAgents/com.minerva.agent.plist
launchctl load ~/Library/LaunchAgents/com.minerva.agent.plist
# Logs: /tmp/minerva-agent.log
# Config: ~/Library/LaunchAgents/com.minerva.agent.plist
# Script: /Users/jairo/minerva/agent/run-agent.sh
```

**CRITICAL: Building from wrong directory deploys the wrong binary and breaks the bot.**
This has happened multiple times. The `cd` before `go build` is NOT optional.
- Server: `cd /Users/jairo/minerva && go build .` (root) → deploys to VPS
- Agent: `cd /Users/jairo/minerva/agent && go build .` → runs locally via launchd
- VPS agent: same source as Mac agent, build with `cd /Users/jairo/minerva/agent && GOOS=linux GOARCH=amd64 go build -o /tmp/minerva-agent .`
- **Always verify** after deploy: `sudo journalctl -u minerva -n 5` must show `server.go` and `webhook.go` lines, NOT `client.go`
- If you see `client.go` in server logs → you deployed the agent binary as the server. Rebuild from root.

## Environment Variables (.env)

| Variable | Required | Description |
|----------|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Yes | Telegram bot token from @BotFather |
| `ADMIN_ID` | Yes | Telegram user ID (int64) |
| `DATABASE_PATH` | No | SQLite path (default: `./minerva.db`) |
| `MINERVA_WORKSPACE` | No | Workspace dir for Claude CLI (default: `./workspace`) |
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

## AI Brain (Claude CLI)

The AI brain runs as `claude -p --continue --dangerously-skip-permissions --output-format text` in `./workspace`.
Claude CLI reads `workspace/CLAUDE.md` automatically, which contains all tool instructions.

**CRITICAL**: The `--continue` flag is **mandatory**. Without it, each invocation is a fresh
session with no memory of previous tool calls, causing Claude to fabricate responses instead
of actually executing commands. With `--continue`, Claude maintains conversation state and
can see the real outputs of its previous Bash tool executions.

**Important**: Claude CLI cannot use custom tool definitions (ToolCalls). Instead, all Minerva
tools are exposed as CLI commands that Claude executes via its built-in Bash tool.
The `workspace/CLAUDE.md` file documents all available CLI commands. When deploying a new
Minerva instance, copy `workspace/CLAUDE.md` to the workspace directory.

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
