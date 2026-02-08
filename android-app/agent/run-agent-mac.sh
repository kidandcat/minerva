#!/bin/zsh
source ~/.zprofile 2>/dev/null
source ~/.zshrc 2>/dev/null
# Connect to Android Minerva via relay
RELAY_KEY="${RELAY_KEY:-21ZUY8q6DPUbC8ZzbaFoIilfCDTjJw6}"
exec /Users/jairo/minerva/android-app/agent/minerva-agent \
    -name mac-android \
    -server "wss://minerva-relay.fly.dev/agent/ws?key=${RELAY_KEY}"
