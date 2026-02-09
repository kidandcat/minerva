# Minerva Installation Guide

Step-by-step guide to install Minerva on a VPS. These instructions are designed to be followed by Claude Code or manually.

## Prerequisites

- **VPS** with Ubuntu 22.04+ (or Debian 12+), minimum 1 GB RAM
- **Domain** pointing to the VPS IP (A record), e.g. `minerva.example.com`
- **Go 1.24+** installed (for binary builds) OR **Docker + Docker Compose** installed
- **Claude CLI** installed and authenticated — [install instructions](https://docs.anthropic.com/en/docs/claude-code)

### External Service Accounts (configure as needed)

| Service | Purpose | Sign up |
|---------|---------|---------|
| Telegram Bot | Chat interface (required) | [@BotFather](https://t.me/BotFather) |
| Anthropic | AI backend (required — Claude CLI) | [console.anthropic.com](https://console.anthropic.com) |
| Google AI | Voice AI (Gemini Live) | [aistudio.google.com/apikey](https://aistudio.google.com/apikey) |
| Twilio | Phone calls (outbound/inbound) | [twilio.com](https://www.twilio.com) |
| Resend | Email sending | [resend.com](https://resend.com) |

## Option A: Docker Install (Recommended)

### 1. Clone the repository

```bash
git clone https://github.com/kidandcat/minerva.git
cd minerva/install
```

### 2. Run the setup script

```bash
./setup.sh
```

This will prompt for all configuration values and generate a `.env` file.

### 3. Set your domain

Edit `caddy/Caddyfile` and replace `{$MINERVA_DOMAIN:localhost}` with your domain:

```
minerva.example.com {
    reverse_proxy minerva:8080
}
```

Or set the environment variable for Caddy:

```bash
export MINERVA_DOMAIN=minerva.example.com
```

### 4. Start services

```bash
docker compose up -d
```

Caddy will automatically obtain a TLS certificate for your domain.

### 5. Verify

```bash
# Check containers are running
docker compose ps

# Check logs
docker compose logs minerva

# Health check
curl https://minerva.example.com/health
```

## Option B: Binary Install (systemd)

### 1. Clone and build

```bash
git clone https://github.com/kidandcat/minerva.git
cd minerva
go build -o minerva .
```

### 2. Create system user

```bash
sudo useradd --system --home-dir /opt/minerva --create-home --shell /usr/sbin/nologin minerva
```

### 3. Install the binary and workspace

```bash
sudo cp minerva /opt/minerva/
sudo mkdir -p /opt/minerva/workspace
sudo cp -r workspace/CLAUDE.md /opt/minerva/workspace/
sudo chown -R minerva:minerva /opt/minerva
```

### 4. Configure

```bash
sudo cp install/.env.template /opt/minerva/.env
sudo nano /opt/minerva/.env  # fill in values
sudo chmod 600 /opt/minerva/.env
sudo chown minerva:minerva /opt/minerva/.env
```

Or run the setup script:

```bash
cd install && ./setup.sh
sudo cp .env /opt/minerva/.env
sudo chown minerva:minerva /opt/minerva/.env
sudo chmod 600 /opt/minerva/.env
```

### 5. Install systemd service

```bash
sudo cp install/systemd/minerva.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable minerva
sudo systemctl start minerva
```

### 6. Install Caddy (reverse proxy)

```bash
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install caddy
```

Configure Caddy:

```bash
sudo tee /etc/caddy/Caddyfile << 'EOF'
minerva.example.com {
    reverse_proxy localhost:8080
}
EOF

sudo systemctl reload caddy
```

### 7. Verify

```bash
sudo systemctl status minerva
curl https://minerva.example.com/health
```

## Configuring the Telegram Bot

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts to create a bot
3. Copy the **bot token** — set it as `TELEGRAM_BOT_TOKEN`
4. Get your user ID: message [@userinfobot](https://t.me/userinfobot) and copy the ID — set it as `ADMIN_ID`
5. Send `/start` to your new bot — you should get a welcome message

## Configuring External Services

### Anthropic (Claude CLI)

Claude CLI must be installed and authenticated on the machine running Minerva.

```bash
# Install Claude CLI
npm install -g @anthropic-ai/claude-code

# Authenticate (interactive — run once)
claude
```

The `claude` binary must be in PATH for the Minerva process.

### Google AI (Gemini Live Voice)

1. Go to [aistudio.google.com/apikey](https://aistudio.google.com/apikey)
2. Create an API key
3. Set `GOOGLE_API_KEY` in `.env`

### Twilio (Phone Calls)

1. Create a Twilio account at [twilio.com](https://www.twilio.com)
2. Get a phone number with voice capability
3. Set in `.env`:
   - `TWILIO_ACCOUNT_SID` — from Twilio dashboard
   - `TWILIO_AUTH_TOKEN` — from Twilio dashboard
   - `TWILIO_PHONE_NUMBER` — your Twilio number (e.g. `+14155551234`)
4. Configure voice webhook in Twilio console:
   - Go to Phone Numbers → your number → Voice Configuration
   - Set "A call comes in" webhook to: `https://minerva.example.com/voice/incoming` (POST)
5. **Important**: Enable geo permissions for any countries you want to call:
   - Go to Voice → Settings → Geo Permissions

### Resend (Email)

1. Create a Resend account at [resend.com](https://resend.com)
2. Verify your domain (DNS records)
3. Create an API key
4. Set in `.env`:
   - `RESEND_API_KEY`
   - `FROM_EMAIL` — e.g. `Minerva <minerva@yourdomain.com>`
5. For inbound email: configure a webhook in Resend pointing to `https://minerva.example.com/email/webhook`

## Verification Checklist

After installation, verify each component:

- [ ] `curl https://your-domain.com/health` returns `200 OK`
- [ ] Send `/start` to your bot on Telegram — get welcome message
- [ ] Send a text message — get an AI response
- [ ] Send `/reminders` — shows "No pending reminders"
- [ ] (If Twilio configured) Make a test call via Telegram: "Call +1234567890"
- [ ] (If Resend configured) Send a test email via Telegram: "Send an email to test@example.com"

## Troubleshooting

### Minerva won't start

```bash
# Check logs
sudo journalctl -u minerva -f          # systemd
docker compose logs -f minerva          # docker

# Common issues:
# - TELEGRAM_BOT_TOKEN invalid → check with BotFather
# - Port in use → change WEBHOOK_PORT
# - Database permission error → check /opt/minerva ownership
```

### No AI responses

```bash
# Check Claude CLI is installed and accessible
which claude
claude --version

# Test Claude CLI directly
echo "hello" | claude -p --output-format text

# Check workspace directory exists
ls -la /opt/minerva/workspace/CLAUDE.md
```

### Voice calls not working

- Ensure `BASE_URL` is set to your public HTTPS URL
- Verify Twilio webhook points to `https://your-domain.com/voice/incoming`
- Check `GOOGLE_API_KEY` is valid
- Ensure Twilio geo permissions are enabled for target countries

### TLS certificate issues

```bash
# Check Caddy logs
sudo journalctl -u caddy -f            # systemd
docker compose logs -f caddy            # docker

# Verify DNS
dig your-domain.com
# Should return your VPS IP
```

### Bot not responding

- Check `ADMIN_ID` matches your Telegram user ID
- Ensure the bot token is correct
- Check if another instance is running (lock file at `~/.minerva.lock`)

## Updating

### Docker

```bash
cd minerva
git pull
docker compose down
docker compose up -d --build
```

### Binary

```bash
cd minerva
git pull
go build -o minerva .
sudo systemctl stop minerva
sudo cp minerva /opt/minerva/
sudo systemctl start minerva
```
