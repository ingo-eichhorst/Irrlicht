#!/bin/bash

# build-release.sh - Build Irrlicht daemon + macOS app and create installer
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

# Read version from version.json (single source of truth)
VERSION=$(python3 -c "import json; print(json.load(open('version.json'))['version'])")
BUILD_DIR=".build"
DAEMON_NAME="irrlichd"
APP_NAME="Irrlicht"
BUNDLE_ID="com.anthropic.irrlicht"
PKG_NAME="Irrlicht-${VERSION}-mac-installer.pkg"

echo "Building Irrlicht v$VERSION"
echo "============================================="

# Clean build directory
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

# ── 1. Sync web frontend ──────────────────────────────────────────────
echo ""
echo "Syncing web frontend..."
mkdir -p core/cmd/irrlichd/ui
cp platforms/web/index.html core/cmd/irrlichd/ui/index.html

# ── 2. Build Go daemon (universal binary) ─────────────────────────────
echo ""
echo "Building daemon..."

echo "  arm64..."
cd core
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" \
    -o "../$BUILD_DIR/${DAEMON_NAME}-darwin-arm64" ./cmd/irrlichd/

echo "  amd64..."
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" \
    -o "../$BUILD_DIR/${DAEMON_NAME}-darwin-amd64" ./cmd/irrlichd/

cd ..

echo "  Creating universal binary..."
lipo -create -output "$BUILD_DIR/${DAEMON_NAME}-darwin-universal" \
    "$BUILD_DIR/${DAEMON_NAME}-darwin-arm64" \
    "$BUILD_DIR/${DAEMON_NAME}-darwin-amd64"

echo "  Testing..."
"$BUILD_DIR/${DAEMON_NAME}-darwin-universal" --version

# ── 3. Build Swift macOS app (.app bundle) ─────────────────────────────
echo ""
echo "Building macOS app..."
cd platforms/macos
swift build -c release --arch arm64 --arch x86_64 2>&1 | tail -5
cd "$ROOT_DIR"

SWIFT_BIN="platforms/macos/.build/apple/Products/Release/Irrlicht"
if [ ! -f "$SWIFT_BIN" ]; then
    # Fallback path for single-arch or different swift build layouts
    SWIFT_BIN=$(find platforms/macos/.build -name Irrlicht -type f -perm +111 | grep -i release | head -1)
fi

if [ -z "$SWIFT_BIN" ] || [ ! -f "$SWIFT_BIN" ]; then
    echo "ERROR: Could not find built Irrlicht binary"
    exit 1
fi
echo "  Built: $SWIFT_BIN"

# Create .app bundle
APP_BUNDLE="$BUILD_DIR/${APP_NAME}.app"
APP_CONTENTS="$APP_BUNDLE/Contents"
mkdir -p "$APP_CONTENTS/MacOS"
mkdir -p "$APP_CONTENTS/Resources"

cp "$SWIFT_BIN" "$APP_CONTENTS/MacOS/${APP_NAME}"

# Generate Info.plist with resolved variables
cat > "$APP_CONTENTS/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIconFile</key>
    <string></string>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>LSApplicationCategoryType</key>
    <string>public.app-category.developer-tools</string>
    <key>LSMinimumSystemVersion</key>
    <string>13.0</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSHumanReadableCopyright</key>
    <string>Copyright 2024 Anthropic. All rights reserved.</string>
    <key>NSPrincipalClass</key>
    <string>NSApplication</string>
</dict>
</plist>
PLIST

echo "  Created $APP_BUNDLE"

# ── 4. Create LaunchDaemon plist ───────────────────────────────────────
echo ""
echo "Creating LaunchAgent plist..."

LAUNCHAGENT_PLIST="$BUILD_DIR/${BUNDLE_ID}.daemon.plist"
cat > "$LAUNCHAGENT_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${BUNDLE_ID}.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/${DAEMON_NAME}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/irrlichd.out.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/irrlichd.err.log</string>
</dict>
</plist>
PLIST

# ── 5. Build installer .pkg ────────────────────────────────────────────
echo ""
echo "Building installer package..."

PKG_ROOT="$BUILD_DIR/pkg-root"
PKG_SCRIPTS="$BUILD_DIR/pkg-scripts"
rm -rf "$PKG_ROOT" "$PKG_SCRIPTS"

# Lay out installed file hierarchy
mkdir -p "$PKG_ROOT/usr/local/bin"
mkdir -p "$PKG_ROOT/Applications"
mkdir -p "$PKG_ROOT/Library/LaunchAgents"

cp "$BUILD_DIR/${DAEMON_NAME}-darwin-universal" "$PKG_ROOT/usr/local/bin/${DAEMON_NAME}"
chmod 755 "$PKG_ROOT/usr/local/bin/${DAEMON_NAME}"
cp -R "$APP_BUNDLE" "$PKG_ROOT/Applications/${APP_NAME}.app"
cp "$LAUNCHAGENT_PLIST" "$PKG_ROOT/Library/LaunchAgents/${BUNDLE_ID}.daemon.plist"

# Post-install script: load LaunchAgent for the installing user
mkdir -p "$PKG_SCRIPTS"
cat > "$PKG_SCRIPTS/postinstall" <<'SCRIPT'
#!/bin/bash
PLIST="/Library/LaunchAgents/com.anthropic.irrlicht.daemon.plist"
AGENT_LABEL="com.anthropic.irrlicht.daemon"

# Copy to user LaunchAgents and load
CURRENT_USER=$(stat -f "%Su" /dev/console)
USER_LA_DIR="/Users/$CURRENT_USER/Library/LaunchAgents"
mkdir -p "$USER_LA_DIR"
cp "$PLIST" "$USER_LA_DIR/"
chown "$CURRENT_USER" "$USER_LA_DIR/$(basename $PLIST)"

# Unload if already running, then load
su "$CURRENT_USER" -c "launchctl bootout gui/$(id -u $CURRENT_USER) $USER_LA_DIR/$(basename $PLIST)" 2>/dev/null || true
su "$CURRENT_USER" -c "launchctl bootstrap gui/$(id -u $CURRENT_USER) $USER_LA_DIR/$(basename $PLIST)" 2>/dev/null || true

exit 0
SCRIPT
chmod 755 "$PKG_SCRIPTS/postinstall"

# Build the component package
pkgbuild \
    --root "$PKG_ROOT" \
    --scripts "$PKG_SCRIPTS" \
    --identifier "$BUNDLE_ID" \
    --version "$VERSION" \
    --install-location "/" \
    "$BUILD_DIR/irrlicht-component.pkg"

# Build the product (installer with welcome text)
cat > "$BUILD_DIR/distribution.xml" <<DIST
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
    <title>Irrlicht v${VERSION}</title>
    <welcome language="en"><![CDATA[
        <h2>Irrlicht v${VERSION}</h2>
        <p>This installer will set up:</p>
        <ul>
            <li><strong>irrlichd</strong> — background daemon for monitoring AI coding sessions</li>
            <li><strong>Irrlicht.app</strong> — macOS menu bar UI</li>
        </ul>
        <p>The daemon starts automatically on login via LaunchAgent.</p>
    ]]></welcome>
    <options customize="never" require-scripts="false"/>
    <choices-outline>
        <line choice="default"/>
    </choices-outline>
    <choice id="default" title="Irrlicht">
        <pkg-ref id="${BUNDLE_ID}"/>
    </choice>
    <pkg-ref id="${BUNDLE_ID}" version="${VERSION}">#irrlicht-component.pkg</pkg-ref>
</installer-gui-script>
DIST

productbuild \
    --distribution "$BUILD_DIR/distribution.xml" \
    --package-path "$BUILD_DIR" \
    "$BUILD_DIR/$PKG_NAME"

echo "  Created $BUILD_DIR/$PKG_NAME"

# ── 6. Checksums ───────────────────────────────────────────────────────
echo ""
echo "Calculating checksums..."
cd "$BUILD_DIR"
shasum -a 256 "$PKG_NAME" ${DAEMON_NAME}-darwin-universal > checksums.sha256
cd ..

# ── 7. Summary ─────────────────────────────────────────────────────────
echo ""
echo "============================================="
echo "Release v$VERSION build complete!"
echo ""
echo "Installer:  $BUILD_DIR/$PKG_NAME"
echo "Daemon:     $BUILD_DIR/${DAEMON_NAME}-darwin-universal"
echo "App:        $BUILD_DIR/${APP_NAME}.app"
echo ""
echo "Checksums:"
cat "$BUILD_DIR/checksums.sha256"
echo ""
echo "Installs:"
echo "  /usr/local/bin/irrlichd"
echo "  /Applications/Irrlicht.app"
echo "  ~/Library/LaunchAgents/${BUNDLE_ID}.daemon.plist"
