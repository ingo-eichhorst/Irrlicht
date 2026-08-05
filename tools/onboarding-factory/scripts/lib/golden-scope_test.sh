#!/usr/bin/env bash
# golden-scope_test.sh — unit tests for golden-scope.sh. Plain bash (no
# framework), matching the style of classify-failure_test.sh. Run directly or via
# scripts/smoke-test.sh.
#
# Why this exists (#1333, finding B1). refresh-golden.sh regenerates EVERY golden
# (the replay test has no per-fixture filter) and then discards every golden
# change outside its target scenario. Run in a loop — one cell after another, as
# a record sweep does — each iteration reverted the previous one's work. Observed
# directly: a loop over 8 cells reported `M <golden>` for FOUR of them, and
# `git status --short` immediately afterwards showed only the LAST one; the other
# three had been checked out from under it.
#
# The revert was safe only under strict commit-per-cell, because a committed
# golden isn't a "change" to discard — which made commit-per-cell load-bearing
# for a reason the skill never gave (it justifies it as "a dirty replaydata/
# makes the next precheck refuse").
#
# The fix is to snapshot what was ALREADY dirty before regenerating and leave
# exactly those alone. These tests pin that decision as pure list arithmetic, so
# they need no git repo and no `go test` run.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=golden-scope.sh
source "$DIR/golden-scope.sh"

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  local label="$1" expected="$2" got="$3"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

SCOPE="replaydata/agents/copilot/scenarios/2-6_long-agentic-session-stress"
A="replaydata/agents/copilot/scenarios/1-1_session-start/recordings/r1/transcript.jsonl.replay.json.golden"
B="replaydata/agents/copilot/scenarios/1-2_session-end/recordings/r1/transcript.jsonl.replay.json.golden"
MINE="$SCOPE/recordings/r1/transcript.jsonl.replay.json.golden"
OTHER="replaydata/agents/codex/scenarios/1-1_session-start/recordings/r1/transcript.jsonl.replay.json.golden"

echo "== in-scope goldens are never restored =="
got="$(golden_restore_list "$SCOPE" "" "$MINE")"
assert_eq "my own golden is kept" "" "$got"

echo "== an unrelated adapter's drift IS restored (never mask another cell) =="
got="$(golden_restore_list "$SCOPE" "" "$OTHER")"
assert_eq "codex drift reverted" "$OTHER" "$got"

echo "== THE BUG: a golden already dirty before the run must be left alone =="
# A and B were written by earlier iterations of the same loop and are not yet
# committed. Regenerating for 2-6 must not check them out.
got="$(golden_restore_list "$SCOPE" "$(printf '%s\n%s\n' "$A" "$B")" "$(printf '%s\n%s\n%s\n' "$A" "$B" "$MINE")")"
assert_eq "earlier iterations' goldens survive" "" "$got"

echo "== but drift that appeared DURING this run is still restored =="
got="$(golden_restore_list "$SCOPE" "$A" "$(printf '%s\n%s\n%s\n' "$A" "$OTHER" "$MINE")")"
assert_eq "only the new drift is reverted" "$OTHER" "$got"

echo "== untracked goldens: same rule =="
got="$(golden_remove_list "$SCOPE" "" "$OTHER")"
assert_eq "a new out-of-scope golden is removed" "$OTHER" "$got"

got="$(golden_remove_list "$SCOPE" "$A" "$(printf '%s\n%s\n' "$A" "$OTHER")")"
assert_eq "a pre-existing untracked golden is kept" "$OTHER" "$got"

got="$(golden_remove_list "$SCOPE" "" "$MINE")"
assert_eq "my own new golden is kept" "" "$got"

echo "== empty inputs don't produce phantom entries =="
assert_eq "no modifications -> nothing to restore" "" "$(golden_restore_list "$SCOPE" "" "")"
assert_eq "no untracked -> nothing to remove" "" "$(golden_remove_list "$SCOPE" "" "")"

echo "== a scenario whose name prefixes another isn't confused for it =="
# 2-6 vs 2-6x: prefix matching on the raw string would swallow the second.
SCOPE2="replaydata/agents/copilot/scenarios/2-6_long"
NEIGHBOUR="replaydata/agents/copilot/scenarios/2-6_longer-thing/recordings/r1/transcript.jsonl.replay.json.golden"
got="$(golden_restore_list "$SCOPE2" "" "$NEIGHBOUR")"
assert_eq "neighbouring scenario is restored, not claimed" "$NEIGHBOUR" "$got"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "golden-scope_test: ALL PASS"
else
  echo "golden-scope_test: $fails FAILED" >&2
  exit 1
fi
