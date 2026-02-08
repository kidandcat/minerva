#!/data/data/com.termux/files/usr/bin/bash
# Minerva Android Agent - runs on the phone
export HOME=/data/data/com.termux/files/home
export PATH=/data/data/com.termux/files/usr/bin:$PATH
source $HOME/.env 2>/dev/null
exec $HOME/minerva-agent \
    -name phone \
    -server "ws://localhost:8081/agent"
