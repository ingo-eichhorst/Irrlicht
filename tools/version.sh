#!/usr/bin/env bash
# version.sh — compute the version string to bake into Go binaries.
#
# Output format (semver-style with build metadata):
#   <base>                          — clean checkout exactly on a release commit
#   <base>+<sha7>                   — clean dev checkout (untagged or post-tag commits)
#   <base>+<sha7>.dirty             — dev checkout with uncommitted changes
#
#   <base> = the `version` field of version.json (the "intended" version
#            for the next release; bumped at release time).
#   <sha7> = `git rev-parse --short=7 HEAD`.
#   .dirty = appended when `git status --porcelain` is non-empty.
#
# `+` introduces semver build metadata: anything after it is ignored by
# semver-aware comparators, so dev builds never compare as "newer" than
# the release version they're based on. That keeps `0.3.13+abc1234`
# from accidentally outranking `0.3.13`.
#
# Usage:
#   $(tools/version.sh)              — full computed string
#   $(tools/version.sh --base)       — just the version.json field
#
# Falls back to "dev" if git isn't available (e.g. building from a
# source tarball without a .git directory).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BASE="$(python3 -c "import json; print(json.load(open('$ROOT_DIR/version.json'))['version'])" 2>/dev/null || echo "dev")"

if [[ "${1:-}" == "--base" ]]; then
  echo "$BASE"
  exit 0
fi

if ! command -v git >/dev/null 2>&1 || ! git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
  echo "$BASE"
  exit 0
fi

SHA="$(git -C "$ROOT_DIR" rev-parse --short=7 HEAD 2>/dev/null || echo "")"
if [[ -z "$SHA" ]]; then
  echo "$BASE"
  exit 0
fi

DIRTY=""
if [[ -n "$(git -C "$ROOT_DIR" status --porcelain 2>/dev/null)" ]]; then
  DIRTY=".dirty"
fi

echo "${BASE}+${SHA}${DIRTY}"
