#!/bin/bash
# uninstall.sh — Remove Irrlicht and restore Claude Code hook configuration
#
# Must be run with sudo (removes files from /usr/local/bin and /Applications).
#
# Usage:
#   sudo /usr/local/bin/irrlicht-uninstall.sh
#   sudo ./tools/installer/uninstall.sh

set -euo pipefail

# ---------------------------------------------------------------------------
# Privilege check
# ---------------------------------------------------------------------------
if [[ "$(id -u)" != "0" ]]; then
    echo "❌  This script must be run as root (sudo)." >&2
    echo "    Usage: sudo $0" >&2
    exit 1
fi

echo "🗑️   Irrlicht Uninstaller"
echo "========================"

# ---------------------------------------------------------------------------
# Determine the actual (non-root) user who invoked sudo
# ---------------------------------------------------------------------------
ACTUAL_USER="${SUDO_USER:-}"
ACTUAL_HOME=""
if [[ -n "$ACTUAL_USER" ]]; then
    ACTUAL_HOME=$(eval echo "~$ACTUAL_USER")
fi

# ---------------------------------------------------------------------------
# Quit running Irrlicht instance
# ---------------------------------------------------------------------------
if pgrep -x "Irrlicht" &>/dev/null; then
    echo "Stopping running Irrlicht instance..."
    pkill -x "Irrlicht" || true
    sleep 1
    echo "   ✅  Irrlicht stopped"
fi

# ---------------------------------------------------------------------------
# Revert Claude Code hook configuration (before removing the binary)
# ---------------------------------------------------------------------------
MERGER_BINARY="/usr/local/bin/irrlicht-settings-merger"
if [[ -x "$MERGER_BINARY" ]] && [[ -n "$ACTUAL_USER" ]]; then
    echo "Reverting Claude Code hook configuration for user: $ACTUAL_USER"
    if sudo -u "$ACTUAL_USER" HOME="$ACTUAL_HOME" "$MERGER_BINARY" --action merge-disable 2>/dev/null; then
        echo "   ✅  Claude Code hooks reverted"
    else
        echo "   ⚠️   Could not revert hooks — you may need to remove them manually" >&2
        echo "       from: ${ACTUAL_HOME}/.claude/settings.json" >&2
    fi
elif [[ ! -x "$MERGER_BINARY" ]]; then
    echo "⚠️   settings-merger not found — skipping hook revert" >&2
fi

# ---------------------------------------------------------------------------
# Remove installed files
# ---------------------------------------------------------------------------
REMOVED=()

remove_if_exists() {
    local path="$1"
    if [[ -e "$path" ]] || [[ -L "$path" ]]; then
        rm -rf "$path"
        REMOVED+=("$path")
        echo "   🗑️   Removed: $path"
    fi
}

echo "Removing installed files..."
remove_if_exists "/usr/local/bin/irrlicht-hook"
remove_if_exists "/usr/local/bin/irrlicht-settings-merger"
remove_if_exists "/usr/local/bin/irrlicht-uninstall.sh"
remove_if_exists "/Applications/Irrlicht.app"

# ---------------------------------------------------------------------------
# Remove application support data (optional — prompt user)
# ---------------------------------------------------------------------------
if [[ -n "$ACTUAL_HOME" ]]; then
    IRRLICHT_DATA="${ACTUAL_HOME}/Library/Application Support/Irrlicht"
    if [[ -d "$IRRLICHT_DATA" ]]; then
        echo ""
        read -r -p "Remove application data (session state files) at '${IRRLICHT_DATA}'? [y/N] " REPLY
        if [[ "${REPLY,,}" == "y" ]]; then
            rm -rf "$IRRLICHT_DATA"
            echo "   🗑️   Removed: $IRRLICHT_DATA"
        else
            echo "   Kept: $IRRLICHT_DATA"
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Remove installer receipt (pkgutil)
# ---------------------------------------------------------------------------
BUNDLE_ID="com.anthropic.irrlicht"
if pkgutil --pkgs="$BUNDLE_ID" &>/dev/null; then
    echo "Removing package receipt..."
    pkgutil --forget "$BUNDLE_ID" 2>/dev/null || true
    echo "   ✅  Package receipt removed"
fi

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
echo ""
if [[ ${#REMOVED[@]} -gt 0 ]]; then
    echo "✅  Irrlicht has been uninstalled."
else
    echo "ℹ️   No Irrlicht files were found. Nothing to remove."
fi
echo ""
echo "Claude Code hook configuration has been reverted."
