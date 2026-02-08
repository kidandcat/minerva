#!/bin/zsh
source ~/.zprofile 2>/dev/null
source ~/.zshrc 2>/dev/null
exec /Users/jairo/minerva/agent/minerva-agent \
    -name mac \
    -server "wss://home.jairo.cloud/agent"
