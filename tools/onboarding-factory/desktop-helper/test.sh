#!/usr/bin/env bash
# One local/CI entry point for the macOS-only Claude Desktop helper.
set -euo pipefail

PACKAGE_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$PACKAGE_DIR/../../.." && pwd)"
SCRATCH_DIR="$REPO_ROOT/.build/claude-desktop-helper"

if [[ "$(dirname "$SCRATCH_DIR")" != "$REPO_ROOT/.build" ]] \
  || [[ "$(basename "$SCRATCH_DIR")" != "claude-desktop-helper" ]]; then
  echo "desktop-helper: generated output must stay under repository-root ./.build" >&2
  exit 1
fi

"$PACKAGE_DIR/guardrails-test.sh"

swift build --package-path "$PACKAGE_DIR" --scratch-path "$SCRATCH_DIR"
TEST_LIST="$(swift test --package-path "$PACKAGE_DIR" --scratch-path "$SCRATCH_DIR" list)"
if [[ -z "$TEST_LIST" ]]; then
  echo "desktop-helper: Swift test discovery found no tests" >&2
  exit 1
fi
swift test --package-path "$PACKAGE_DIR" --scratch-path "$SCRATCH_DIR"
