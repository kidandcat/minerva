#!/system/bin/sh
# Minerva Phone Bridge - Magisk Module Installer
# Grants system-level permissions for audio capture during calls

PACKAGE="com.minerva.bridge"

ui_print "- Installing Minerva Phone Bridge Permissions"

# Create priv-app directory for the APK
ui_print "- Creating priv-app directory..."
mkdir -p "$MODPATH/system/priv-app/MinervaBridge"
set_perm_recursive "$MODPATH/system/priv-app/MinervaBridge" 0 0 0755 0644

# Copy existing APK from system if already installed as user app
# (The install script will push the APK here before installing the module)
APK_PATH=$(pm path "$PACKAGE" 2>/dev/null | head -1 | sed 's/package://')
if [ -n "$APK_PATH" ] && [ -f "$APK_PATH" ]; then
    ui_print "- Found existing APK at $APK_PATH"
    cp "$APK_PATH" "$MODPATH/system/priv-app/MinervaBridge/MinervaBridge.apk"
    ui_print "- Copied APK to priv-app"
fi

# Create privapp-permissions XML
ui_print "- Creating privileged permissions whitelist..."
mkdir -p "$MODPATH/system/etc/permissions"

cat > "$MODPATH/system/etc/permissions/privapp-permissions-minerva.xml" << 'XMLEOF'
<?xml version="1.0" encoding="utf-8"?>
<permissions>
    <privapp-permissions package="com.minerva.bridge">
        <permission name="android.permission.CAPTURE_AUDIO_OUTPUT"/>
        <permission name="android.permission.BIND_INCALL_SERVICE"/>
        <permission name="android.permission.MODIFY_PHONE_STATE"/>
    </privapp-permissions>
</permissions>
XMLEOF

# Set proper permissions
ui_print "- Setting file permissions..."
set_perm_recursive "$MODPATH/system" 0 0 0755 0644
set_perm "$MODPATH/system/etc/permissions/privapp-permissions-minerva.xml" 0 0 0644

# If the APK was copied, set its permissions too
if [ -f "$MODPATH/system/priv-app/MinervaBridge/MinervaBridge.apk" ]; then
    set_perm "$MODPATH/system/priv-app/MinervaBridge/MinervaBridge.apk" 0 0 0644
fi

ui_print "- Minerva Phone Bridge permissions module installed"
ui_print "- Reboot to activate the module"
ui_print ""
