#!/bin/bash

# build-release.sh - Cross-platform build script for Irrlicht hook receiver
set -e

VERSION="1.0.0"
BUILD_DIR="build"
BINARY_NAME="irrlicht-hook"

echo "🏗️  Building Irrlicht Hook Receiver v$VERSION"
echo "============================================="

# Clean build directory
rm -rf $BUILD_DIR
mkdir -p $BUILD_DIR

# Build for macOS (both architectures)
echo "Building for macOS..."

# macOS Apple Silicon (arm64)
echo "  Building macOS arm64..."
cd tools/irrlicht-hook
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" -o "../../$BUILD_DIR/${BINARY_NAME}-darwin-arm64" .

# macOS Intel (amd64) 
echo "  Building macOS amd64..."
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../../$BUILD_DIR/${BINARY_NAME}-darwin-amd64" .

cd ../../

# Create universal macOS binary
echo "  Creating universal macOS binary..."
lipo -create -output "$BUILD_DIR/${BINARY_NAME}-darwin-universal" \
    "$BUILD_DIR/${BINARY_NAME}-darwin-arm64" \
    "$BUILD_DIR/${BINARY_NAME}-darwin-amd64"

# Build for other platforms (for future distribution)
echo "Building for Linux..."
cd tools/irrlicht-hook
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../../$BUILD_DIR/${BINARY_NAME}-linux-amd64" .
cd ../../

echo "Building for Windows..."
cd tools/irrlicht-hook  
GOOS=windows GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" -o "../../$BUILD_DIR/${BINARY_NAME}-windows-amd64.exe" .
cd ../../

# Calculate checksums
echo "Calculating checksums..."
cd $BUILD_DIR
shasum -a 256 * > checksums.sha256
cd ..

# Show results
echo ""
echo "✅ Build completed successfully!"
echo ""
echo "📦 Built binaries:"
ls -la $BUILD_DIR/

echo ""
echo "🔍 Binary info:"
file $BUILD_DIR/${BINARY_NAME}-darwin-universal
echo ""

echo "📋 Checksums:"
cat $BUILD_DIR/checksums.sha256

echo ""
echo "🧪 Testing universal binary..."
if $BUILD_DIR/${BINARY_NAME}-darwin-universal --version; then
    echo "✅ Universal binary works!"
else
    echo "❌ Universal binary test failed"
    exit 1
fi

echo ""
echo "🎉 Release binaries ready in $BUILD_DIR/"
echo "   Primary target: ${BINARY_NAME}-darwin-universal"