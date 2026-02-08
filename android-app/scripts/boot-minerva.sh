#!/data/data/com.termux/files/usr/bin/bash
# Termux:Boot auto-start script
# Place in ~/.termux/boot/start-minerva.sh

termux-wake-lock

# Wait for network
sleep 10

# Start SSHD
sshd

# Start Minerva watchdog as root (needed for network access)
# The binary must run as root to have inet socket permissions
su -c "export HOME=$HOME && export PATH=/data/data/com.termux/files/usr/bin:\$PATH && nohup bash $HOME/scripts/minerva-watchdog.sh >> $HOME/minerva-data/boot.log 2>&1 &"
