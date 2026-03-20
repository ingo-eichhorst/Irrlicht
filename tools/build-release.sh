#!/bin/bash

# build-release.sh - Cross-platform build script for Irrlicht hook receiver
set -e

# Read version from version.json (single source of truth)
VERSION=$(python3 -c "import json; print(json.load(open('version.json'))['version'])")
BUILD_DIR="build"
BINARY_NAME="irrlicht-hook"
SHIM_NAME="irrlicht-shim"
AIDER_TAIL_NAME="irrlicht-aider-tail"
COPILOT_HOOK_NAME="irrlicht-hook-copilot"
CURSOR_HOOK_NAME="irrlicht-hook-cursor"

echo "🏗️  Building Irrlicht Hook Receiver v$VERSION"
echo "============================================="

# Clean build directory
rm -rf $BUILD_DIR
mkdir -p $BUILD_DIR

# Sync web frontend into Go embed directory
echo "Syncing web frontend..."
cp frontend/web/index.html core/cmd/irrlichtd/ui/index.html

# Build for macOS (both architectures)
echo "Building for macOS..."

# macOS Apple Silicon (arm64)
echo "  Building macOS arm64..."
cd core
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${BINARY_NAME}-darwin-arm64" ./cmd/irrlicht-hook/
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${SHIM_NAME}-darwin-arm64" ./cmd/irrlicht-shim/
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${AIDER_TAIL_NAME}-darwin-arm64" ./cmd/irrlicht-aider-tail/
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${COPILOT_HOOK_NAME}-darwin-arm64" ./cmd/irrlicht-hook-copilot/
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${CURSOR_HOOK_NAME}-darwin-arm64" ./cmd/irrlicht-hook-cursor/

# macOS Intel (amd64)
echo "  Building macOS amd64..."
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${BINARY_NAME}-darwin-amd64" ./cmd/irrlicht-hook/
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${SHIM_NAME}-darwin-amd64" ./cmd/irrlicht-shim/
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${AIDER_TAIL_NAME}-darwin-amd64" ./cmd/irrlicht-aider-tail/
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${COPILOT_HOOK_NAME}-darwin-amd64" ./cmd/irrlicht-hook-copilot/
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${CURSOR_HOOK_NAME}-darwin-amd64" ./cmd/irrlicht-hook-cursor/

cd ..

# Create universal macOS binary
echo "  Creating universal macOS binary..."
lipo -create -output "$BUILD_DIR/${BINARY_NAME}-darwin-universal" \
    "$BUILD_DIR/${BINARY_NAME}-darwin-arm64" \
    "$BUILD_DIR/${BINARY_NAME}-darwin-amd64"
lipo -create -output "$BUILD_DIR/${SHIM_NAME}-darwin-universal" \
    "$BUILD_DIR/${SHIM_NAME}-darwin-arm64" \
    "$BUILD_DIR/${SHIM_NAME}-darwin-amd64"
lipo -create -output "$BUILD_DIR/${AIDER_TAIL_NAME}-darwin-universal" \
    "$BUILD_DIR/${AIDER_TAIL_NAME}-darwin-arm64" \
    "$BUILD_DIR/${AIDER_TAIL_NAME}-darwin-amd64"
lipo -create -output "$BUILD_DIR/${COPILOT_HOOK_NAME}-darwin-universal" \
    "$BUILD_DIR/${COPILOT_HOOK_NAME}-darwin-arm64" \
    "$BUILD_DIR/${COPILOT_HOOK_NAME}-darwin-amd64"
lipo -create -output "$BUILD_DIR/${CURSOR_HOOK_NAME}-darwin-universal" \
    "$BUILD_DIR/${CURSOR_HOOK_NAME}-darwin-arm64" \
    "$BUILD_DIR/${CURSOR_HOOK_NAME}-darwin-amd64"

# Build installer tools (macOS only — these manage platform-specific config files)
echo "  Building installer tools (macOS)..."
cd tools/copilot-hooks-merger
GOOS=darwin GOARCH=arm64 go build -o "../../$BUILD_DIR/copilot-hooks-merger-darwin-arm64" .
GOOS=darwin GOARCH=amd64 go build -o "../../$BUILD_DIR/copilot-hooks-merger-darwin-amd64" .
cd ../cursor-hooks-merger
GOOS=darwin GOARCH=arm64 go build -o "../../$BUILD_DIR/cursor-hooks-merger-darwin-arm64" .
GOOS=darwin GOARCH=amd64 go build -o "../../$BUILD_DIR/cursor-hooks-merger-darwin-amd64" .
cd ../..
lipo -create -output "$BUILD_DIR/copilot-hooks-merger-darwin-universal" \
    "$BUILD_DIR/copilot-hooks-merger-darwin-arm64" \
    "$BUILD_DIR/copilot-hooks-merger-darwin-amd64"
lipo -create -output "$BUILD_DIR/cursor-hooks-merger-darwin-universal" \
    "$BUILD_DIR/cursor-hooks-merger-darwin-arm64" \
    "$BUILD_DIR/cursor-hooks-merger-darwin-amd64"

# Build for other platforms (for future distribution)
echo "Building for Linux..."
cd core
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${BINARY_NAME}-linux-amd64" ./cmd/irrlicht-hook/
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${SHIM_NAME}-linux-amd64" ./cmd/irrlicht-shim/
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${AIDER_TAIL_NAME}-linux-amd64" ./cmd/irrlicht-aider-tail/
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${COPILOT_HOOK_NAME}-linux-amd64" ./cmd/irrlicht-hook-copilot/
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${CURSOR_HOOK_NAME}-linux-amd64" ./cmd/irrlicht-hook-cursor/
cd ..

echo "Building for Windows..."
cd core
GOOS=windows GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${BINARY_NAME}-windows-amd64.exe" ./cmd/irrlicht-hook/
GOOS=windows GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${SHIM_NAME}-windows-amd64.exe" ./cmd/irrlicht-shim/
GOOS=windows GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${AIDER_TAIL_NAME}-windows-amd64.exe" ./cmd/irrlicht-aider-tail/
GOOS=windows GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${COPILOT_HOOK_NAME}-windows-amd64.exe" ./cmd/irrlicht-hook-copilot/
GOOS=windows GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${CURSOR_HOOK_NAME}-windows-amd64.exe" ./cmd/irrlicht-hook-cursor/
cd ..

# Build macOS installer package
echo ""
echo "Building macOS installer package..."
./platform/macos/create-installer-package.sh --version "$VERSION"

# Calculate checksums (files only — build dir may contain Irrlicht.app bundle directory)
echo "Calculating checksums..."
cd $BUILD_DIR
find . -maxdepth 1 -type f ! -name 'checksums.sha256' | sort | xargs shasum -a 256 > checksums.sha256
cd ..

# Show results
echo ""
echo "✅ Build completed successfully!"
echo ""
echo "📦 Built artifacts:"
ls -la $BUILD_DIR/

echo ""
echo "🔍 Binary info:"
file $BUILD_DIR/${BINARY_NAME}-darwin-universal
echo ""

echo "📋 Checksums:"
cat $BUILD_DIR/checksums.sha256

echo ""
echo "🧪 Testing universal binaries..."
if $BUILD_DIR/${BINARY_NAME}-darwin-universal --version; then
    echo "✅ irrlicht-hook universal binary works!"
else
    echo "❌ irrlicht-hook universal binary test failed"
    exit 1
fi
if $BUILD_DIR/${SHIM_NAME}-darwin-universal --version; then
    echo "✅ irrlicht-shim universal binary works!"
else
    echo "❌ irrlicht-shim universal binary test failed"
    exit 1
fi
if $BUILD_DIR/${COPILOT_HOOK_NAME}-darwin-universal --version; then
    echo "✅ irrlicht-hook-copilot universal binary works!"
else
    echo "❌ irrlicht-hook-copilot universal binary test failed"
    exit 1
fi
if $BUILD_DIR/${CURSOR_HOOK_NAME}-darwin-universal --version; then
    echo "✅ irrlicht-hook-cursor universal binary works!"
else
    echo "❌ irrlicht-hook-cursor universal binary test failed"
    exit 1
fi

echo ""
echo "🎉 Release artifacts ready in $BUILD_DIR/"
echo "   Hook binary:            ${BINARY_NAME}-darwin-universal"
echo "   Shim binary:            ${SHIM_NAME}-darwin-universal"
echo "   Aider tail:             ${AIDER_TAIL_NAME}-darwin-universal"
echo "   Copilot hook binary:    ${COPILOT_HOOK_NAME}-darwin-universal"
echo "   Cursor hook binary:     ${CURSOR_HOOK_NAME}-darwin-universal"
echo "   Copilot hooks merger:   copilot-hooks-merger-darwin-universal"
echo "   Cursor hooks merger:    cursor-hooks-merger-darwin-universal"
echo "   Installer pkg:          Irrlicht-v${VERSION}.pkg"
