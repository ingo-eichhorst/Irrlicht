#!/bin/bash
# create-installer-package.sh — Build Irrlicht-vX.X.X.pkg for macOS distribution
#
# Bundles irrlicht-hook, settings-merger, and Irrlicht.app into a signed/unsigned
# .pkg installer using pkgbuild + productbuild.
#
# Usage:
#   ./platform/macos/create-installer-package.sh [--version VERSION] [--sign IDENTITY]
#
# Outputs:
#   build/Irrlicht-vVERSION.pkg
#
# Requirements:
#   - macOS with Xcode Command Line Tools (pkgbuild, productbuild)
#   - Go toolchain (for building irrlicht-hook and settings-merger)
#   - Swift toolchain (for building Irrlicht.app)

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
VERSION="1.0.0"
SIGN_IDENTITY=""          # e.g. "Developer ID Installer: Your Name (TEAMID)"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BUILD_DIR="$REPO_ROOT/build"
INSTALLER_DIR="$REPO_ROOT/platform/macos"
STAGING_DIR="$BUILD_DIR/installer-staging"
PKG_SCRIPTS_DIR="$BUILD_DIR/installer-scripts"
COMPONENT_PKG="$BUILD_DIR/irrlicht-component.pkg"
BUNDLE_ID="com.anthropic.irrlicht"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --version)
            VERSION="$2"; shift 2 ;;
        --sign)
            SIGN_IDENTITY="$2"; shift 2 ;;
        *)
            echo "Unknown option: $1" >&2
            echo "Usage: $0 [--version VERSION] [--sign IDENTITY]" >&2
            exit 1 ;;
    esac
done

FINAL_PKG="$BUILD_DIR/Irrlicht-v${VERSION}.pkg"

echo "📦  Irrlicht Installer Builder v$VERSION"
echo "========================================"
echo "  Repo:    $REPO_ROOT"
echo "  Output:  $FINAL_PKG"
[[ -n "$SIGN_IDENTITY" ]] && echo "  Signing: $SIGN_IDENTITY"
echo ""

# ---------------------------------------------------------------------------
# Prerequisite checks
# ---------------------------------------------------------------------------
for tool in pkgbuild productbuild go swift lipo; do
    if ! command -v "$tool" &>/dev/null; then
        echo "❌  Required tool not found: $tool" >&2
        exit 1
    fi
done

# ---------------------------------------------------------------------------
# Build components
# ---------------------------------------------------------------------------
echo "🔨  Building irrlicht-hook (universal binary)..."
cd "$REPO_ROOT/core"
GOOS=darwin GOARCH=arm64 go build \
    -ldflags "-X main.Version=$VERSION" \
    -o "$BUILD_DIR/irrlicht-hook-darwin-arm64" \
    ./cmd/irrlicht-hook/
GOOS=darwin GOARCH=amd64 go build \
    -ldflags "-X main.Version=$VERSION" \
    -o "$BUILD_DIR/irrlicht-hook-darwin-amd64" \
    ./cmd/irrlicht-hook/
lipo -create -output "$BUILD_DIR/irrlicht-hook-darwin-universal" \
    "$BUILD_DIR/irrlicht-hook-darwin-arm64" \
    "$BUILD_DIR/irrlicht-hook-darwin-amd64"
echo "   ✅  irrlicht-hook built"

echo "🔨  Building settings-merger (universal binary)..."
cd "$REPO_ROOT/tools/settings-merger"
GOOS=darwin GOARCH=arm64 go build \
    -ldflags "-X main.Version=$VERSION" \
    -o "$BUILD_DIR/settings-merger-darwin-arm64" \
    .
GOOS=darwin GOARCH=amd64 go build \
    -ldflags "-X main.Version=$VERSION" \
    -o "$BUILD_DIR/settings-merger-darwin-amd64" \
    .
lipo -create -output "$BUILD_DIR/settings-merger-darwin-universal" \
    "$BUILD_DIR/settings-merger-darwin-arm64" \
    "$BUILD_DIR/settings-merger-darwin-amd64"
echo "   ✅  settings-merger built"

echo "🔨  Building Irrlicht.app (release)..."
cd "$REPO_ROOT/Irrlicht.app"
swift build -c release 2>&1 | tail -5
echo "   ✅  Irrlicht.app built"

# ---------------------------------------------------------------------------
# Assemble .app bundle
# ---------------------------------------------------------------------------
echo "📁  Assembling Irrlicht.app bundle..."
APP_BUNDLE="$BUILD_DIR/Irrlicht.app"
APP_MACOS="$APP_BUNDLE/Contents/MacOS"
APP_RESOURCES="$APP_BUNDLE/Contents/Resources"
rm -rf "$APP_BUNDLE"
mkdir -p "$APP_MACOS" "$APP_RESOURCES"

# Copy executable (SPM places it in .build/release/<target-name>)
SWIFT_EXECUTABLE="$REPO_ROOT/Irrlicht.app/.build/release/Irrlicht"
if [[ ! -f "$SWIFT_EXECUTABLE" ]]; then
    echo "❌  Swift build output not found: $SWIFT_EXECUTABLE" >&2
    exit 1
fi
cp "$SWIFT_EXECUTABLE" "$APP_MACOS/Irrlicht"
chmod +x "$APP_MACOS/Irrlicht"

# Write Info.plist with resolved variables
cat > "$APP_BUNDLE/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleExecutable</key>
    <string>Irrlicht</string>
    <key>CFBundleIdentifier</key>
    <string>com.anthropic.irrlicht.app</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>Irrlicht</string>
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
    <string>Copyright © 2024 Anthropic. All rights reserved.</string>
    <key>NSPrincipalClass</key>
    <string>NSApplication</string>
</dict>
</plist>
PLIST
echo "   ✅  App bundle assembled: $APP_BUNDLE"

# ---------------------------------------------------------------------------
# Stage installation layout
# ---------------------------------------------------------------------------
echo "📁  Staging installation root..."
rm -rf "$STAGING_DIR"
mkdir -p \
    "$STAGING_DIR/usr/local/bin" \
    "$STAGING_DIR/Applications" \
    "$STAGING_DIR/Library/Application Support/Irrlicht"

# Hook receiver → /usr/local/bin/irrlicht-hook
cp "$BUILD_DIR/irrlicht-hook-darwin-universal" "$STAGING_DIR/usr/local/bin/irrlicht-hook"
chmod 755 "$STAGING_DIR/usr/local/bin/irrlicht-hook"

# Settings merger → /usr/local/bin/irrlicht-settings-merger
cp "$BUILD_DIR/settings-merger-darwin-universal" "$STAGING_DIR/usr/local/bin/irrlicht-settings-merger"
chmod 755 "$STAGING_DIR/usr/local/bin/irrlicht-settings-merger"

# Uninstall script → /usr/local/bin/irrlicht-uninstall.sh
cp "$INSTALLER_DIR/uninstall.sh" "$STAGING_DIR/usr/local/bin/irrlicht-uninstall.sh"
chmod 755 "$STAGING_DIR/usr/local/bin/irrlicht-uninstall.sh"

# App bundle → /Applications/Irrlicht.app
cp -R "$APP_BUNDLE" "$STAGING_DIR/Applications/"
chmod -R 755 "$STAGING_DIR/Applications/Irrlicht.app"

echo "   ✅  Staging complete"

# ---------------------------------------------------------------------------
# Copy installer scripts
# ---------------------------------------------------------------------------
rm -rf "$PKG_SCRIPTS_DIR"
mkdir -p "$PKG_SCRIPTS_DIR"
cp "$INSTALLER_DIR/scripts/preinstall"  "$PKG_SCRIPTS_DIR/preinstall"
cp "$INSTALLER_DIR/scripts/postinstall" "$PKG_SCRIPTS_DIR/postinstall"
chmod +x "$PKG_SCRIPTS_DIR/preinstall" "$PKG_SCRIPTS_DIR/postinstall"

# ---------------------------------------------------------------------------
# Build component package with pkgbuild
# ---------------------------------------------------------------------------
echo "📦  Building component package..."
pkgbuild \
    --root "$STAGING_DIR" \
    --identifier "$BUNDLE_ID" \
    --version "$VERSION" \
    --scripts "$PKG_SCRIPTS_DIR" \
    --install-location "/" \
    "$COMPONENT_PKG"
echo "   ✅  Component package: $COMPONENT_PKG"

# ---------------------------------------------------------------------------
# Build final product package with productbuild
# ---------------------------------------------------------------------------
echo "📦  Building final product package..."

# Substitute VERSION into distribution.xml
DISTRIBUTION_XML="$BUILD_DIR/distribution.xml"
sed "s/{{VERSION}}/$VERSION/g; s/{{BUNDLE_ID}}/$BUNDLE_ID/g" \
    "$INSTALLER_DIR/distribution.xml" > "$DISTRIBUTION_XML"

# Assemble resources directory (welcome screen, license)
RESOURCES_DIR="$BUILD_DIR/installer-resources"
rm -rf "$RESOURCES_DIR"
mkdir -p "$RESOURCES_DIR"
cp "$INSTALLER_DIR/welcome.html" "$RESOURCES_DIR/welcome.html"
[[ -f "$REPO_ROOT/LICENSE" ]] && cp "$REPO_ROOT/LICENSE" "$RESOURCES_DIR/LICENSE"

if [[ -n "$SIGN_IDENTITY" ]]; then
    productbuild \
        --distribution "$DISTRIBUTION_XML" \
        --package-path "$BUILD_DIR" \
        --resources "$RESOURCES_DIR" \
        --sign "$SIGN_IDENTITY" \
        "$FINAL_PKG"
else
    productbuild \
        --distribution "$DISTRIBUTION_XML" \
        --package-path "$BUILD_DIR" \
        --resources "$RESOURCES_DIR" \
        "$FINAL_PKG"
fi

# ---------------------------------------------------------------------------
# Cleanup intermediate files
# ---------------------------------------------------------------------------
rm -f "$COMPONENT_PKG"
rm -f "$DISTRIBUTION_XML"
rm -rf "$STAGING_DIR" "$PKG_SCRIPTS_DIR" "$RESOURCES_DIR"
rm -f "$BUILD_DIR/irrlicht-hook-darwin-arm64" "$BUILD_DIR/irrlicht-hook-darwin-amd64"
rm -f "$BUILD_DIR/settings-merger-darwin-arm64" "$BUILD_DIR/settings-merger-darwin-amd64"

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
PKG_SIZE=$(du -sh "$FINAL_PKG" | cut -f1)
echo ""
echo "✅  Installer package ready!"
echo ""
echo "   📦  $FINAL_PKG  ($PKG_SIZE)"
echo ""
echo "To install:"
echo "   sudo installer -pkg \"$FINAL_PKG\" -target /"
echo ""
echo "To distribute:"
[[ -n "$SIGN_IDENTITY" ]] && echo "   ✅  Package is signed with: $SIGN_IDENTITY"
[[ -z "$SIGN_IDENTITY" ]] && echo "   ⚠️   Package is unsigned. Pass --sign 'Developer ID Installer: ...' to sign."
