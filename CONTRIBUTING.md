# Contributing to Minerva

Thanks for your interest in contributing to Minerva! This document covers the basics.

## Development Setup

### Prerequisites

- Go 1.24+
- [Claude CLI](https://docs.anthropic.com/en/docs/claude-code) installed and authenticated
- A Telegram bot token (from [@BotFather](https://t.me/BotFather))

### Getting Started

```bash
git clone https://github.com/kidandcat/minerva.git
cd minerva
cp .env.example .env
# Fill in TELEGRAM_BOT_TOKEN and ADMIN_ID
go build -o minerva .
./minerva
```

### Project Layout

Minerva is a Go monolith — all server code lives in the root package:

- `main.go` — CLI entry point and command routing
- `config.go` — Environment variable loading
- `server.go` — Server lifecycle (start/stop)
- `bot.go` — Telegram bot handlers and commands
- `ai.go` — Claude CLI integration
- `db.go` — SQLite schema and queries
- `tools.go` — Tool definitions and executor
- `voice.go` — Gemini Live voice integration (Telnyx)
- `phone.go` — Android phone bridge
- `agents.go` — Remote agent WebSocket hub
- `webhook.go` — HTTP endpoints

The `agent/` directory is a **separate Go binary** (different `main` package) that connects to the server.

### Building

```bash
# Server
go build -o minerva .

# Agent (separate binary)
cd agent && go build -o minerva-agent .
```

## How to Contribute

### Reporting Bugs

Open an issue with:
- What you expected to happen
- What actually happened
- Steps to reproduce
- Go version and OS

### Suggesting Features

Open an issue describing the feature and why it would be useful.

### Pull Requests

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Ensure the code compiles: `go build ./...`
5. Run vet: `go vet ./...`
6. Commit with a clear message
7. Push and open a PR

### Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep functions focused and small
- Use descriptive variable names
- Add comments only where the logic isn't self-evident
- All code, comments, and commits in English

### Commit Messages

- Use imperative mood: "Add feature" not "Added feature"
- Keep the first line under 72 characters
- Reference issues when applicable: "Fix #123: handle timeout in voice calls"

## Architecture Decisions

- **Single binary**: Minerva is intentionally a monolith for simplicity of deployment
- **Claude CLI as brain**: Instead of using the API directly, Minerva shells out to `claude` CLI with `--continue` for session persistence
- **No custom tool protocol**: Tools are exposed as CLI commands that Claude executes via its built-in Bash tool
- **SQLite**: No external database dependencies — everything in a single file
- **WebSocket agents**: Agents connect to the server (not the other way around), making NAT traversal simpler

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
