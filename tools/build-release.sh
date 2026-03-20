#!/bin/bash

# build-release.sh - Build script for Irrlicht daemon
set -e

# Read version from version.json (single source of truth)
VERSION=$(python3 -c "import json; print(json.load(open('version.json'))['version'])")
BUILD_DIR="build"
DAEMON_NAME="irrlichtd"

echo "Building Irrlicht v$VERSION"
echo "============================================="

# Clean build directory
rm -rf $BUILD_DIR
mkdir -p $BUILD_DIR

# Build for macOS (both architectures)
echo "Building for macOS..."

# macOS Apple Silicon (arm64)
echo "  Building macOS arm64..."
cd core
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${DAEMON_NAME}-darwin-arm64" ./cmd/irrlichtd/

# macOS Intel (amd64)
echo "  Building macOS amd64..."
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../$BUILD_DIR/${DAEMON_NAME}-darwin-amd64" ./cmd/irrlichtd/

cd ..

# Create universal macOS binary
echo "  Creating universal macOS binary..."
lipo -create -output "$BUILD_DIR/${DAEMON_NAME}-darwin-universal" \
    "$BUILD_DIR/${DAEMON_NAME}-darwin-arm64" \
    "$BUILD_DIR/${DAEMON_NAME}-darwin-amd64"

# Calculate checksums (files only — build dir may contain Irrlicht.app bundle directory)
echo "Calculating checksums..."
cd $BUILD_DIR
find . -maxdepth 1 -type f ! -name 'checksums.sha256' | sort | xargs shasum -a 256 > checksums.sha256
cd ..

# Show results
echo ""
echo "Build completed successfully!"
echo ""
echo "Built artifacts:"
ls -la $BUILD_DIR/

echo ""
echo "Binary info:"
file $BUILD_DIR/${DAEMON_NAME}-darwin-universal
echo ""

echo "Checksums:"
cat $BUILD_DIR/checksums.sha256

echo ""
echo "Testing universal binaries..."
if $BUILD_DIR/${DAEMON_NAME}-darwin-universal --version; then
    echo "irrlichtd universal binary works!"
else
    echo "irrlichtd universal binary test failed"
    exit 1
fi

echo ""
echo "Release artifacts ready in $BUILD_DIR/"
echo "   Daemon binary: ${DAEMON_NAME}-darwin-universal"
