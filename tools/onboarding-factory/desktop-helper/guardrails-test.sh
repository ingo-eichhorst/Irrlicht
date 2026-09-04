#!/usr/bin/env bash
# Static safety checks for the live macOS control layer.
set -euo pipefail

PACKAGE_DIR="$(cd "$(dirname "$0")" && pwd)"
LIVE_DIR="$PACKAGE_DIR/Sources/ClaudeDesktopHelper"
RUNNER="$LIVE_DIR/CommandRunner.swift"
TEST_SCRIPT="$PACKAGE_DIR/test.sh"

command -v rg >/dev/null 2>&1 || {
  echo "desktop-helper guardrails: rg is required" >&2
  exit 1
}

if rg -n 'AXUIElementPerformAction|kAXPressAction|NSAppleScript|osascript|Process\(' "$LIVE_DIR"; then
  echo "desktop-helper guardrails: forbidden indirect control or process execution found" >&2
  exit 1
fi

if rg -n '(CG)?Point[[:space:]]*\([[:space:]]*x:[[:space:]]*[-+]?[0-9]' "$LIVE_DIR"; then
  echo "desktop-helper guardrails: fixed click coordinate found" >&2
  exit 1
fi

rg -q 'let plan = try ClickPlan\(freshFrame: frame\)' "$RUNNER" || {
  echo "desktop-helper guardrails: click geometry is not derived from the fresh frame" >&2
  exit 1
}
rg -q 'AXRuntime\.physicalClick\(plan\.point\)' "$RUNNER" || {
  echo "desktop-helper guardrails: the physical click bypasses its dynamic click plan" >&2
  exit 1
}
rg -q 'SCRATCH_DIR="\$REPO_ROOT/\.build/claude-desktop-helper"' "$TEST_SCRIPT" || {
  echo "desktop-helper guardrails: generated output left repository-root ./.build" >&2
  exit 1
}

echo "desktop-helper guardrails: PASS"
