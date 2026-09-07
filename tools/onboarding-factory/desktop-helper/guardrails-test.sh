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

  # OCCURRENCES, not lines. `grep -c` counts matching LINES, so two calls
  # written on one line counted as one — which is how a second, unguarded click
  # site would have slipped past this check. Verified 2026-09-07 by putting two
  # physicalClick calls on a single line: the count assertion passed.
  if count=$(LC_ALL=C grep -o -F "$needle" "$path" | wc -l | tr -d ' '); then
    :
  else
    echo "desktop-helper guardrails: could not count $needle in $path" >&2
    exit 1
  fi
  [ -n "$count" ] || {
    echo "desktop-helper guardrails: counting $needle in $path produced nothing" >&2
    exit 1
  }
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
# The invariant is that the clicked point comes from the ClickPlan and is proven
# to hit the target — not that it is spelled one particular way. It used to be
# checked as the single literal `dependencies.physicalClick(plan.point)`, which
# stopped matching the moment the plan offered more than one point (#1887: the
# owned-session menu's centre is covered by the window's drag region, so the
# click has to try other points inside the SAME frame). These four assertions
# pin the invariant itself, and together they are stricter than the literal was:
# the points come from the plan, every one is hit-tested, only a hit-tested
# candidate is ever clicked, and there is exactly one click site.
assert_present \
  "the click points are not taken from the dynamic click plan" \
  'for candidate in plan.candidates' \
  "$RUNNER"
assert_present \
  "a candidate click point is not hit-tested against the target" \
  'try dependencies.requireHitTarget(target.element, candidate)' \
  "$RUNNER"
assert_present \
  "the clicked point is not the candidate that passed the hit test" \
  'clickedPoint = candidate' \
  "$RUNNER"
assert_count \
  "the physical click bypasses its dynamic click plan" \
  'dependencies.physicalClick(' \
  1 \
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
