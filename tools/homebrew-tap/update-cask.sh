#!/bin/bash
# update-cask.sh — bump the Homebrew cask to a new version + DMG sha256.
#
# Updates tools/homebrew-tap/Casks/irrlicht.rb in this repo (the canonical
# template). If IRRLICHT_TAP_DIR points at a clone of the external tap repo
# (ingo-eichhorst/homebrew-irrlicht), the file is also copied there and
# committed; pass --push to push to origin.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
CASK_FILE="$SCRIPT_DIR/Casks/irrlicht.rb"

VERSION=""
DMG_PATH=""
PUSH=0

usage() {
    cat <<'HELP'
Usage: update-cask.sh [--version X.Y.Z] [--dmg path] [--push]

  --version  bump to this version (default: read from version.json)
  --dmg      DMG path to hash (default: probe .build/ then /tmp/)
  --push     git push the bumped cask in $IRRLICHT_TAP_DIR
HELP
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --dmg)     DMG_PATH="$2"; shift 2 ;;
        --push)    PUSH=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown arg: $1" >&2; usage >&2; exit 2 ;;
    esac
done

if [ -z "$VERSION" ]; then
    VERSION=$(python3 -c "import json; print(json.load(open('$ROOT_DIR/version.json'))['version'])")
fi

DMG_CANDIDATES=(
    "$ROOT_DIR/.build/Irrlicht-$VERSION.dmg"
    "/tmp/Irrlicht-$VERSION.dmg"
)
if [ -z "$DMG_PATH" ]; then
    for candidate in "${DMG_CANDIDATES[@]}"; do
        [ -f "$candidate" ] && { DMG_PATH="$candidate"; break; }
    done
fi

if [ -z "$DMG_PATH" ] || [ ! -f "$DMG_PATH" ]; then
    echo "DMG not found for version $VERSION (probed: ${DMG_CANDIDATES[*]}) — pass --dmg <path>" >&2
    exit 1
fi

SHA=$(shasum -a 256 "$DMG_PATH" | awk '{print $1}')

echo "version: $VERSION"
echo "dmg:     $DMG_PATH"
echo "sha256:  $SHA"

python3 - "$CASK_FILE" "$VERSION" "$SHA" <<'PY'
import re, sys
path, version, sha = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path).read()
src = re.sub(r'(version\s+)"[^"]+"', f'\\1"{version}"', src, count=1)
src = re.sub(r'(sha256\s+)"[0-9a-fA-F]{64}"', f'\\1"{sha}"', src, count=1)
open(path, "w").write(src)
PY

echo "updated $CASK_FILE"

if [ -z "${IRRLICHT_TAP_DIR:-}" ]; then
    echo "IRRLICHT_TAP_DIR not set — skipping external tap sync."
    echo "set IRRLICHT_TAP_DIR=<path to homebrew-irrlicht clone> to publish."
    exit 0
fi

if [ ! -d "$IRRLICHT_TAP_DIR/.git" ]; then
    echo "IRRLICHT_TAP_DIR is not a git repo: $IRRLICHT_TAP_DIR" >&2
    exit 1
fi

mkdir -p "$IRRLICHT_TAP_DIR/Casks"
cp "$CASK_FILE" "$IRRLICHT_TAP_DIR/Casks/irrlicht.rb"

cd "$IRRLICHT_TAP_DIR"

# Stage first, then check the index — `git diff --quiet` reports no diff for
# untracked files, which would silently no-op a fresh tap clone.
git add Casks/irrlicht.rb
if git diff --cached --quiet -- Casks/irrlicht.rb; then
    echo "tap repo already at $VERSION — nothing to commit."
    exit 0
fi

git commit -m "irrlicht $VERSION"

if [ "$PUSH" -eq 1 ]; then
    # Rebase on top of remote first to avoid non-fast-forward push failures
    # when another machine has already advanced the tap.
    git pull --rebase --autostash || {
        echo "ERROR: rebase failed — resolve manually in $IRRLICHT_TAP_DIR" >&2
        exit 1
    }
    git push origin HEAD
    echo "pushed to $(git remote get-url origin)"
else
    echo "committed locally in $IRRLICHT_TAP_DIR — pass --push to publish."
fi
