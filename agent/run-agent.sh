#!/bin/zsh
source ~/.zprofile 2>/dev/null
source ~/.zshrc 2>/dev/null

MINERVA_SERVER="${MINERVA_SERVER:-wss://home.jairo.cloud/agent}"
AGENT_NAME="${AGENT_NAME:-mac}"

exec "$(dirname "$0")/minerva-agent" \
    -name "$AGENT_NAME" \
    -server "$MINERVA_SERVER"
