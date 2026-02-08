#!/data/data/com.termux/files/usr/bin/bash
# Termux setup script - runs inside Termux via SSH or ADB
# Sets up the environment for Minerva
set -e

echo "=== Minerva Termux Setup ==="

# Update and install packages
echo "[1/6] Installing packages..."
pkg update -y
pkg install -y nodejs-lts openssh

# Install Claude Code
echo "[2/6] Installing Claude Code CLI..."
npm install -g @anthropic-ai/claude-code

# Setup SSH
echo "[3/6] Configuring SSH..."
mkdir -p ~/.ssh
if [ -f /data/local/tmp/authorized_keys ]; then
    cat /data/local/tmp/authorized_keys >> ~/.ssh/authorized_keys
    chmod 600 ~/.ssh/authorized_keys
fi
# Generate host key if needed
if [ ! -f ~/.ssh/ssh_host_rsa_key ]; then
    ssh-keygen -t rsa -f ~/.ssh/ssh_host_rsa_key -N "" -q
fi

# Create directories
echo "[4/6] Creating directories..."
mkdir -p ~/minerva-data
mkdir -p ~/minerva-tasks
mkdir -p ~/scripts
mkdir -p ~/.termux/boot

# Setup scripts
echo "[5/6] Setting up scripts..."
cp /data/local/tmp/minerva-watchdog.sh ~/scripts/minerva-watchdog.sh
cp /data/local/tmp/boot-minerva.sh ~/.termux/boot/start-minerva.sh
chmod +x ~/scripts/minerva-watchdog.sh
chmod +x ~/.termux/boot/start-minerva.sh

# Apply root protections
echo "[6/6] Applying root protections..."
if command -v su &>/dev/null; then
    # Disable phantom process killer
    su -c "settings put global settings_enable_monitor_phantom_procs false" 2>/dev/null || true
    # Whitelist Termux from battery optimization
    su -c "dumpsys deviceidle whitelist +com.termux" 2>/dev/null || true
    echo "Root protections applied"
else
    echo "WARNING: Root not available. Some protections could not be applied."
fi

# Enable wake lock
termux-wake-lock 2>/dev/null || true

echo ""
echo "=== Termux setup complete ==="
echo "SSHD port: 8022"
echo "Minerva binary: ~/minerva"
echo "Config: ~/.env"
echo "Data: ~/minerva-data/"
