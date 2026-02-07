#!/system/bin/sh
# Minerva Bridge Magisk Module
# Grants system-level audio capture permissions

MODDIR=${0%/*}

ui_print "- Installing Minerva Bridge Permissions"

# Package name for the app
PACKAGE="com.minerva.bridge"

# Create the permissions XML directory
mkdir -p "$MODDIR/system/etc/permissions"

# Create the permissions file
cat > "$MODDIR/system/etc/permissions/privapp-permissions-minerva.xml" << 'EOF'
<?xml version="1.0" encoding="utf-8"?>
<permissions>
    <privapp-permissions package="com.minerva.bridge">
        <permission name="android.permission.CAPTURE_AUDIO_OUTPUT"/>
        <permission name="android.permission.RECORD_AUDIO"/>
        <permission name="android.permission.MODIFY_AUDIO_SETTINGS"/>
        <permission name="android.permission.READ_PHONE_STATE"/>
        <permission name="android.permission.ANSWER_PHONE_CALLS"/>
        <permission name="android.permission.CALL_PHONE"/>
        <permission name="android.permission.READ_CALL_LOG"/>
    </privapp-permissions>
</permissions>
EOF

# If the app is installed as a user app, we need to move it to priv-app
# This is typically done manually after installing the APK

ui_print "- Permissions XML created"
ui_print ""
ui_print "IMPORTANT: For full functionality, you need to:"
ui_print "1. Install the Minerva Bridge APK"
ui_print "2. Move the APK to /system/priv-app/MinervaBridge/"
ui_print "   Or reinstall via ADB with -g flag"
ui_print "3. Reboot your device"
ui_print ""
ui_print "- Installation complete"
