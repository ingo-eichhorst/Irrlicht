#!/bin/bash

# build-release.sh - Build Irrlicht daemon + macOS app and create installer
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

# Read version from version.json (single source of truth). build-release.sh
# always uses the BARE base version — release artifacts must not carry git
# sha or .dirty markers in the binary's --version output, since the tagged
# commit is the source of truth for releases. Dev builds use the full
# computed string via tools/version.sh / tools/build-dev.sh.
VERSION=$("$SCRIPT_DIR/version.sh" --base)
BUILD_DIR=".build"
DAEMON_NAME="irrlichd"
APP_NAME="Irrlicht"
BUNDLE_ID="io.irrlicht.app"
PKG_NAME="Irrlicht-${VERSION}-mac-installer.pkg"
DMG_NAME="Irrlicht-${VERSION}.dmg"

echo "Building Irrlicht v$VERSION"
echo "============================================="

# ── Web payload (single source of truth for all three copy sites) ─────
# Every file the dashboard + Elfdans PWA need at runtime, copied into the
# darwin tarball, the linux tarballs, and the app bundle Resources alike.
# irrlicht.js statically imports its sibling modules, so a list holding only
# the entry files serves a dashboard that 404s its own module graph and
# cannot boot; sw.js and elfdans-icon.svg are referenced by nothing statically
# (runtime registration / manifest JSON), so forgetting them ships a silently
# push-less PWA (docs/mobile-notifications-arc42.md §8.7, risk 6). Kept
# complete by platforms/web/release-files.test.js, which derives the required
# set from index.html + the import graph and fails when this list falls
# behind.
WEB_FILES=(
    index.html
    irrlicht.css
    irrlicht.js
    elfdans-icon.svg
    elfdans.js
    elfdansDashboard.js
    elfdans.webmanifest
    collapsedGroups.js
    collapsedSet.js
    collapsedSummaries.js
    connectionProtocol.js
    domReconcile.js
    formatters.js
    historyTab.js
    permissionsWizard.js
    pair-handoff.js
    quotaChips.js
    sessionIdentity.js
    sw.js
)

copy_web_files() {
    local dest="$1"
    local f
    for f in "${WEB_FILES[@]}"; do
        cp "platforms/web/$f" "$dest/"
    done
}

# ── 0. Signing preflight ─────────────────────────────────────────────
# Fails in seconds, not after the ~5-minute build. A keychain is unlocked per
# login session: on the factory build host, the release keychain unlocked in
# an interactive `ssh -t` session still read as locked from another session.
# So listing the identity is not enough; it has to sign something here.
#
# NOTARYTOOL_KEYCHAIN names the keychain that holds the notarytool profile
# when it is not the default keychain. On the factory host that is the
# dedicated release keychain; see the ir:release skill, Step 6.
#
# RELEASE_KEYCHAIN_PASSWORD_FILE (optional) unlocks NOTARYTOOL_KEYCHAIN in
# this session from a mode-600 file, so a release needs nobody at the keyboard
# (tools/release-keychain.sh says how, and what it refuses). When this run
# unlocked the keychain, it locks it again on exit; a keychain that was
# already unlocked is left as it was.
if [[ -n "${RELEASE_KEYCHAIN_PASSWORD_FILE:-}" ]]; then
    if [[ -z "${NOTARYTOOL_KEYCHAIN:-}" ]]; then
        echo "ERROR: RELEASE_KEYCHAIN_PASSWORD_FILE is set but NOTARYTOOL_KEYCHAIN, the keychain it unlocks, is not." >&2
        exit 1
    fi
    KEYCHAIN_STATE=$("$SCRIPT_DIR/release-keychain.sh" unlock "$NOTARYTOOL_KEYCHAIN" "$RELEASE_KEYCHAIN_PASSWORD_FILE")
    echo "Release keychain: $KEYCHAIN_STATE ($NOTARYTOOL_KEYCHAIN)"
    if [[ "$KEYCHAIN_STATE" == "unlocked" ]]; then
        trap '"$SCRIPT_DIR/release-keychain.sh" lock "$NOTARYTOOL_KEYCHAIN" && echo "Release keychain: locked again"' EXIT
    fi
fi
NOTARY_ARGS=()
if [[ -n "${NOTARYTOOL_KEYCHAIN_PROFILE:-}" ]]; then
    NOTARY_ARGS=(--keychain-profile "$NOTARYTOOL_KEYCHAIN_PROFILE")
    if [[ -n "${NOTARYTOOL_KEYCHAIN:-}" ]]; then
        NOTARY_ARGS+=(--keychain "$NOTARYTOOL_KEYCHAIN")
    fi
fi
if [[ -n "${DEVELOPER_ID:-}" ]]; then
    echo "Signing preflight..."
    if [[ -z "${NOTARYTOOL_KEYCHAIN_PROFILE:-}" ]]; then
        echo "ERROR: DEVELOPER_ID is set but NOTARYTOOL_KEYCHAIN_PROFILE is not." >&2
        echo "       A Developer ID-signed DMG must be notarized — without it Gatekeeper blocks launch." >&2
        echo "       Create a profile first: xcrun notarytool store-credentials <profile>" >&2
        exit 1
    fi
    PREFLIGHT_IDENTITY="Developer ID Application: ${DEVELOPER_ID}"
    if ! security find-identity -v -p codesigning | grep -qF "\"$PREFLIGHT_IDENTITY\""; then
        echo "ERROR: no valid codesigning identity \"$PREFLIGHT_IDENTITY\" in the keychain search list." >&2
        exit 1
    fi
    PREFLIGHT_DIR=$(mktemp -d)
    cp /usr/bin/true "$PREFLIGHT_DIR/sign-probe"
    if ! codesign --force --sign "$PREFLIGHT_IDENTITY" --options runtime --timestamp \
            "$PREFLIGHT_DIR/sign-probe" 2>"$PREFLIGHT_DIR/codesign.err"; then
        echo "ERROR: cannot sign with \"$PREFLIGHT_IDENTITY\". Is its keychain unlocked in THIS session?" >&2
        cat "$PREFLIGHT_DIR/codesign.err" >&2
        rm -rf "$PREFLIGHT_DIR"
        exit 1
    fi
    if ! xcrun notarytool history "${NOTARY_ARGS[@]}" >/dev/null 2>"$PREFLIGHT_DIR/notarytool.err"; then
        echo "ERROR: notarytool cannot use profile \"$NOTARYTOOL_KEYCHAIN_PROFILE\"${NOTARYTOOL_KEYCHAIN:+ in $NOTARYTOOL_KEYCHAIN}." >&2
        cat "$PREFLIGHT_DIR/notarytool.err" >&2
        rm -rf "$PREFLIGHT_DIR"
        exit 1
    fi
    rm -rf "$PREFLIGHT_DIR"
    echo "  Identity signs here; notarytool profile readable"
fi

# Clean build directory
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

# ── 1. Build Go binaries (universal) ─────────────────────────────────
echo ""
echo "Building Go binaries..."

build_universal() {
    local pkg="$1"
    local out="$2"
    echo "  Building $out..."
    GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$VERSION" \
        -o "../$BUILD_DIR/${out}-darwin-arm64" "$pkg"
    GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.Version=$VERSION" \
        -o "../$BUILD_DIR/${out}-darwin-amd64" "$pkg"
    lipo -create -output "../$BUILD_DIR/${out}-darwin-universal" \
        "../$BUILD_DIR/${out}-darwin-arm64" \
        "../$BUILD_DIR/${out}-darwin-amd64"
}

cd core
build_universal "./cmd/irrlichd/"       "$DAEMON_NAME"
build_universal "./cmd/irrlicht-focus/" "irrlicht-focus"
build_universal "./cmd/irrlicht-ls/"    "irrlicht-ls"
cd ..

echo "  Testing daemon..."
"$BUILD_DIR/${DAEMON_NAME}-darwin-universal" --version

# ── 1b. Tarball the standalone daemon with its UI ────────────────────
# The curl --daemon-only installer downloads this tarball; the daemon
# resolves the UI from ~/.local/share/irrlicht/web at runtime.
echo ""
echo "Creating standalone daemon tarball..."
TARBALL_STAGING="$BUILD_DIR/tarball-staging"
rm -rf "$TARBALL_STAGING"
mkdir -p "$TARBALL_STAGING/web"
cp "$BUILD_DIR/${DAEMON_NAME}-darwin-universal" "$TARBALL_STAGING/${DAEMON_NAME}"
copy_web_files "$TARBALL_STAGING/web"
tar -czf "$BUILD_DIR/${DAEMON_NAME}-darwin-universal.tar.gz" -C "$TARBALL_STAGING" .
rm -rf "$TARBALL_STAGING"
echo "  Created $BUILD_DIR/${DAEMON_NAME}-darwin-universal.tar.gz"

# ── 1c. Linux daemon tarballs (daemon-only — no tray UI on Linux) ─────
# Pure cross-compile from macOS: the daemon is pure Go (Linux process
# observation is /proc + pidfd based, no cgo). One tarball per arch, same
# layout as the darwin tarball (daemon + web/), extracted to
# ~/.local/share/irrlicht by the curl installer. The web dashboard at
# 127.0.0.1:7837 is the Linux UI — there is no tray app on Linux yet.
echo ""
echo "Creating Linux daemon tarballs..."
build_linux_tarball() {
    local arch="$1"
    echo "  Building ${DAEMON_NAME}-linux-${arch}..."
    ( cd core && GOOS=linux GOARCH="$arch" go build -ldflags "-X main.Version=$VERSION" \
        -o "../$BUILD_DIR/${DAEMON_NAME}-linux-${arch}" ./cmd/irrlichd/ )
    local staging="$BUILD_DIR/tarball-staging-linux-${arch}"
    rm -rf "$staging"
    mkdir -p "$staging/web"
    cp "$BUILD_DIR/${DAEMON_NAME}-linux-${arch}" "$staging/${DAEMON_NAME}"
    copy_web_files "$staging/web"
    tar -czf "$BUILD_DIR/${DAEMON_NAME}-linux-${arch}.tar.gz" -C "$staging" .
    rm -rf "$staging"
    echo "  Created $BUILD_DIR/${DAEMON_NAME}-linux-${arch}.tar.gz"
}
build_linux_tarball amd64
build_linux_tarball arm64

# ── 1d. Linux relay tarballs ──────────────────────────────────────────
# The standalone relay hub, published so operators stop having to build from
# source. Both arches are first-class: the deployment target's free tier is
# Ampere A1 (aarch64), with an amd64 shape operators land on when A1 capacity
# is unavailable (docs/mobile-notifications-arc42.md §7).
#
# Two things here differ from the daemon tarballs above and neither is
# cosmetic:
#
#   · CGO_ENABLED=0 is set EXPLICITLY rather than inherited from what a
#     darwin→linux cross-compile happens to imply, because the relay is meant
#     to run on musl and distroless bases where a dynamically linked binary
#     dies at exec time with no useful message.
#
#   · The layout is {bin/, Resources/web/}, NOT the daemon's flat
#     {irrlichd, web/}. The relay resolves its UI through the same search
#     order the daemon uses, whose only exe-relative branch is
#     <exedir>/../Resources/web (core/cmd/irrlichtrelay/main.go
#     resolveUIDirFor). Measured: staged flat, the relay logs "dashboard UI
#     not found" and answers 503 on /; staged this way it logs "serving
#     dashboard from …/Resources/web" with no environment set at all.
#
# The web payload comes from copy_web_files so the relay inherits WEB_FILES
# rather than growing a fourth copy of the list.
echo ""
echo "Creating Linux relay tarballs..."
RELAY_NAME="irrlichtrelay"
build_relay_linux_tarball() {
    local arch="$1"
    echo "  Building ${RELAY_NAME}-linux-${arch}..."
    ( cd core && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
        -ldflags "-X main.Version=$VERSION" \
        -o "../$BUILD_DIR/${RELAY_NAME}-linux-${arch}" ./cmd/irrlichtrelay/ )
    local staging="$BUILD_DIR/tarball-staging-relay-${arch}"
    rm -rf "$staging"
    mkdir -p "$staging/bin" "$staging/Resources/web"
    cp "$BUILD_DIR/${RELAY_NAME}-linux-${arch}" "$staging/bin/${RELAY_NAME}"
    copy_web_files "$staging/Resources/web"
    tar -czf "$BUILD_DIR/${RELAY_NAME}-linux-${arch}.tar.gz" -C "$staging" .
    rm -rf "$staging"
    echo "  Created $BUILD_DIR/${RELAY_NAME}-linux-${arch}.tar.gz"
}
build_relay_linux_tarball amd64
build_relay_linux_tarball arm64

# ── 3. Build Swift macOS app (.app bundle) ─────────────────────────────
# Universal (arm64 + x86_64), by one of two routes:
#   · `swift build --arch arm64 --arch x86_64` needs XCBuild, which only Xcode
#     ships. SwiftPM looks for it at the path XCBUILD below. On a host with
#     only the Command Line Tools it fails with "xcbuild executable at
#     '/Library/Developer/SharedFrameworks/XCBuild.framework/Versions/A/Support/xcbuild'
#     does not exist or is not executable" (the factory build host, Swift 6.3.3).
#   · Without XCBuild, each arch is built on SwiftPM's native build system and
#     the two are joined with `lipo -create`, as the Go binaries above are.
# Both routes then pass the same arch and freshness checks.
#
# The build's exit status is checked. It used to be piped into `tail`, which
# discarded it, and a `find` fallback then picked up whatever older Release
# binary was lying around.
#
# The release build has its own SwiftPM scratch directory, SWIFT_SCRATCH,
# under $BUILD_DIR, which the clean step above has just deleted. It never
# reads platforms/macos/.build, the dev and test builds' cache. That cache had
# been copied over from another Mac on the factory host, and its
# workspace-state.json named that Mac's paths: the v0.6.8 build failed at
# planning with "XCFramework Info.plist not found at
# '/Users/ingo/projects/irrlicht/platforms/macos/.build/artifacts/...'". A
# release starts from a fresh resolve and a full compile every time, so no
# leftover state from an earlier build or another machine can reach it.
echo ""
echo "Building macOS app..."
SWIFT_LOG="$ROOT_DIR/$BUILD_DIR/swift-build.log"
SWIFT_SCRATCH="$ROOT_DIR/$BUILD_DIR/swift"
XCBUILD="${DEVELOPER_DIR:-$(xcode-select -p)}/../SharedFrameworks/XCBuild.framework/Versions/A/Support/xcbuild"

swift_build() {
    if ! ( cd platforms/macos && swift build -c release --scratch-path "$SWIFT_SCRATCH" "$@" ) >>"$SWIFT_LOG" 2>&1; then
        echo "ERROR: swift build -c release --scratch-path $SWIFT_SCRATCH $* failed. Last lines of $SWIFT_LOG:" >&2
        tail -30 "$SWIFT_LOG" >&2
        exit 1
    fi
}

require_universal() {
    local f="$1" archs a
    archs=$(lipo -archs "$f") || { echo "ERROR: lipo cannot read $f" >&2; exit 1; }
    for a in arm64 x86_64; do
        if [[ " $archs " != *" $a "* ]]; then
            echo "ERROR: $f lacks $a (has: $archs)" >&2
            exit 1
        fi
    done
}

if [[ -x "$XCBUILD" ]]; then
    echo "  Universal build via XCBuild"
    swift_build --arch arm64 --arch x86_64
    SWIFT_BIN="$SWIFT_SCRATCH/apple/Products/Release/Irrlicht"
    SWIFT_INPUTS=("$SWIFT_BIN")
    SPARKLE_SRC="$SWIFT_SCRATCH/apple/Products/Release/Sparkle.framework"
    if [[ ! -d "$SPARKLE_SRC" ]]; then
        SPARKLE_SRC=$(find "$SWIFT_SCRATCH" -name "Sparkle.framework" -type d -not -path "*artifacts*" | grep -i release | head -1)
    fi
else
    echo "  No XCBuild (Command Line Tools only): building each arch, then lipo"
    SWIFT_INPUTS=()
    for arch in arm64 x86_64; do
        swift_build --arch "$arch"
        SWIFT_ARCH_DIR=$(cd platforms/macos && swift build -c release --scratch-path "$SWIFT_SCRATCH" --arch "$arch" --show-bin-path)
        SWIFT_INPUTS+=("$SWIFT_ARCH_DIR/Irrlicht")
    done
    for f in "${SWIFT_INPUTS[@]}"; do
        [[ -f "$f" ]] || { echo "ERROR: Swift build output missing: $f" >&2; exit 1; }
    done
    SWIFT_BIN="$BUILD_DIR/Irrlicht-swift-universal"
    lipo -create -output "$SWIFT_BIN" "${SWIFT_INPUTS[@]}"
    # The native build adds an rpath into the build host's toolchain
    # (/Library/Developer/CommandLineTools/usr/lib/swift-6.2/macosx on the
    # factory host). That directory doesn't exist on users' Macs, and nothing
    # in the app is resolved through it: every Swift library is linked by its
    # /usr/lib/swift path, and Sparkle, the only @rpath library, resolves
    # through @executable_path/../Frameworks, added below. Shipping a
    # build-host path is pointless, so drop it.
    SWIFT_DEV_DIR="${DEVELOPER_DIR:-$(xcode-select -p)}"
    for rp in $(otool -l "$SWIFT_BIN" | awk '$1 == "path" { print $2 }' | sort -u); do
        case "$rp" in
            "$SWIFT_DEV_DIR"/*) install_name_tool -delete_rpath "$rp" "$SWIFT_BIN" ;;
        esac
    done
    # Sparkle is a prebuilt binary framework, so each per-arch output holds
    # the same copy. The arch check below confirms that copy is universal.
    SPARKLE_SRC="$SWIFT_ARCH_DIR/Sparkle.framework"
fi

if [[ ! -f "$SWIFT_BIN" ]]; then
    echo "ERROR: Could not find built Irrlicht binary at $SWIFT_BIN" >&2
    exit 1
fi
require_universal "$SWIFT_BIN"
# Freshness: SwiftPM relinks whenever a source changed, so every binary the
# app is made from must be newer than the newest app source. A binary older
# than one is stale; v0.3.4 shipped a 10-day-old app this way.
NEWEST_SRC=$(find platforms/macos/Irrlicht -name '*.swift' -print0 | xargs -0 stat -f '%m %N' | sort -n | tail -1 | cut -d' ' -f2-)
for f in "${SWIFT_INPUTS[@]}"; do
    if [[ ! "$f" -nt "$NEWEST_SRC" ]]; then
        echo "ERROR: $f is not newer than $NEWEST_SRC. Stale Swift binary." >&2
        exit 1
    fi
done
echo "  Built: $SWIFT_BIN ($(lipo -archs "$SWIFT_BIN"))"

# Create .app bundle
APP_BUNDLE="$BUILD_DIR/${APP_NAME}.app"
APP_CONTENTS="$APP_BUNDLE/Contents"
mkdir -p "$APP_CONTENTS/MacOS"
mkdir -p "$APP_CONTENTS/Resources/web"

cp "$SWIFT_BIN" "$APP_CONTENTS/MacOS/${APP_NAME}"

# Web UI lives next to the daemon — resolved by irrlichd at runtime via
# <exe>/../Resources/web (see resolveUIDir in core/cmd/irrlichd/main.go).
copy_web_files "$APP_CONTENTS/Resources/web"

# AppIcon — required for menu bar / Finder display.
cp platforms/macos/Irrlicht/Resources/AppIcon.icns "$APP_CONTENTS/Resources/AppIcon.icns"

# Embed daemon and CLI tools inside the app bundle (single-artifact distribution)
cp "$BUILD_DIR/${DAEMON_NAME}-darwin-universal" "$APP_CONTENTS/MacOS/${DAEMON_NAME}"
chmod 755 "$APP_CONTENTS/MacOS/${DAEMON_NAME}"
cp "$BUILD_DIR/irrlicht-focus-darwin-universal" "$APP_CONTENTS/MacOS/irrlicht-focus"
chmod 755 "$APP_CONTENTS/MacOS/irrlicht-focus"
cp "$BUILD_DIR/irrlicht-ls-darwin-universal" "$APP_CONTENTS/MacOS/irrlicht-ls"
chmod 755 "$APP_CONTENTS/MacOS/irrlicht-ls"
echo "  Embedded daemon, irrlicht-focus, and irrlicht-ls in app bundle"

# Embed Sparkle.framework so the Sparkle auto-updater finds itself at runtime.
# SwiftPM copies Sparkle.framework next to the executable in its build output;
# in the .app bundle the conventional home is Contents/Frameworks/, so we
# copy it there and add an @executable_path/../Frameworks rpath to the main
# binary (the SwiftPM-built executable only has @loader_path, which points
# at Contents/MacOS/ inside the bundle).
# SPARKLE_SRC was resolved by the Swift build step above, per build route.
if [[ -z "$SPARKLE_SRC" ]] || [[ ! -d "$SPARKLE_SRC" ]]; then
    echo "ERROR: Could not find Sparkle.framework in SwiftPM build output" >&2
    exit 1
fi
require_universal "$SPARKLE_SRC/Versions/Current/Sparkle"
mkdir -p "$APP_CONTENTS/Frameworks"
cp -R "$SPARKLE_SRC" "$APP_CONTENTS/Frameworks/Sparkle.framework"
# Only add the rpath if it isn't already present — install_name_tool errors
# on duplicates, and SwiftPM may grow this rpath itself in a future toolchain.
if ! otool -l "$APP_CONTENTS/MacOS/${APP_NAME}" | grep -q "@executable_path/../Frameworks"; then
    install_name_tool -add_rpath "@executable_path/../Frameworks" "$APP_CONTENTS/MacOS/${APP_NAME}"
fi
echo "  Embedded Sparkle.framework"

# Generate Info.plist with resolved variables.
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
    <string>AppIcon</string>
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
    <key>NSAppleEventsUsageDescription</key>
    <string>Irrlicht uses AppleScript to bring the correct iTerm2 or Terminal.app window and tab to the front when you click a session row.</string>
    <key>NSHumanReadableCopyright</key>
    <string>Copyright 2024 Anthropic. All rights reserved.</string>
    <key>NSPrincipalClass</key>
    <string>NSApplication</string>
    <key>SUFeedURL</key>
    <string>https://irrlicht.io/appcast.xml</string>
    <key>SUPublicEDKey</key>
    <string>nKRcUPAmK6syLFEvp9O30FFvjhTIfGxYVv/6y8zpZI0=</string>
    <key>SUEnableAutomaticChecks</key>
    <true/>
</dict>
</plist>
PLIST

echo "  Created $APP_BUNDLE"

# Embed provisioning profile — required for restricted entitlements (focus-status)
# under Developer ID distribution. Set PROVISIONING_PROFILE to the path of the
# .provisionprofile downloaded from developer.apple.com.
if [[ -n "${PROVISIONING_PROFILE:-}" ]]; then
    if [[ ! -f "$PROVISIONING_PROFILE" ]]; then
        echo "ERROR: PROVISIONING_PROFILE set but file not found: $PROVISIONING_PROFILE" >&2
        exit 1
    fi
    cp "$PROVISIONING_PROFILE" "$APP_CONTENTS/embedded.provisionprofile"
    echo "  Embedded provisioning profile: $PROVISIONING_PROFILE"
elif [[ -n "${DEVELOPER_ID:-}" ]]; then
    echo "WARNING: DEVELOPER_ID is set but PROVISIONING_PROFILE is not."
    echo "         The focus-status entitlement requires a Developer ID provisioning"
    echo "         profile to be honoured at runtime. Download one from developer.apple.com"
    echo "         and re-run with PROVISIONING_PROFILE=/path/to/profile.provisionprofile"
fi

# ── 3b. Sign app bundle ────────────────────────────────────────────────
# Sparkle's nested helpers (Downloader.xpc, Installer.xpc, Updater.app,
# Autoupdate, the framework binary itself) must each be signed deepest-first
# before the outer bundle so that `codesign --verify --deep --strict` accepts
# the chain. Order per https://sparkle-project.org/documentation/sandboxing/.
echo ""
ENTITLEMENTS="platforms/macos/Irrlicht/Resources/Irrlicht.entitlements"
SPARKLE_FW="$APP_CONTENTS/Frameworks/Sparkle.framework"
SPARKLE_VERSION_DIR="$SPARKLE_FW/Versions/Current"

sign_sparkle() {
    local sign_args=("$@")
    for xpc in "$SPARKLE_VERSION_DIR"/XPCServices/*.xpc; do
        [[ -d "$xpc" ]] || continue
        codesign --force "${sign_args[@]}" "$xpc"
    done
    if [[ -d "$SPARKLE_VERSION_DIR/Updater.app" ]]; then
        codesign --force "${sign_args[@]}" "$SPARKLE_VERSION_DIR/Updater.app"
    fi
    if [[ -f "$SPARKLE_VERSION_DIR/Autoupdate" ]]; then
        codesign --force "${sign_args[@]}" "$SPARKLE_VERSION_DIR/Autoupdate"
    fi
    codesign --force "${sign_args[@]}" "$SPARKLE_VERSION_DIR/Sparkle"
    codesign --force "${sign_args[@]}" "$SPARKLE_FW"
}

if [[ -n "${DEVELOPER_ID:-}" ]]; then
    echo "Signing app bundle with Developer ID..."
    SIGN_IDENTITY="Developer ID Application: ${DEVELOPER_ID}"
    sign_sparkle --sign "$SIGN_IDENTITY" --options runtime --timestamp
    codesign --force --sign "$SIGN_IDENTITY" --options runtime --timestamp \
        "$APP_CONTENTS/MacOS/${DAEMON_NAME}"
    codesign --force --sign "$SIGN_IDENTITY" --options runtime --timestamp \
        "$APP_CONTENTS/MacOS/irrlicht-focus"
    codesign --force --sign "$SIGN_IDENTITY" --options runtime --timestamp \
        "$APP_CONTENTS/MacOS/irrlicht-ls"
    codesign --force --sign "$SIGN_IDENTITY" --options runtime --timestamp \
        --entitlements "$ENTITLEMENTS" "$APP_BUNDLE"
    codesign --verify --deep --strict "$APP_BUNDLE"
    # Value-aware XPath: a <false/> declaration on get-task-allow is harmless
    # and shouldn't fail the build; only an explicit <true/> blocks notarization.
    # `:-` returns the raw plist XML; bare `-` returns human-readable text.
    ENTS_XML=$(codesign -d --entitlements :- "$APP_BUNDLE" 2>/dev/null)
    GTA_TRUE=$(echo "$ENTS_XML" | xmllint --xpath \
      "boolean(/plist/dict/key[text()='com.apple.security.get-task-allow']/following-sibling::*[1][self::true])" \
      - 2>/dev/null)
    [[ "$GTA_TRUE" = "true" ]] && { echo "ERROR: get-task-allow=true in signed bundle — notarization would reject" >&2; exit 1; } || true
    echo "  Signed $APP_BUNDLE (Developer ID); get-task-allow not true"
else
    echo "Signing app bundle (ad-hoc — set DEVELOPER_ID to use Developer ID cert)..."
    sign_sparkle --sign -
    codesign --force --sign - "$APP_CONTENTS/MacOS/${DAEMON_NAME}"
    codesign --force --sign - "$APP_CONTENTS/MacOS/irrlicht-focus"
    codesign --force --sign - "$APP_CONTENTS/MacOS/irrlicht-ls"
    codesign --force --sign - "$APP_BUNDLE"
    codesign --verify --deep --strict "$APP_BUNDLE"
    echo "  Ad-hoc signed $APP_BUNDLE"
fi

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

# ── 4a. Sign the DMG file itself ──────────────────────────────────────
# The app inside is already Developer ID-signed, but an unsigned DMG fails
# `spctl -a -t open --context context:primary-signature` (v0.4.5–v0.5.0
# shipped unsigned DMGs; harmless for Gatekeeper, but the release-skill
# verification flags it). Must happen BEFORE notarization so the ticket
# covers the signed bytes — signing after stapling would invalidate it.
if [[ -n "${DEVELOPER_ID:-}" ]]; then
    echo "  Signing DMG with Developer ID..."
    codesign --force --sign "Developer ID Application: ${DEVELOPER_ID}" \
        --timestamp "$BUILD_DIR/$DMG_NAME"
    codesign --verify "$BUILD_DIR/$DMG_NAME"
    echo "  Signed $DMG_NAME"
fi

# ── 4b. Notarize and staple DMG ────────────────────────────────────────
if [[ -n "${NOTARYTOOL_KEYCHAIN_PROFILE:-}" ]]; then
    echo ""
    echo "Notarizing DMG..."
    xcrun notarytool submit "$BUILD_DIR/$DMG_NAME" \
        "${NOTARY_ARGS[@]}" \
        --wait
    echo "  Stapling notarization ticket..."
    xcrun stapler staple "$BUILD_DIR/$DMG_NAME"
    xcrun stapler validate "$BUILD_DIR/$DMG_NAME"
    echo "  Notarized and stapled $DMG_NAME"
fi
# (DEVELOPER_ID without NOTARYTOOL_KEYCHAIN_PROFILE is refused by the
# signing preflight at the top, before anything is built.)

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
# Put the irrlicht-ls CLI on PATH (#608). Runs as root, so /usr/local/bin
# is always writable; ln -sf keeps reinstalls/upgrades idempotent. Guarded
# with || true — a failed symlink must never fail the install.
mkdir -p /usr/local/bin 2>/dev/null || true
ln -sf /Applications/Irrlicht.app/Contents/MacOS/irrlicht-ls /usr/local/bin/irrlicht-ls 2>/dev/null || true
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
# site/install.sh's sha256_verify greps this file for the asset it just
# downloaded, so an asset missing here cannot be verified and the install
# aborts — every published tarball has to be listed.
shasum -a 256 "$DMG_NAME" "$PKG_NAME" \
    ${DAEMON_NAME}-darwin-universal.tar.gz \
    ${DAEMON_NAME}-linux-amd64.tar.gz \
    ${DAEMON_NAME}-linux-arm64.tar.gz \
    ${RELAY_NAME}-linux-amd64.tar.gz \
    ${RELAY_NAME}-linux-arm64.tar.gz > checksums.sha256
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
echo "Linux daemon (daemon-only, web UI at 127.0.0.1:7837):"
echo "  amd64:      $BUILD_DIR/${DAEMON_NAME}-linux-amd64.tar.gz"
echo "  arm64:      $BUILD_DIR/${DAEMON_NAME}-linux-arm64.tar.gz"
echo ""
echo "Linux relay hub (extract anywhere; bin/ + Resources/web/ travel together):"
echo "  amd64:      $BUILD_DIR/${RELAY_NAME}-linux-amd64.tar.gz"
echo "  arm64:      $BUILD_DIR/${RELAY_NAME}-linux-arm64.tar.gz"
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
