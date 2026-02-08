#!/data/data/com.termux/files/usr/bin/bash
# Minerva watchdog - restarts on crash
# Run via: nohup ./scripts/minerva-watchdog.sh &

MINERVA_BIN="$HOME/minerva"
AGENT_BIN="$HOME/minerva-agent"
LOG_DIR="$HOME/minerva-data"
ENV_FILE="$HOME/.env"

mkdir -p "$LOG_DIR"

# Source environment
if [ -f "$ENV_FILE" ]; then
    set -a
    source "$ENV_FILE"
    set +a
fi

# Set workspace for Claude CLI
export MINERVA_WORKSPACE="$HOME/workspace"

# Start agent in background (auto-restarts with minerva)
start_agent() {
    if [ -x "$AGENT_BIN" ]; then
        pkill -f minerva-agent 2>/dev/null
        sleep 1
        nohup "$AGENT_BIN" -name phone -server ws://localhost:8081/agent >> "$LOG_DIR/agent.log" 2>&1 &
        echo "[$(date)] Agent started (PID $!)"
    fi
}

while true; do
    echo "[$(date)] Starting Minerva..."
    start_agent
    "$MINERVA_BIN" 2>&1 | tee -a "$LOG_DIR/minerva.log"
    EXIT_CODE=$?
    echo "[$(date)] Minerva exited with code $EXIT_CODE, restarting in 5s..."

    # Rotate log if too large (>10MB)
    LOG_SIZE=$(wc -c < "$LOG_DIR/minerva.log" 2>/dev/null || echo 0)
    if [ "$LOG_SIZE" -gt 10485760 ]; then
        mv "$LOG_DIR/minerva.log" "$LOG_DIR/minerva.log.old"
        echo "[$(date)] Log rotated" > "$LOG_DIR/minerva.log"
    fi

    sleep 5
done
