#!/bin/bash
# Minerva Android - One-Click Install Script
# Runs from Mac with phone connected via USB
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SERVER_DIR="$SCRIPT_DIR/server"
SCRIPTS_DIR="$SCRIPT_DIR/scripts"
BUILD_DIR="/tmp/minerva-android-build"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info() { echo -e "${BLUE}[INFO]${NC} $1"; }
success() { echo -e "${GREEN}[OK]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

echo ""
echo "========================================="
echo "  Minerva Android - One-Click Install"
echo "========================================="
echo ""

# ─── Check prerequisites ───────────────────────────────────────────

info "Checking prerequisites..."

command -v adb >/dev/null 2>&1 || error "adb not found. Install Android SDK Platform Tools."
command -v go >/dev/null 2>&1 || error "Go not found. Install Go 1.24+."

# Check ADB connection
ADB_DEVICE=$(adb devices | grep -v "List" | grep "device$" | head -1 | cut -f1)
if [ -z "$ADB_DEVICE" ]; then
    error "No Android device connected via ADB. Connect your phone and enable USB debugging."
fi
success "ADB device: $ADB_DEVICE"

# Check if Termux is installed
if ! adb shell pm list packages 2>/dev/null | grep -q com.termux; then
    warn "Termux not found on device."
    echo "Please install Termux from F-Droid: https://f-droid.org/en/packages/com.termux/"
    echo "Then run this script again."
    exit 1
fi
success "Termux found"

# Check if Termux:Boot is installed
if ! adb shell pm list packages 2>/dev/null | grep -q com.termux.boot; then
    warn "Termux:Boot not found. Auto-start on reboot will not work."
    echo "Install from F-Droid: https://f-droid.org/en/packages/com.termux.boot/"
    read -p "Continue anyway? [y/N] " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy]$ ]] || exit 1
fi

# ─── Collect configuration ──────────────────────────────────────────

echo ""
info "Configuration..."

# Telegram Bot Token
if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
    read -p "Telegram Bot Token: " TELEGRAM_BOT_TOKEN
fi
[ -z "$TELEGRAM_BOT_TOKEN" ] && error "Telegram bot token is required"

# Admin ID
if [ -z "$ADMIN_ID" ]; then
    read -p "Telegram Admin ID (your user ID): " ADMIN_ID
fi
[ -z "$ADMIN_ID" ] && error "Admin ID is required"

# Claude Code OAuth Token
if [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ]; then
    read -p "Claude Code OAuth Token: " CLAUDE_CODE_OAUTH_TOKEN
fi
[ -z "$CLAUDE_CODE_OAUTH_TOKEN" ] && error "OAuth token is required"

# Google API Key (for Gemini Live voice)
if [ -z "$GOOGLE_API_KEY" ]; then
    read -p "Google API Key (for voice, leave empty to skip): " GOOGLE_API_KEY
fi

# Relay key (optional)
if [ -z "$RELAY_KEY" ]; then
    read -p "Relay Key (optional, leave empty to skip): " RELAY_KEY
fi

RELAY_URL="${RELAY_URL:-https://minerva-relay.fly.dev}"

echo ""
success "Configuration collected"

# ─── Phase 1: Build ────────────────────────────────────────────────

echo ""
echo "═══ Phase 1: Build ═══"
mkdir -p "$BUILD_DIR"

info "Cross-compiling Minerva for Android ARM64..."
cd "$SERVER_DIR"
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -o "$BUILD_DIR/minerva" .
success "Binary built: $(du -h "$BUILD_DIR/minerva" | cut -f1)"

# ─── Phase 2: Push files to phone ──────────────────────────────────

echo ""
echo "═══ Phase 2: Push to Phone ═══"

TERMUX_HOME="/data/data/com.termux/files/home"
TERMUX_TMP="/data/local/tmp"

# Get Termux UID for correct file ownership
TERMUX_UID=$(adb shell "stat -c %u $TERMUX_HOME" 2>/dev/null | tr -d '\r')
TERMUX_GID=$(adb shell "stat -c %g $TERMUX_HOME" 2>/dev/null | tr -d '\r')
info "Termux UID: $TERMUX_UID, GID: $TERMUX_GID"

# Push binary
info "Pushing Minerva binary..."
adb push "$BUILD_DIR/minerva" "$TERMUX_TMP/minerva"
adb shell "su -c 'cp $TERMUX_TMP/minerva $TERMUX_HOME/minerva && chmod +x $TERMUX_HOME/minerva && chown $TERMUX_UID:$TERMUX_GID $TERMUX_HOME/minerva'"
success "Binary pushed"

# Push .env file
info "Creating .env config..."
ENV_CONTENT="TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN
ADMIN_ID=$ADMIN_ID
CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN
DATABASE_PATH=$TERMUX_HOME/minerva-data/minerva.db"

if [ -n "$GOOGLE_API_KEY" ]; then
    ENV_CONTENT="$ENV_CONTENT
GOOGLE_API_KEY=$GOOGLE_API_KEY"
fi

if [ -n "$RELAY_KEY" ]; then
    ENV_CONTENT="$ENV_CONTENT
RELAY_URL=$RELAY_URL
RELAY_KEY=$RELAY_KEY"
fi

echo "$ENV_CONTENT" > "$BUILD_DIR/.env"
adb push "$BUILD_DIR/.env" "$TERMUX_TMP/.env"
adb shell "su -c 'cp $TERMUX_TMP/.env $TERMUX_HOME/.env && chown $TERMUX_UID:$TERMUX_GID $TERMUX_HOME/.env'"
success ".env pushed"

# Push scripts
info "Pushing scripts..."
adb push "$SCRIPTS_DIR/minerva-watchdog.sh" "$TERMUX_TMP/minerva-watchdog.sh"
adb push "$SCRIPTS_DIR/boot-minerva.sh" "$TERMUX_TMP/boot-minerva.sh"
adb push "$SCRIPT_DIR/termux-setup.sh" "$TERMUX_TMP/termux-setup.sh"
success "Scripts pushed"

# Push SSH key
SSH_KEY="$HOME/.ssh/id_ed25519.pub"
if [ ! -f "$SSH_KEY" ]; then
    SSH_KEY="$HOME/.ssh/id_rsa.pub"
fi
if [ -f "$SSH_KEY" ]; then
    info "Pushing SSH public key..."
    adb push "$SSH_KEY" "$TERMUX_TMP/authorized_keys"
    success "SSH key pushed"
else
    warn "No SSH public key found. You'll need to set up SSH manually."
fi

# ─── Phase 2.5: Magisk Module (Phone Bridge Permissions) ──────────

echo ""
echo "═══ Phase 2.5: Magisk Module ═══"

MAGISK_DIR="$SCRIPT_DIR/magisk"
MAGISK_ZIP="/tmp/minerva-magisk.zip"

if adb shell "su -c 'which magisk'" >/dev/null 2>&1; then
    info "Magisk detected, packaging permissions module..."

    # Build the Magisk module zip
    (cd "$MAGISK_DIR" && zip -r "$MAGISK_ZIP" . -x '*.DS_Store')
    success "Magisk module packaged: $(du -h "$MAGISK_ZIP" | cut -f1)"

    # Push zip to phone
    info "Pushing Magisk module to phone..."
    adb push "$MAGISK_ZIP" "/data/local/tmp/minerva-magisk.zip"
    success "Module pushed"

    # Install via Magisk
    info "Installing Magisk module..."
    adb shell "su -c 'magisk --install-module /data/local/tmp/minerva-magisk.zip'" 2>&1
    success "Magisk module installed"

    # Cleanup
    adb shell "su -c 'rm /data/local/tmp/minerva-magisk.zip'" 2>/dev/null || true
    rm -f "$MAGISK_ZIP"

    warn "A reboot is needed for the Magisk module to take effect."
    warn "After reboot, CAPTURE_AUDIO_OUTPUT and privileged permissions will be active."
else
    warn "Magisk not detected on device. Skipping permissions module."
    warn "Without Magisk, audio capture during calls will not work."
    warn "Install Magisk and re-run this script to enable call audio capture."
fi

# ─── Phase 3: Setup Termux ─────────────────────────────────────────

echo ""
echo "═══ Phase 3: Setup Termux ═══"

info "Running Termux setup (this may take a few minutes)..."
info "Make sure Termux is open on the device!"
echo ""

# The setup script runs inside Termux
# We use 'am broadcast' to run a command in Termux or fallback to su
adb shell "su -c 'chmod +x $TERMUX_TMP/termux-setup.sh'"

# Try running via Termux's run-command intent (if Termux:API is installed)
# Fallback: use su to run as Termux user
TERMUX_UID=$(adb shell "stat -c %u $TERMUX_HOME" 2>/dev/null | tr -d '\r')
if [ -n "$TERMUX_UID" ]; then
    info "Running setup as Termux user (UID: $TERMUX_UID)..."
    adb shell "su -c 'su $TERMUX_UID -c \"export HOME=$TERMUX_HOME && export PATH=/data/data/com.termux/files/usr/bin:\\\$PATH && bash $TERMUX_TMP/termux-setup.sh\"'" 2>&1
else
    warn "Could not determine Termux UID. Running setup via su..."
    adb shell "su -c 'bash $TERMUX_TMP/termux-setup.sh'" 2>&1
fi

success "Termux setup complete"

# ─── Phase 4: Start ────────────────────────────────────────────────

echo ""
echo "═══ Phase 4: Start ═══"

info "Starting SSHD..."
adb shell "su -c 'su $TERMUX_UID -c \"export HOME=$TERMUX_HOME && export PATH=/data/data/com.termux/files/usr/bin:\\\$PATH && sshd\"'" 2>/dev/null || true

info "Starting Minerva watchdog..."
adb shell "su -c 'su $TERMUX_UID -c \"export HOME=$TERMUX_HOME && export PATH=/data/data/com.termux/files/usr/bin:\\\$PATH && nohup bash $TERMUX_HOME/scripts/minerva-watchdog.sh >> $TERMUX_HOME/minerva-data/boot.log 2>&1 &\"'" 2>/dev/null || true

# Wait for startup
sleep 3

# Verify
info "Checking if Minerva is running..."
RUNNING=$(adb shell "ps -A 2>/dev/null | grep minerva | grep -v grep" || true)
if [ -n "$RUNNING" ]; then
    success "Minerva is running!"
else
    warn "Could not verify Minerva is running. Check logs on the phone."
fi

# Get phone IP
PHONE_IP=$(adb shell "ip route | grep 'src' | head -1 | awk '{print \$NF}'" 2>/dev/null | tr -d '\r')

echo ""
echo "========================================="
echo "  Installation Complete!"
echo "========================================="
echo ""
echo "SSH access:    ssh -p 8022 $PHONE_IP"
echo "Logs:          ssh -p 8022 $PHONE_IP tail -f minerva-data/minerva.log"
echo "Telegram:      Send a message to your bot"
echo "Token update:  /token <new_token> (in Telegram)"
echo ""
if [ -n "$RELAY_KEY" ]; then
    # Compute webhook URL
    WEBHOOK_HASH=$(echo -n "$RELAY_KEY" | shasum -a 256 | cut -c1-48)
    echo "Relay webhook: $RELAY_URL/hook/$WEBHOOK_HASH"
    echo "(Configure this URL in Resend for email webhooks)"
    echo ""
fi
echo "To restart:    ssh -p 8022 $PHONE_IP 'pkill minerva'"
echo "To update:     Re-run this script"
echo ""
