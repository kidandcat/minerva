#!/bin/bash
set -e

echo "=== Building Minerva for Android ==="

# Check for gomobile
if ! command -v gomobile &> /dev/null; then
    echo "Installing gomobile..."
    go install golang.org/x/mobile/cmd/gomobile@latest
    gomobile init
fi

# Build AAR
echo "Building mobile.aar..."
gomobile bind -target=android -androidapi 24 -o android-app/app/libs/mobile.aar ./mobile

echo "mobile.aar created at android-app/app/libs/mobile.aar"

# Instructions
echo ""
echo "=== Build complete ==="
echo ""
echo "To build the APK:"
echo "  cd android-app"
echo "  ./gradlew assembleDebug"
echo ""
echo "Or open android-app/ in Android Studio"
