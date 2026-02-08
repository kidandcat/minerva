#!/data/data/com.termux/files/usr/bin/bash
# Minerva watchdog - restarts on crash
# Run via: nohup ./scripts/minerva-watchdog.sh &

MINERVA_BIN="$HOME/minerva"
LOG_DIR="$HOME/minerva-data"
ENV_FILE="$HOME/.env"

mkdir -p "$LOG_DIR"

# Source environment
if [ -f "$ENV_FILE" ]; then
    set -a
    source "$ENV_FILE"
    set +a
fi

while true; do
    echo "[$(date)] Starting Minerva..."
    "$MINERVA_BIN" 2>&1 | tee -a "$LOG_DIR/minerva.log"
    EXIT_CODE=$?
    echo "[$(date)] Minerva exited with code $EXIT_CODE, restarting in 5s..."

    # Rotate log if too large (>10MB)
    LOG_SIZE=$(stat -c%s "$LOG_DIR/minerva.log" 2>/dev/null || echo 0)
    if [ "$LOG_SIZE" -gt 10485760 ]; then
        mv "$LOG_DIR/minerva.log" "$LOG_DIR/minerva.log.old"
        echo "[$(date)] Log rotated" > "$LOG_DIR/minerva.log"
    fi

    sleep 5
done
