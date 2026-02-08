#!/bin/bash
# Connect to Android Minerva via relay (runs on VPS)
RELAY_KEY="${RELAY_KEY:-21ZUY8q6DPUbC8ZzbaFoIilfCDTjJw6}"
exec /home/ubuntu/minerva-agent-android \
    -name vps-android \
    -server "wss://minerva-relay.fly.dev/agent/ws?key=${RELAY_KEY}"
