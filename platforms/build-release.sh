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
BUNDLE_ID="io.irrlicht.app"
PKG_NAME="Irrlicht-${VERSION}-mac-installer.pkg"
DMG_NAME="Irrlicht-${VERSION}.dmg"

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

# Embed daemon inside the app bundle (single-artifact distribution)
cp "$BUILD_DIR/${DAEMON_NAME}-darwin-universal" "$APP_CONTENTS/MacOS/${DAEMON_NAME}"
chmod 755 "$APP_CONTENTS/MacOS/${DAEMON_NAME}"
echo "  Embedded daemon in app bundle"

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

# ── 4. Create DMG (primary distribution) ──────────────────────────────
echo ""
echo "Creating DMG..."

DMG_STAGING="$BUILD_DIR/dmg-staging"
rm -rf "$DMG_STAGING"
mkdir -p "$DMG_STAGING"

cp -R "$APP_BUNDLE" "$DMG_STAGING/"
ln -s /Applications "$DMG_STAGING/Applications"

hdiutil create -volname "Irrlicht $VERSION" \
    -srcfolder "$DMG_STAGING" \
    -ov -format UDZO \
    "$BUILD_DIR/$DMG_NAME"

rm -rf "$DMG_STAGING"
echo "  Created $BUILD_DIR/$DMG_NAME"

# ── 5. Create LaunchAgent plist (optional, for power users) ──────────
echo ""
echo "Creating LaunchAgent plist (optional — for running daemon without menu bar app)..."

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
        <string>/Applications/${APP_NAME}.app/Contents/MacOS/${DAEMON_NAME}</string>
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

# ── 6. Build installer .pkg (alternative distribution) ────────────────
echo ""
echo "Building installer package..."

PKG_ROOT="$BUILD_DIR/pkg-root"
PKG_SCRIPTS="$BUILD_DIR/pkg-scripts"
rm -rf "$PKG_ROOT" "$PKG_SCRIPTS"

# Lay out installed file hierarchy — only the app bundle, daemon lives inside it
mkdir -p "$PKG_ROOT/Applications"
cp -R "$APP_BUNDLE" "$PKG_ROOT/Applications/${APP_NAME}.app"

# Post-install script: add to Login Items (optional, lightweight)
mkdir -p "$PKG_SCRIPTS"
cat > "$PKG_SCRIPTS/postinstall" <<'SCRIPT'
#!/bin/bash
# The app manages its own daemon lifecycle — no LaunchAgent needed.
# Just open the app so the user sees it in the menu bar immediately.
CURRENT_USER=$(stat -f "%Su" /dev/console)
su "$CURRENT_USER" -c "open /Applications/Irrlicht.app" 2>/dev/null || true
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
        <p>This installer will set up <strong>Irrlicht.app</strong> in your Applications folder.</p>
        <p>The app includes the monitoring daemon — everything runs from a single application.
        Just drag to Applications or use this installer, then launch from your menu bar.</p>
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

# ── 7. Checksums ───────────────────────────────────────────────────────
echo ""
echo "Calculating checksums..."
cd "$BUILD_DIR"
shasum -a 256 "$DMG_NAME" "$PKG_NAME" ${DAEMON_NAME}-darwin-universal > checksums.sha256
cd ..

# ── 8. Summary ─────────────────────────────────────────────────────────
echo ""
echo "============================================="
echo "Release v$VERSION build complete!"
echo ""
echo "Distribution (pick one):"
echo "  DMG:        $BUILD_DIR/$DMG_NAME  (drag to Applications)"
echo "  Installer:  $BUILD_DIR/$PKG_NAME  (double-click to install)"
echo ""
echo "App bundle:   $BUILD_DIR/${APP_NAME}.app"
echo "  Contains:   ${APP_NAME} (menu bar UI) + ${DAEMON_NAME} (embedded daemon)"
echo ""
echo "Optional:"
echo "  LaunchAgent: $BUILD_DIR/${BUNDLE_ID}.daemon.plist"
echo "               (for running daemon without the menu bar app)"
echo ""
echo "Checksums:"
cat "$BUILD_DIR/checksums.sha256"
echo ""
echo "Install:"
echo "  1. Open $BUILD_DIR/$DMG_NAME"
echo "  2. Drag Irrlicht.app to Applications"
echo "  3. Launch Irrlicht from Applications — daemon starts automatically"
