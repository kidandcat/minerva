#!/bin/zsh
source ~/.zprofile 2>/dev/null
source ~/.zshrc 2>/dev/null
exec /Users/jairo/minerva/agent/minerva-agent --name mac --server ws://51.254.142.231:8081/agent --dir /Users/jairo
