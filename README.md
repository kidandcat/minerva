# Minerva

Personal AI assistant powered by Claude, accessible via Telegram. Minerva can manage reminders, store memory, send emails, make phone calls, and delegate coding tasks to remote Claude Code agents.

## Architecture

```
┌──────────────┐     ┌──────────────────────────┐     ┌─────────────┐
│   Telegram   │◄───►│    Minerva (Go binary)   │◄───►│  Claude CLI  │
│   Bot API    │     │                          │     │  (AI brain)  │
└──────────────┘     │  ┌────────────────────┐  │     └─────────────┘
                     │  │   Webhook Server   │  │
┌──────────────┐     │  │  /webhook/email    │  │     ┌─────────────┐
│   Resend     │────►│  │  /agent (WS)       │  │◄───►│   Agent:    │
│   (email)    │     │  │  /twilio/ws        │  │     │   mac       │
└──────────────┘     │  └────────────────────┘  │     └─────────────┘
                     │                          │
┌──────────────┐     │  ┌────────────────────┐  │     ┌─────────────┐
│   Twilio     │◄───►│  │     SQLite DB      │  │◄───►│   Agent:    │
│   (voice)    │     │  └────────────────────┘  │     │   vps       │
└──────────────┘     └──────────────────────────┘     └─────────────┘
```

**Minerva brain** handles: communication, reminders, memory, organization.
**Agents** handle: all source code tasks (git, builds, debugging, code changes).

## Features

- **AI Chat** — Talk to Claude via Telegram with persistent conversation history
- **Reminders** — Schedule reminders with natural language
- **Memory** — Persistent memory across conversations
- **Remote Agents** — Delegate coding tasks to Claude Code instances on any machine
- **Email** — Send and receive emails (via Resend)
- **Voice Calls** — Make and receive phone calls (via Twilio)
- **Background Tasks** — Long-running tasks with progress tracking
- **Code Execution** — Run JavaScript snippets in a sandbox

## Requirements

- **Go 1.24+**
- **[Claude CLI](https://docs.anthropic.com/en/docs/claude-code)** — installed and authenticated
- **Telegram Bot Token** — from [@BotFather](https://t.me/BotFather)

## Quick Start

```bash
# Clone
git clone https://github.com/kidandcat/minerva.git
cd minerva

# Configure
cp .env.example .env
# Edit .env with your Telegram bot token and admin ID

# Build and run
go build -o minerva .
./minerva
```

## Configuration

Copy `.env.example` to `.env` and fill in the required values:

| Variable | Required | Description |
|----------|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Yes | Telegram bot token from @BotFather |
| `ADMIN_ID` | Yes | Your Telegram user ID |
| `DATABASE_PATH` | No | SQLite database path (default: `./minerva.db`) |
| `MINERVA_WORKSPACE` | No | Claude CLI workspace directory (default: `./workspace`) |
| `WEBHOOK_PORT` | No | HTTP server port (default: `8080`) |
| `RESEND_API_KEY` | No | Resend API key for email |
| `TWILIO_ACCOUNT_SID` | No | Twilio account SID for voice calls |
| `TWILIO_AUTH_TOKEN` | No | Twilio auth token |
| `TWILIO_PHONE_NUMBER` | No | Twilio phone number |
| `AGENT_PASSWORD` | No | Password for agent authentication |
| `MAX_CONTEXT_MESSAGES` | No | Messages to include as context (default: `20`) |

## Docker

```bash
cp .env.example .env
# Edit .env with your values

docker compose up -d
```

## CLI

Minerva includes a CLI for direct interaction with the system. This is how Claude (the AI brain) interacts with Minerva's features.

```bash
# Reminders
minerva reminder create "Buy groceries" --at "2025-02-06T10:00:00Z"
minerva reminder list
minerva reminder delete 1

# Memory
minerva memory get
minerva memory set "Prefers dark mode"

# Send message to admin via Telegram
minerva send "Hello from CLI"

# Agents
minerva agent list
minerva agent run mac "git status" --dir /path/to/project
```

## Agents

Agents are Claude Code instances running on remote machines that connect to Minerva via WebSocket. They handle all code-related tasks.

### Running an agent

```bash
cd agent
go build -o minerva-agent .

# Connect to Minerva
./minerva-agent --name my-agent --server ws://your-server:8080/agent --dir /home/user
```

### Agent with password

```bash
# Build with password
go build -ldflags "-X main.Password=your-secret" -o minerva-agent ./agent

# Server must have matching AGENT_PASSWORD in .env
```

### Install as service

**macOS (launchd):**

```bash
# Create ~/Library/LaunchAgents/com.minerva.agent.plist
# See agent/run-agent.sh for reference
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

## Project Structure

```
minerva/
├── main.go          # Entry point, CLI commands
├── config.go        # Configuration from environment
├── db.go            # SQLite database schema and queries
├── ai.go            # Claude CLI integration (queue-based)
├── bot.go           # Telegram bot handlers
├── tools.go         # Tool executor (reminders, memory, email, etc.)
├── agents.go        # Agent hub (WebSocket server)
├── webhook.go       # HTTP server (email webhooks, agent API)
├── task_runner.go   # Background task management
├── twilio.go        # Voice call handling
├── workspace/
│   └── CLAUDE.md    # System prompt and instructions for Claude
├── agent/
│   ├── main.go      # Agent binary entry point
│   ├── client.go    # WebSocket client
│   └── executor.go  # Claude CLI execution
├── tools/
│   ├── reminder.go  # Reminder operations
│   ├── memory.go    # Memory storage
│   ├── email.go     # Resend email
│   └── code.go      # JavaScript execution (Goja)
└── mcp/
    └── main.go      # MCP server (JSON-RPC)
```

## How It Works

1. **Message arrives** via Telegram
2. **Minerva queues** it for Claude CLI processing (serial, to maintain `--continue` session)
3. **Claude responds**, optionally calling tools (reminders, agents, email, etc.)
4. **Tool results** are fed back to Claude for final response
5. **Response sent** to user via Telegram

Agent tasks are **asynchronous** — Minerva dispatches work to agents and continues chatting. When an agent completes a task, the result is fed back through Minerva's AI brain for summarization.

## License

MIT — see [LICENSE](LICENSE)
