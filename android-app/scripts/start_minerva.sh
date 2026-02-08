#!/data/data/com.termux/files/usr/bin/bash

# Fix permissions (in case they were reset after reboot)
chmod 755 $HOME/minerva $HOME/start_minerva.sh 2>/dev/null
chmod 755 /data/data/com.termux/files/usr/bin/claude 2>/dev/null

cd $HOME
source .env
export SSL_CERT_DIR=/etc/security/cacerts
export PATH=/data/data/com.termux/files/usr/bin:$PATH
export LD_LIBRARY_PATH=/data/data/com.termux/files/usr/lib
./minerva
