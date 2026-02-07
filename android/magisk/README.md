# Minerva Bridge Magisk Module

This Magisk module grants the CAPTURE_AUDIO_OUTPUT permission to the Minerva Bridge app, enabling bidirectional call audio capture.

## What it does

- Creates a privapp-permissions XML file that grants system-level audio permissions
- Enables the app to capture call audio (both uplink and downlink)

## Installation

### Method 1: Magisk Manager (Recommended)

1. Zip this folder:
   ```bash
   cd android/magisk
   zip -r minerva-bridge-magisk.zip *
   ```

2. Transfer the ZIP to your device

3. Open Magisk Manager -> Modules -> Install from storage

4. Select the ZIP file and reboot

### Method 2: Manual Installation

1. Copy the module folder to `/data/adb/modules/minerva-bridge-permissions/`

2. Reboot your device

## After Module Installation

The module alone is not enough. You also need to:

### Option A: Install as Privileged App (Recommended)

1. Build the APK:
   ```bash
   cd android
   ./gradlew assembleRelease
   ```

2. Install as priv-app via ADB (requires root):
   ```bash
   adb root
   adb remount
   adb push app/build/outputs/apk/release/app-release.apk /system/priv-app/MinervaBridge/MinervaBridge.apk
   adb shell chmod 644 /system/priv-app/MinervaBridge/MinervaBridge.apk
   adb reboot
   ```

### Option B: Use pm grant (simpler but may not persist)

1. Install the APK normally:
   ```bash
   adb install app/build/outputs/apk/release/app-release.apk
   ```

2. Grant permission via ADB root:
   ```bash
   adb shell su -c "pm grant com.minerva.bridge android.permission.CAPTURE_AUDIO_OUTPUT"
   ```

## Verification

Check if the permission is granted:
```bash
adb shell dumpsys package com.minerva.bridge | grep CAPTURE_AUDIO_OUTPUT
```

If you see `CAPTURE_AUDIO_OUTPUT: granted=true`, the setup is complete.

## Troubleshooting

### Permission not granted after reboot

Make sure the app is installed in `/system/priv-app/` not `/data/app/`. Regular apps cannot receive privileged permissions.

### Audio capture still not working

1. Check if SELinux is enforcing:
   ```bash
   adb shell getenforce
   ```

2. Try setting SELinux to permissive (temporarily):
   ```bash
   adb shell su -c "setenforce 0"
   ```

3. Some ROMs may have additional restrictions on call audio capture.

### Fallback: Use VOICE_COMMUNICATION

If VOICE_CALL audio source doesn't work, the app will automatically fall back to VOICE_COMMUNICATION or MIC sources. Note that these may only capture one direction of the call.
