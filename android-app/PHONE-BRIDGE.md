# Phone Bridge Setup Guide

Replace Twilio with a physical Android phone for making/receiving calls with Gemini Live AI.

## Architecture

```
Telegram → Minerva Server (Go, on phone via Termux)
                ↓ WebSocket ws://localhost:8081/phone
           Bridge App (Kotlin APK, InCallService)
                ↓ Android Telecom API
           Phone Call (real SIM)
                ↑↓ PCM Audio
           Gemini Live (streaming via server)
```

## Requirements

- **Android 9+ (API 28+)** — InCallService binding requires the app to be default dialer
- **Rooted phone** (Magisk recommended) — for Termux as root, CAPTURE_AUDIO_OUTPUT
- **SIM card** with voice plan
- **Termux** installed from F-Droid
- **Claude Code** installed in Termux (for AI brain)

### Why Android 9+?

On Android 8.1 and below, `InCallService` cannot be bound by the Telecom framework
unless the app is the system default dialer AND implements a full dialer UI. Starting
with Android 9 (API 28), the binding is more permissive and works with just the
intent filters declared in the manifest.

## Components

### 1. Go Server (`android-app/server/`)

Runs in Termux. Handles:
- Telegram bot (long polling)
- WebSocket server at `:8081`
- Phone bridge endpoint: `/phone` (WebSocket for bridge app)
- Phone call endpoint: `/phone/call` (POST to initiate calls)
- Gemini Live connection for AI voice
- Audio resampling (24kHz Gemini ↔ 16kHz phone)
- Transcript capture and call summary generation

Build: `GOOS=android GOARCH=arm64 go build -o /tmp/minerva-android .`

### 2. Bridge App (`android-app/bridge/`)

Kotlin Android app. Handles:
- WebSocket connection to local server (`ws://localhost:8081/phone`)
- `InCallService` (CallHandler) — intercepts Android call events
- `AudioBridge` — bidirectional PCM audio capture/playback
- `BridgeForegroundService` — keeps connection alive, handles `make_call` commands
- `BootReceiver` — auto-starts on phone reboot
- Auto-connects on app launch

Build: `cd android-app/bridge && ./gradlew assembleDebug`
Install: `adb install -r android-app/bridge/app/build/outputs/apk/debug/app-debug.apk`

### 3. Scripts (`android-app/scripts/`)

| Script | Purpose |
|--------|---------|
| `minerva-watchdog.sh` | Auto-restarts server on crash, manages battery, starts agent |
| `battery-manager.sh` | Keeps battery 20-80% (standalone version) |
| `run-agent-phone.sh` | Runs Claude Code agent on the phone |
| `start_minerva.sh` | Manual server start script |

## Call Flow

### Outbound Call (Telegram → Phone)
1. User sends "llama al +34..." via Telegram
2. AI brain calls `make_call` tool → POST `/phone/call`
3. Server sends `{"type":"command","command":"make_call","to":"+34..."}` via WebSocket
4. `BridgeForegroundService` receives command → `Intent.ACTION_CALL`
5. Android creates call → `CallHandler.onCallAdded()` → sends `call_start`
6. Call connects → `CallHandler.onCallActive()` → sends `call_active` + starts AudioBridge
7. Server receives `call_active` → connects to Gemini Live
8. Audio flows: Phone mic → AudioBridge → MinervaClient → Server → Gemini → Server → MinervaClient → AudioBridge → Phone speaker
9. Call ends → `call_end` → Server generates summary → Telegram notification

### Incoming Call
1. Phone rings → `CallHandler.onCallAdded()` → sends `call_start` with direction="incoming"
2. Same flow as outbound from step 6 onward

## Setup on New Phone

### Prerequisites
1. Root the phone with Magisk
2. Install Termux from F-Droid
3. Install Termux:Boot from F-Droid

### Quick Install
```bash
# From Mac with phone connected via USB
cd minerva/android-app
./install.sh
```

### Manual Install

#### A. Server (Termux)
```bash
# Build
GOOS=android GOARCH=arm64 go build -o /tmp/minerva-android ./android-app/server/

# Push
adb push /tmp/minerva-android /data/local/tmp/minerva-android
adb shell "su -c 'cp /data/local/tmp/minerva-android /data/data/com.termux/files/home/minerva && chmod 755 /data/data/com.termux/files/home/minerva'"

# Create .env (see android-app/server CLAUDE.md for all vars)
# Key vars: TELEGRAM_BOT_TOKEN, ADMIN_ID, GOOGLE_API_KEY, RELAY_URL, RELAY_KEY

# Start watchdog
adb shell "su -c 'HOME=/data/data/com.termux/files/home PATH=/data/data/com.termux/files/usr/bin:\$PATH nohup /data/data/com.termux/files/home/scripts/minerva-watchdog.sh > /data/data/com.termux/files/home/minerva-data/watchdog.log 2>&1 &'"
```

#### B. Bridge App
```bash
# Build APK
cd android-app/bridge
./gradlew assembleDebug

# Install
adb install -r app/build/outputs/apk/debug/app-debug.apk

# Launch
adb shell "am start -n com.minerva.bridge/.MainActivity"
```

#### C. Set Default Dialer
Go to **Settings → Apps → Default apps → Phone app** and select **Minerva Phone Bridge**.
This is required for the `CallHandler` (InCallService) to receive call events from Android.

## Updating

```bash
# Rebuild and deploy server
GOOS=android GOARCH=arm64 go build -o /tmp/minerva-android ./android-app/server/
adb push /tmp/minerva-android /data/local/tmp/minerva-android

# Stop, copy, restart
adb shell "su -c 'kill \$(pgrep -f watchdog)'"
sleep 2
adb shell "su -c 'kill \$(pgrep -f /data/data/com.termux/files/home/minerva)'"
sleep 2
adb shell "su -c 'cp /data/local/tmp/minerva-android /data/data/com.termux/files/home/minerva && chmod 755 /data/data/com.termux/files/home/minerva'"
adb shell "su -c 'HOME=/data/data/com.termux/files/home PATH=/data/data/com.termux/files/usr/bin:\$PATH nohup /data/data/com.termux/files/home/scripts/minerva-watchdog.sh > /data/data/com.termux/files/home/minerva-data/watchdog.log 2>&1 &'"

# Rebuild and deploy bridge app
cd android-app/bridge && ./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
adb shell "am force-stop com.minerva.bridge"
adb shell "am start -n com.minerva.bridge/.MainActivity"
```

## Troubleshooting

### Bridge not connecting
- Check server is running: `adb shell "su -c 'pgrep minerva'"`
- Check logs: `adb shell "su -c 'tail -30 /data/data/com.termux/files/home/minerva-data/watchdog.log'" | grep -v relay`
- Screenshot bridge app: `adb exec-out screencap -p > /tmp/bridge.png`

### Call made but no AI audio
- Ensure app is set as **default dialer** (Settings → Default apps → Phone)
- Check logcat: `adb logcat -s CallHandler AudioBridge MinervaClient`
- Requires **Android 9+** for InCallService to work

### "No phone connected" error
- Bridge app must be running and connected
- Look for `[PhoneBridge] Connected` in server logs
- Bridge auto-reconnects every 5 seconds

### "Network not available" on call
- Phone needs active SIM with voice capability
- Check signal strength in status bar

## Environment Variables (.env)

```
TELEGRAM_BOT_TOKEN=...
ADMIN_ID=282611642
DATABASE_PATH=/data/data/com.termux/files/home/minerva-data/minerva.db
GOOGLE_API_KEY=...          # For Gemini Live voice
RELAY_URL=https://minerva-relay.fly.dev
RELAY_KEY=...
MINERVA_WORKSPACE=/data/data/com.termux/files/home/workspace
RESEND_API_KEY=...          # For email
SSL_CERT_FILE=/data/data/com.termux/files/usr/etc/tls/cert.pem
```
