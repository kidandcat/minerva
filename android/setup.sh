#!/bin/bash
# Minerva Bridge - Automated Termux Setup Script
# Run this on your Android device via Termux

set -e

echo "========================================="
echo "Minerva Bridge - Termux Setup"
echo "========================================="

# Check if running on Android/Termux
if [ ! -d "/data/data/com.termux" ]; then
    echo "This script should be run in Termux on an Android device"
    echo "Or use it as a reference for manual setup"
fi

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

print_status() {
    echo -e "${GREEN}[+]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[!]${NC} $1"
}

print_error() {
    echo -e "${RED}[-]${NC} $1"
}

# Update Termux packages
print_status "Updating Termux packages..."
pkg update -y
pkg upgrade -y

# Install required packages
print_status "Installing build tools..."
pkg install -y openjdk-17 gradle git

# Set JAVA_HOME
export JAVA_HOME=$PREFIX/opt/openjdk
export PATH=$JAVA_HOME/bin:$PATH

print_status "Java version:"
java -version

# Clone or update the project
PROJECT_DIR="$HOME/minerva-bridge"

if [ -d "$PROJECT_DIR" ]; then
    print_status "Updating existing project..."
    cd "$PROJECT_DIR"
    git pull
else
    print_status "Cloning project..."
    # If this is part of a larger repo, adjust the clone command
    mkdir -p "$PROJECT_DIR"
    cd "$PROJECT_DIR"
    # Copy files from wherever they are (adjust path as needed)
fi

print_status "Project directory: $PROJECT_DIR"

# Create local.properties if needed
if [ ! -f "local.properties" ]; then
    echo "sdk.dir=$PREFIX" > local.properties
    print_status "Created local.properties"
fi

# Build the project
print_status "Building APK..."
./gradlew assembleDebug

APK_PATH="app/build/outputs/apk/debug/app-debug.apk"

if [ -f "$APK_PATH" ]; then
    print_status "Build successful!"
    echo ""
    echo "APK location: $PROJECT_DIR/$APK_PATH"
    echo ""
    print_warning "To install, you need to:"
    echo "1. Enable 'Install from unknown sources' for Termux"
    echo "2. Run: termux-open $APK_PATH"
    echo "   Or: cp $APK_PATH /sdcard/Download/"
    echo ""
    echo "For privileged installation (required for call audio capture):"
    echo "3. Install Magisk module from android/magisk/"
    echo "4. Move APK to /system/priv-app/MinervaBridge/"
    echo "5. Reboot"
else
    print_error "Build failed!"
    exit 1
fi

# Root setup instructions
echo ""
echo "========================================="
echo "ROOT SETUP INSTRUCTIONS"
echo "========================================="
echo ""
echo "If you have Magisk root:"
echo ""
echo "1. Create Magisk module ZIP:"
echo "   cd android/magisk && zip -r minerva-magisk.zip *"
echo ""
echo "2. Install via Magisk Manager -> Modules"
echo ""
echo "3. Move APK to priv-app (via root shell):"
echo "   su"
echo "   mkdir -p /system/priv-app/MinervaBridge"
echo "   cp $APK_PATH /system/priv-app/MinervaBridge/base.apk"
echo "   chmod 644 /system/priv-app/MinervaBridge/base.apk"
echo "   reboot"
echo ""
echo "4. After reboot, grant remaining runtime permissions in Settings"
echo ""
echo "========================================="
print_status "Setup script complete!"
