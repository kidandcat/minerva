# Minerva Bridge - Android Phone Bridge

Android app that bridges your phone's calls to the Minerva AI server, enabling AI-assisted phone conversations.

## Features

- **WebSocket Connection**: Maintains persistent connection to Minerva server
- **Bidirectional Audio**: Captures both incoming and outgoing call audio (requires root)
- **InCallService Integration**: Receives call events from Android Telecom framework
- **Auto-start on Boot**: Service starts automatically when device boots
- **Auto-answer Option**: Optionally answer calls automatically for hands-free operation

## Requirements

- Android 8.0 (API 26) or higher
- Rooted device with Magisk (for full bidirectional audio capture)
- CAPTURE_AUDIO_OUTPUT permission (granted via Magisk module)

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Minerva Bridge App                        │
│                                                              │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │ MainActivity │  │ MinervaService│  │   CallHandler    │  │
│  │   (UI)       │  │  (WebSocket)  │  │  (InCallService) │  │
│  └─────────────┘  └──────────────┘  └───────────────────┘  │
│                           │                    │            │
│                           ▼                    │            │
│                    ┌──────────────┐            │            │
│                    │ AudioBridge  │◄───────────┘            │
│                    │ (Capture +   │                         │
│                    │  Playback)   │                         │
│                    └──────────────┘                         │
└─────────────────────────────────────────────────────────────┘
                           │
                           │ WebSocket (JSON + Base64 audio)
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                    Minerva Server                            │
│                                                              │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │ voice.go    │  │   Gemini     │  │   TTS Engine      │  │
│  │ (Handler)   │──│   Live API   │──│   (Response)      │  │
│  └─────────────┘  └──────────────┘  └───────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

## WebSocket Protocol

### Messages to Server

```json
// Registration (on connect)
{"type": "register", "device_id": "abc123", "device_type": "android"}

// Call started
{"type": "call_start", "direction": "incoming", "from": "+1234567890", "caller_name": "John Doe"}

// Call became active
{"type": "call_active"}

// Audio chunk (20ms PCM 16-bit 16kHz mono)
{"type": "audio", "data": "<base64 encoded PCM>"}

// Call ended
{"type": "call_end"}
```

### Messages from Server

```json
// Audio to play in call (AI response)
{"type": "audio", "data": "<base64 encoded PCM>"}

// Call control commands
{"type": "command", "command": "answer"}  // Answer ringing call
{"type": "command", "command": "hangup"}  // End call
{"type": "command", "command": "hold"}    // Hold call
{"type": "command", "command": "unhold"}  // Resume call
```

## Audio Format

- **Sample Rate**: 16000 Hz
- **Channels**: Mono
- **Bit Depth**: 16-bit signed PCM
- **Chunk Size**: 20ms (640 bytes)
- **Encoding**: Base64 for WebSocket transport

This matches the format expected by Google Gemini Live API.

## Building

### From Computer (Android Studio)

1. Open the `android/` folder in Android Studio
2. Sync Gradle
3. Build -> Build Bundle(s) / APK(s) -> Build APK(s)

### From Computer (Command Line)

```bash
cd android
./gradlew assembleDebug
# APK at app/build/outputs/apk/debug/app-debug.apk
```

### From Android (Termux)

```bash
pkg install openjdk-17 gradle git
cd minerva/android
./setup.sh
```

## Installation

### Standard Installation (Limited Functionality)

```bash
adb install app/build/outputs/apk/debug/app-debug.apk
```

This provides basic functionality but cannot capture call audio due to permission restrictions.

### Privileged Installation (Full Functionality)

Requires rooted device with Magisk:

1. **Install Magisk Module**:
   ```bash
   cd android/magisk
   zip -r minerva-magisk.zip *
   # Transfer to device and install via Magisk Manager
   ```

2. **Install as Privileged App**:
   ```bash
   adb root
   adb remount
   adb shell mkdir -p /system/priv-app/MinervaBridge
   adb push app/build/outputs/apk/debug/app-debug.apk /system/priv-app/MinervaBridge/MinervaBridge.apk
   adb shell chmod 644 /system/priv-app/MinervaBridge/MinervaBridge.apk
   adb reboot
   ```

3. **Grant Runtime Permissions**:
   After reboot, open the app and grant all requested permissions.

### Verify Permissions

```bash
adb shell dumpsys package com.minerva.bridge | grep -A 20 "granted=true"
```

Look for `CAPTURE_AUDIO_OUTPUT: granted=true`.

## Configuration

Open the app and configure:

1. **Server URL**: WebSocket URL of your Minerva server (e.g., `ws://192.168.1.100:8080/ws/phone`)
2. **Auto-connect**: Automatically connect when service starts
3. **Auto-answer**: Automatically answer incoming calls

## Usage

1. Start the service (tap "Start Service")
2. Connect to server (tap "Connect" or enable auto-connect)
3. When a call comes in:
   - App notifies server with caller info
   - Audio is streamed bidirectionally
   - AI responses are played into the call
4. Server can send commands to control the call

## Troubleshooting

### Service not starting

- Check if battery optimization is disabled for the app
- Verify all permissions are granted

### Cannot capture call audio

- Ensure device is rooted
- Verify Magisk module is installed and active
- Check if app is installed as priv-app
- Some ROMs have additional restrictions

### WebSocket connection failing

- Verify server URL is correct
- Ensure device and server are on same network
- Check server logs for connection errors

### Audio playback issues

- Check volume levels
- Verify audio format compatibility
- Look for errors in logcat: `adb logcat -s AudioBridge MinervaService`

## Logs

View app logs:
```bash
adb logcat -s MinervaService CallHandler AudioBridge MainActivity
```

## Security Considerations

- The app requires significant permissions (call access, audio recording)
- WebSocket connection should use WSS (TLS) in production
- Server should authenticate devices via X-Device-ID header
- Consider implementing additional authentication mechanisms

## License

Part of the Minerva project.
