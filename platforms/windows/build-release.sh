#!/bin/bash

# build-release.sh - Cross-compile irrlichd + Windows tray app and build NSIS
# installers for Windows amd64 and arm64. Runs from Linux or macOS — no
# Windows runner needed because Go cross-compiles cleanly and NSIS is
# available on both via Homebrew (mac) and apt (linux).
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$ROOT_DIR"

VERSION=$(python3 -c "import json; print(json.load(open('version.json'))['version'])")
BUILD_DIR=".build/windows"
DAEMON_NAME="irrlichd"
APP_NAME="irrlicht"
ARCHES="${ARCHES:-amd64 arm64}"

echo "Building Irrlicht for Windows v$VERSION (arches: $ARCHES)"
echo "============================================="

# Sync web frontend so the daemon's embedded UI is up to date.
mkdir -p core/cmd/irrlichd/ui
cp platforms/web/index.html core/cmd/irrlichd/ui/index.html

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

# Resolve makensis early so we fail fast if the runner is missing it.
if ! command -v makensis >/dev/null 2>&1; then
    echo "ERROR: makensis not found. Install with:"
    echo "  Linux:  sudo apt-get install -y nsis"
    echo "  macOS:  brew install nsis"
    exit 1
fi

build_arch() {
    local arch="$1"
    local stage="$BUILD_DIR/stage-$arch"
    rm -rf "$stage"
    mkdir -p "$stage"

    echo ""
    echo "→ windows/$arch"
    echo "  daemon..."
    GOOS=windows GOARCH="$arch" go build \
        -ldflags "-X main.Version=$VERSION" \
        -o "$stage/$DAEMON_NAME.exe" \
        ./core/cmd/irrlichd
    echo "  tray..."
    # -H windowsgui suppresses the console window for the GUI binary only.
    GOOS=windows GOARCH="$arch" go build \
        -ldflags "-X main.Version=$VERSION -H windowsgui" \
        -o "$stage/$APP_NAME.exe" \
        ./platforms/windows
    cp LICENSE "$stage/LICENSE.txt" 2>/dev/null || true

    echo "  installer..."
    makensis \
        -DVERSION="$VERSION" \
        -DARCH="$arch" \
        -DSTAGE_DIR="$stage" \
        -DOUT_DIR="$BUILD_DIR" \
        "$SCRIPT_DIR/installer/installer.nsi"
}

for arch in $ARCHES; do
    build_arch "$arch"
done

echo ""
echo "Calculating checksums..."
cd "$BUILD_DIR"
shasum -a 256 *.exe > checksums.sha256 2>/dev/null || true
cd "$ROOT_DIR"

echo ""
echo "============================================="
echo "Windows release v$VERSION build complete!"
echo ""
ls -la "$BUILD_DIR"/Irrlicht-*-setup.exe 2>/dev/null || true
echo ""
echo "Test install on a Win 11 VM:"
echo "  Setup:        $BUILD_DIR/Irrlicht-${VERSION}-windows-<arch>-setup.exe"
echo "  Silent:       setup.exe /S"
