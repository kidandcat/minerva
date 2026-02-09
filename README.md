# Minerva

Personal AI assistant powered by Claude, accessible via Telegram. Minerva can manage reminders, store memory, send emails, make phone calls with real-time voice AI, and delegate coding tasks to remote Claude Code agents.

## Architecture

```
┌──────────────┐     ┌──────────────────────────┐     ┌─────────────┐
│   Telegram   │◄───►│    Minerva (Go binary)   │◄───►│  Claude CLI  │
│   Bot API    │     │                          │     │  (AI brain)  │
└──────────────┘     │  ┌────────────────────┐  │     └─────────────┘
                     │  │   Webhook Server   │  │
┌──────────────┐     │  │  /webhook/email    │  │     ┌─────────────┐
│   Resend     │────►│  │  /agent (WS)       │  │◄───►│  Remote     │
│   (email)    │     │  │  /voice/ws         │  │     │  Agents     │
└──────────────┘     │  └────────────────────┐  │     └─────────────┘
                     │                          │
┌──────────────┐     │  ┌────────────────────┐  │     ┌─────────────┐
│ Twilio/Phone │◄───►│  │     SQLite DB      │  │◄───►│ Gemini Live │
│  (voice)     │     │  └────────────────────┘  │     │ (voice AI)  │
└──────────────┘     └──────────────────────────┘     └─────────────┘
```

Minerva is a single Go binary that acts as a personal AI hub:

- **Brain** (Claude CLI) handles conversation, reminders, memory, and decision-making
- **Agents** are remote Claude Code instances that handle all code-related tasks
- **Voice** uses Gemini Live for real-time phone conversations via Twilio or Android bridge
- **Storage** is a single SQLite database with no external dependencies

## Features

- **AI Chat** — Talk to Claude via Telegram with persistent conversation history
- **Reminders** — Schedule reminders with natural language; AI decides when to reschedule
- **Memory** — Persistent memory across conversations
- **Remote Agents** — Delegate coding tasks to Claude Code instances on any machine
- **Email** — Send and receive emails via [Resend](https://resend.com)
- **Voice Calls** — Make and receive phone calls with real-time AI voice (Gemini Live)
- **Android Phone Bridge** — Use a real phone number via an Android device
- **Background Tasks** — Long-running tasks with progress tracking
- **Code Execution** — Run JavaScript snippets in a sandbox (Goja)
- **Encrypted Relay** — Optional relay for agents behind NAT/firewalls

## Requirements

- **Go 1.24+**
- **[Claude CLI](https://docs.anthropic.com/en/docs/claude-code)** — installed and authenticated (`claude` must be in PATH)
- **Telegram Bot Token** — from [@BotFather](https://t.me/BotFather)

Optional (for additional features):
- [Resend](https://resend.com) account — for email
- [Twilio](https://www.twilio.com) account — for phone calls
- [Google AI API key](https://aistudio.google.com/apikey) — for Gemini Live voice

## Quick Start

```bash
# Clone
git clone https://github.com/kidandcat/minerva.git
cd minerva

# Configure
cp .env.example .env
# Edit .env — set TELEGRAM_BOT_TOKEN and ADMIN_ID at minimum

# Build and run
go build -o minerva .
./minerva
```

That's it. Minerva will start polling Telegram for messages.

### With Docker

```bash
cp .env.example .env
# Edit .env with your values
docker compose up -d
```

## Configuration

All configuration is via environment variables (or `.env` file). See [`.env.example`](.env.example) for the full list.

### Required

| Variable | Description |
|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token from [@BotFather](https://t.me/BotFather) |
| `ADMIN_ID` | Your Telegram user ID (get it from [@userinfobot](https://t.me/userinfobot)) |

### Recommended

| Variable | Default | Description |
|----------|---------|-------------|
| `BASE_URL` | — | Public URL for webhooks (required for voice calls) |
| `OWNER_NAME` | `the owner` | Your name (used in voice call prompts) |
| `DEFAULT_COUNTRY_CODE` | `+1` | Default country code for phone numbers |
| `DATABASE_PATH` | `./minerva.db` | SQLite database path |
| `WEBHOOK_PORT` | `8080` | HTTP server port |

### Optional Features

| Variable | Description |
|----------|-------------|
| `RESEND_API_KEY` | Enable email sending via Resend |
| `FROM_EMAIL` | Sender address (e.g., `Minerva <minerva@yourdomain.com>`) |
| `TWILIO_ACCOUNT_SID` | Enable Twilio voice calls |
| `TWILIO_AUTH_TOKEN` | Twilio auth token |
| `TWILIO_PHONE_NUMBER` | Twilio phone number |
| `GOOGLE_API_KEY` | Enable Gemini Live real-time voice AI |
| `AGENT_PASSWORD` | Password for agent WebSocket auth |

## CLI Commands

Minerva includes a CLI for direct interaction. This is also how the AI brain interacts with the system.

```bash
# Reminders
minerva reminder create "Buy groceries" --at "2025-02-06T10:00:00Z"
minerva reminder list
minerva reminder delete 1

# Memory
minerva memory get
minerva memory set "Prefers dark mode"

# Communication
minerva send "Hello from CLI"

# Agents
minerva agent list
minerva agent run mac "git status" --dir /path/to/project

# Voice calls (requires Twilio + Gemini)
minerva call +14155551234 "Make a dinner reservation for 2 at 8pm"

# Email (requires Resend)
minerva email send user@example.com --subject "Hello" --body "Hi there"
```

## Remote Agents

Agents are Claude Code instances running on any machine that connect to Minerva via WebSocket. They handle all code-related tasks (git, builds, debugging, etc.).

### Running an Agent

```bash
cd agent
go build -o minerva-agent .

# Connect to Minerva server
./minerva-agent --name my-laptop --server ws://your-server:8080/agent
```

### Agent with Password

```bash
# Build with embedded password
go build -ldflags "-X main.Password=your-secret" -o minerva-agent ./agent

# Server must have matching AGENT_PASSWORD in .env
```

### Install as Service

**macOS (launchd):**

Set `MINERVA_SERVER` and `AGENT_NAME` in the environment, then use `agent/run-agent.sh`:

```bash
# Edit run-agent.sh or set env vars
export MINERVA_SERVER=wss://your-server/agent
export AGENT_NAME=my-mac

# Create ~/Library/LaunchAgents/com.minerva.agent.plist pointing to run-agent.sh
launchctl load ~/Library/LaunchAgents/com.minerva.agent.plist
```

**Linux (systemd):**

```ini
[Unit]
Description=Minerva Agent
After=network.target

[Service]
Type=simple
ExecStart=/path/to/minerva-agent --name my-agent --server ws://server:8080/agent
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Telegram Commands

| Command | Description |
|---------|-------------|
| `/start` | Welcome message |
| `/new` | Start new conversation |
| `/history` | List past conversations |
| `/system <prompt>` | Set custom AI behavior |
| `/memory` | View stored memory |
| `/reminders` | List pending reminders |
| `/tasks` | View background tasks |
| `/status <id>` | Check task progress |
| `/cancel <id>` | Cancel running task |

## Deployment

### VPS Deployment

```bash
# Build for Linux
GOOS=linux GOARCH=amd64 go build -o minerva-server .

# Copy to server
scp minerva-server user@server:/tmp/minerva-server
ssh user@server 'sudo systemctl stop minerva && sudo cp /tmp/minerva-server /usr/local/bin/minerva && sudo systemctl start minerva'
```

### Reverse Proxy (Caddy)

```
your-domain.com {
    reverse_proxy localhost:8080
}
```

## Project Structure

```
minerva/
├── main.go          # Entry point, CLI commands
├── config.go        # Configuration from environment
├── server.go        # Server lifecycle management
├── bot.go           # Telegram bot handlers
├── ai.go            # Claude CLI integration
├── db.go            # SQLite database layer
├── tools.go         # Tool executor (reminders, memory, email, etc.)
├── agents.go        # Agent hub (WebSocket server)
├── webhook.go       # HTTP server (webhooks, API endpoints)
├── voice.go         # Gemini Live voice (Twilio Media Streams)
├── twilio.go        # Twilio ConversationRelay
├── phone.go         # Android phone bridge
├── task_runner.go   # Background task management
├── relay_client.go  # Encrypted relay client
├── audio.go         # Audio format conversion (mu-law, PCM, resampling)
├── workspace/
│   └── CLAUDE.md    # System prompt for the AI brain
├── agent/
│   ├── main.go      # Agent binary entry point
│   ├── client.go    # WebSocket client
│   └── executor.go  # Claude CLI execution
├── tools/
│   ├── reminder.go  # Reminder CRUD
│   ├── memory.go    # Memory storage
│   ├── email.go     # Resend email integration
│   ├── notes.go     # Note management
│   └── code.go      # JavaScript sandbox (Goja)
├── mcp/
│   └── main.go      # MCP server (JSON-RPC)
├── android-app/     # Android phone bridge app
├── Dockerfile
├── docker-compose.yml
└── .env.example
```

## How It Works

1. **Message arrives** via Telegram
2. **Minerva queues** it for Claude CLI processing (serial, to maintain `--continue` session state)
3. **Claude responds**, optionally calling tools (reminders, agents, email, etc.)
4. **Tool results** are fed back to Claude for final response
5. **Response sent** to user via Telegram

Agent tasks are **asynchronous** — Minerva dispatches work to agents and continues chatting. When an agent completes a task, the result is fed back through Minerva's AI brain for summarization.

Voice calls use **Gemini Live** for real-time audio — Twilio handles telephony while Gemini provides natural conversation. Call summaries are automatically generated and sent via Telegram.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT — see [LICENSE](LICENSE)
