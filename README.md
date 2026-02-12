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
│ Telnyx/Phone │◄───►│  │     SQLite DB      │  │◄───►│ Gemini Live │
│  (voice)     │     │  └────────────────────┘  │     │ (voice AI)  │
└──────────────┘     └──────────────────────────┘     └─────────────┘
```

Minerva is a single Go binary that acts as a personal AI hub:

- **Brain** (Claude CLI) handles conversation, reminders, memory, and decision-making
- **Agents** are remote Claude Code instances that handle all code-related tasks
- **Voice** uses Gemini Live for real-time phone conversations via Telnyx or Android bridge
- **Storage** is a single SQLite database with no external dependencies

## Features

### Telegram Bot
- **AI Chat** — Talk to Claude via Telegram with persistent conversation history
- **Photos & Documents** — Send images and files for analysis (auto-downloaded and passed to Claude)
- **Conversation Management** — Multiple conversations with `/clear`, full message history
- **Multi-user Support** — Admin approval system for additional users with inline approve/reject buttons
- **Bot Commands** — `/reminders`, `/clear`, `/token`, and more via Telegram's command menu

### AI & Memory
- **Claude CLI Brain** — Uses Claude Code (`claude -p`) as the AI backend with session continuity (`--continue`)
- **Persistent Memory** — Store and recall information about the user across conversations (2000 char, AI-managed)
- **System Prompts** — Customizable AI behavior per user via `/system`
- **Context Window** — Configurable number of recent messages injected as conversation context

### Scheduled Tasks
- **Simple Reminders** — Schedule notifications via AI brain (no agent required)
- **Autonomous Agent Tasks** — Schedule code tasks (deployments, builds) on specific agents
- **Recurring Tasks** — Support for daily, weekly, and monthly recurring schedules
- **Status Lifecycle** — `pending` → `running` → `completed`/`failed`

### Voice Calls (Telnyx + Gemini Live)
- **Outbound Calls** — AI makes phone calls on your behalf (reservations, inquiries, etc.)
- **Inbound Calls** — AI answers your phone and takes messages
- **Real-time Voice AI** — Gemini Live (`gemini-2.5-flash-native-audio`) for natural conversation
- **Auto Summaries** — Call summaries sent to Telegram after each call
- **Android Phone Bridge** — Route calls through a real Android phone number via companion app

### Email (Resend)
- **Send Emails** — AI sends emails on your behalf via [Resend](https://resend.com)
- **Inbound Webhooks** — Receive and process incoming emails (Svix-signed webhooks)

### Remote Agents
- **Claude Code Agents** — Connect Claude Code instances from any machine via WebSocket
- **Project Discovery** — Agents report their available projects for smart task routing
- **Async Task Execution** — Dispatch coding tasks and get results via Telegram
- **Encrypted Relay** — Optional relay server for agents behind NAT/firewalls

### Background Tasks
- **Long-running Tasks** — Claude Code executes complex tasks autonomously in the background
- **Progress Tracking** — Real-time progress via `PROGRESS.md`, check with `/tasks`

### Tools System
The AI brain has access to these tools, callable during conversations:
- `create_schedule` / `list_schedules` / `delete_schedule` — Schedule tasks and reminders
- `update_memory` — Persistent user memory management
- `send_email` — Send emails via Resend
- `make_call` — Initiate phone calls via Telnyx
- `run_claude` / `list_claude_projects` — Delegate tasks to remote agents
- `create_task` / `get_task_progress` — Background task management
- `run_code` — Execute JavaScript in a sandboxed environment (Goja)

### CLI
Full CLI for direct interaction and scripting — reminders, memory, agents, email, calls, and more. See [CLI Commands](#cli-commands) below.

## Requirements

- **Go 1.24+**
- **[Claude CLI](https://docs.anthropic.com/en/docs/claude-code)** — installed and authenticated (`claude` must be in PATH)
- **Telegram Bot Token** — from [@BotFather](https://t.me/BotFather)

Optional (for additional features):
- [Resend](https://resend.com) account — for email
- [Telnyx](https://telnyx.com) account — for phone calls
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
| `TELNYX_API_KEY` | Enable Telnyx voice calls |
| `TELNYX_APP_ID` | Telnyx TeXML app ID |
| `TELNYX_PHONE_NUMBER` | Telnyx phone number |
| `TELNYX_PUBLIC_KEY` | Telnyx webhook signing public key (base64) |
| `GOOGLE_API_KEY` | Enable Gemini Live real-time voice AI |
| `AGENT_PASSWORD` | Password for agent WebSocket auth |

## CLI Commands

Minerva includes a CLI for direct interaction. This is also how the AI brain interacts with the system.

```bash
# Scheduled Tasks (reminders and autonomous agent tasks)
minerva schedule create "Remind me to call mom" --at "2025-02-06T10:00:00Z"
minerva schedule create "Deploy to production" --at "2025-02-06T18:00:00Z" --agent mac --dir /path/to/project
minerva schedule list
minerva schedule delete 1
minerva schedule run 1  # Trigger immediately

# Memory
minerva memory get
minerva memory set "Prefers dark mode"

# Communication
minerva send "Hello from CLI"

# Agents
minerva agent list
minerva agent run mac "git status" --dir /path/to/project

# Voice calls (requires Telnyx + Gemini)
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
├── voice.go         # Gemini Live voice (Telnyx media streaming)
├── phone.go         # Android phone bridge
├── task_runner.go   # Background task management
├── relay_client.go  # Encrypted relay client
├── audio.go         # Audio format conversion (PCM resampling)
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

Voice calls use **Gemini Live** for real-time audio — Telnyx handles telephony while Gemini provides natural conversation. Call summaries are automatically generated and sent via Telegram.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT — see [LICENSE](LICENSE)
