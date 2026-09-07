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

# assert_next_line pins ORDER, which is the only thing that separates
# "the boundary runs per character" from "the loop runs inside one boundary".
# Both spellings contain the same two lines, so assert_present cannot tell them
# apart: verified 2026-09-07 by hoisting the loop into the emit closure — every
# other guardrail still passed while the two behavioural tests went red.
assert_next_line() {
  label=$1
  first=$2
  second=$3
  path=$4

  if line=$(LC_ALL=C grep -n -F -- "$first" "$path" | head -1 | cut -d: -f1); then
    :
  else
    echo "desktop-helper guardrails: could not scan $path for $label" >&2
    exit 1
  fi
  [ -n "$line" ] || {
    echo "desktop-helper guardrails: $label (never found: $first)" >&2
    exit 1
  }
  next=$(sed -n "$((line + 1))p" "$path")
  case "$next" in
    *"$second"*) return ;;
    *)
      echo "desktop-helper guardrails: $label" >&2
      echo "  line $line:      $first" >&2
      echo "  expected next:  $second" >&2
      echo "  found instead:  $next" >&2
      exit 1
      ;;
  esac
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
  4 \
  "$RUNNER"
# Both event-posting commands must cross the boundary that re-checks frontmost
# and focus. type_text crosses it PER CHARACTER: a long argument typed into a
# window that stole focus halfway is the failure this prevents, and one check
# before the first character cannot prevent it.
assert_count \
  "an event-posting command bypasses the focus and frontmost boundary" \
  'try KeyboardEventBoundary.emit(' \
  2 \
  "$RUNNER"
assert_next_line \
  "typed text does not cross the boundary once per character" \
  'for character in text {' \
  'try KeyboardEventBoundary.emit(' \
  "$RUNNER"
assert_present \
  "generated output left repository-root ./.build" \
  'SCRATCH_DIR="$REPO_ROOT/.build/claude-desktop-helper"' \
  "$TEST_SCRIPT"

echo "desktop-helper guardrails: PASS"
