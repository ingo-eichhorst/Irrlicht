#!/usr/bin/env bash
# Static safety checks for the live macOS control layer.
set -euo pipefail

PACKAGE_DIR="$(cd "$(dirname "$0")" && pwd)"
LIVE_DIR="$PACKAGE_DIR/Sources/ClaudeDesktopHelper"
RUNNER="$LIVE_DIR/CommandRunner.swift"
TEST_SCRIPT="$PACKAGE_DIR/test.sh"

assert_absent() {
  label=$1
  pattern=$2
  path=$3

  if matches=$(LC_ALL=C grep -R -n -E "$pattern" "$path" 2>&1); then
    printf '%s\n' "$matches" >&2
    echo "desktop-helper guardrails: $label" >&2
    exit 1
  else
    status=$?
    if [ "$status" -ne 1 ]; then
      printf '%s\n' "$matches" >&2
      echo "desktop-helper guardrails: could not scan $path for $label" >&2
      exit 1
    fi
  fi
}

assert_present() {
  label=$1
  needle=$2
  path=$3

  if LC_ALL=C grep -q -F "$needle" "$path"; then
    return
  else
    status=$?
    if [ "$status" -eq 1 ]; then
      echo "desktop-helper guardrails: $label" >&2
    else
      echo "desktop-helper guardrails: could not scan $path for $label" >&2
    fi
    exit 1
  fi
}

assert_count() {
  label=$1
  needle=$2
  expected=$3
  path=$4

  if count=$(LC_ALL=C grep -c -F "$needle" "$path"); then
    :
  else
    status=$?
    if [ "$status" -ne 1 ]; then
      echo "desktop-helper guardrails: could not count $needle in $path" >&2
      exit 1
    fi
  fi
  if [ "$count" -ne "$expected" ]; then
    echo "desktop-helper guardrails: $label (expected $expected, found $count)" >&2
    exit 1
  fi
}

assert_absent \
  "forbidden indirect control or process execution found" \
  'AXUIElementPerformAction|kAXPressAction|NSAppleScript|osascript|Process[[:space:]]*\(' \
  "$LIVE_DIR"
assert_absent \
  "fixed click coordinate found" \
  '(CG)?Point[[:space:]]*\([[:space:]]*x:[[:space:]]*[-+]?[0-9]' \
  "$LIVE_DIR"

assert_present \
  "click geometry is not derived from the fresh frame" \
  'let plan = try ClickPlan(freshFrame: frame)' \
  "$RUNNER"
assert_present \
  "the physical click bypasses its dynamic click plan" \
  'dependencies.physicalClick(plan.point)' \
  "$RUNNER"
assert_present \
  "the keyboard event bypasses its final focus and frontmost guard" \
  'try KeyboardEventBoundary.emit(' \
  "$RUNNER"
assert_count \
  "not every action enforces a false-to-true postcondition transition" \
  'try performAction(' \
  3 \
  "$RUNNER"
assert_present \
  "generated output left repository-root ./.build" \
  'SCRATCH_DIR="$REPO_ROOT/.build/claude-desktop-helper"' \
  "$TEST_SCRIPT"

echo "desktop-helper guardrails: PASS"
