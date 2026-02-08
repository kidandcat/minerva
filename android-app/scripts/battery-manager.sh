#!/data/data/com.termux/files/usr/bin/bash
# Battery manager - keeps battery between 20% and 80%
# Disables charging at 80%, re-enables at 20%
# Run as root: nohup ./scripts/battery-manager.sh &

CHARGE_FILE="/sys/class/power_supply/battery/charging_enabled"
CAPACITY_FILE="/sys/class/power_supply/battery/capacity"
HIGH=80
LOW=20
CHECK_INTERVAL=60

echo "[$(date)] Battery manager started (range: ${LOW}%-${HIGH}%)"

while true; do
    LEVEL=$(cat "$CAPACITY_FILE" 2>/dev/null)
    CHARGING=$(cat "$CHARGE_FILE" 2>/dev/null)

    if [ -z "$LEVEL" ]; then
        echo "[$(date)] ERROR: cannot read battery level"
        sleep "$CHECK_INTERVAL"
        continue
    fi

    if [ "$LEVEL" -ge "$HIGH" ] && [ "$CHARGING" = "1" ]; then
        echo 0 > "$CHARGE_FILE"
        echo "[$(date)] Battery at ${LEVEL}% >= ${HIGH}%, charging DISABLED"
    elif [ "$LEVEL" -le "$LOW" ] && [ "$CHARGING" = "0" ]; then
        echo 1 > "$CHARGE_FILE"
        echo "[$(date)] Battery at ${LEVEL}% <= ${LOW}%, charging ENABLED"
    fi

    sleep "$CHECK_INTERVAL"
done
