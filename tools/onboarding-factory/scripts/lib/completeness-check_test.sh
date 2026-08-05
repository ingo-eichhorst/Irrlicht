#!/usr/bin/env bash
# completeness-check_test.sh — unit tests for completeness-check.sh. Plain bash +
# jq (no framework), matching the style of classify-failure_test.sh. Run
# directly or via scripts/smoke-test.sh.
#
# completeness-check.sh is invoked as a subprocess and emits one JSON object, so
# each case builds a fake staging dir and asserts on .verdict / .reasons.
#
# Why this exists (#1333 / finding A3): two copilot recordings were torn down
# mid-turn and still reported `driver.exit-reason: ok` — `2-6` ended on
# debounce_coalesced with no final transition, `3-5` captured three subagent
# starts and zero completions. `ok` invites promotion, so both were caught only
# because a human read the events by hand before promoting. These assertions are
# that read, mechanized.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$DIR/completeness-check.sh"

command -v jq >/dev/null || { echo "completeness-check_test: jq is required" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }

# Build a staging dir from a heredoc of events.jsonl lines.
# usage: stage <name> [session-uuid ...] <<'EOF' ... EOF
stage() {
  local name="$1"; shift
  local d="$TMP/$name"
  mkdir -p "$d"
  cat > "$d/events.jsonl"
  if [[ $# -gt 0 ]]; then printf '%s\n' "$@" > "$d/session.uuids"; fi
  echo "$d"
}

assert_verdict() {
  local label="$1" expected="$2" staging="$3"
  local got
  got="$(bash "$SCRIPT" "$staging" | jq -r '.verdict')"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

assert_reason() {
  local label="$1" expected="$2" staging="$3"
  local got
  got="$(bash "$SCRIPT" "$staging" | jq -r '.reasons | join(",")')"
  [[ "$got" == *"$expected"* ]] && pass "$label" || fail "$label" "*$expected*" "$got"
  return 0
}

sid="fc7a4387-b5ae-447f-bc8f-78d1eb5faae3"

echo "== complete: a clean run that settles =="
d="$(stage clean "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"transcript_new","session_id":"proc-100","adapter":"copilot"}
{"seq":2,"ts":"2026-08-05T17:19:01Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":3,"ts":"2026-08-05T17:19:20Z","kind":"transcript_activity","session_id":"$sid"}
{"seq":4,"ts":"2026-08-05T17:19:40Z","kind":"state_transition","session_id":"$sid","prev_state":"working","new_state":"ready"}
{"seq":5,"ts":"2026-08-05T17:19:41Z","kind":"process_exited","session_id":"$sid","pid":123}
EOF
)"
assert_verdict "settles then exits -> complete" "complete" "$d"

echo "== suspect: torn down mid-turn, ends on debounce_coalesced (the 2-6 shape) =="
d="$(stage truncated "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:20Z","kind":"transcript_activity","session_id":"$sid"}
{"seq":3,"ts":"2026-08-05T17:19:22Z","kind":"debounce_coalesced","session_id":"$sid"}
EOF
)"
assert_verdict "ends on unresolved activity -> suspect" "suspect" "$d"
assert_reason "names the trailing-activity reason" "trailing_unresolved_activity" "$d"
assert_reason "names the unsettled session" "unsettled_session" "$d"

echo "== suspect: driven session left working even though the tail is resolved =="
d="$(stage unsettled "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:30Z","kind":"transcript_removed","session_id":"$sid"}
EOF
)"
assert_verdict "last transition is working -> suspect" "suspect" "$d"
assert_reason "names the unsettled session" "unsettled_session" "$d"

echo "== complete: waiting is a settle, not a truncation =="
d="$(stage waiting "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:30Z","kind":"state_transition","session_id":"$sid","prev_state":"working","new_state":"waiting"}
EOF
)"
assert_verdict "settling to waiting -> complete" "complete" "$d"

echo "== opt-out: a cell that documents its unsettled ending =="
d="$(stage optout "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:30Z","kind":"transcript_removed","session_id":"$sid"}
EOF
)"
echo '{"ends_unsettled": true}' > "$d/completeness-waiver.json"
assert_verdict "declared ends_unsettled -> complete" "complete" "$d"
assert_reason "records the waiver" "waived" "$d"

echo "== a proc- id is TERMINAL when no real session appears (the aider shape) =="
# aider's daemon-side ids are `proc-<pid>` for the whole session — the placeholder
# never reconciles into anything. An unconditional proc- exclusion made this
# assertion a no-op for that entire adapter: 29 of 370 committed recordings have
# zero non-proc transitions and all 29 are aider.
d="$(stage aiderish "proc-70465" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"proc-70465","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:30Z","kind":"transcript_removed","session_id":"proc-70465"}
EOF
)"
assert_verdict "unsettled proc- session is caught" "suspect" "$d"
assert_reason "and it is named" "proc-70465" "$d"

d="$(stage aiderish_ok "proc-70465" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"proc-70465","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:30Z","kind":"state_transition","session_id":"proc-70465","prev_state":"working","new_state":"ready"}
EOF
)"
assert_verdict "a settled proc- session still passes" "complete" "$d"

echo "== the waiver comes from the COMMITTED catalog, not just a staging file =="
# meta.ends_unsettled in replaydata/agents/scenarios.json is the durable home:
# a staging-only waiver was unreachable, since run-cell.sh creates the staging
# dir, drives the agent and runs this check in one unbroken sequence.
d="$(stage catalogwaiver "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:30Z","kind":"transcript_removed","session_id":"$sid"}
EOF
)"
got="$(bash "$SCRIPT" "$d" --scenario 1-2_session-end | jq -r '.verdict')"
[[ "$got" == "complete" ]] && pass "a declared ends_unsettled scenario is waived" \
                           || fail "a declared ends_unsettled scenario is waived" "complete" "$got"
got="$(bash "$SCRIPT" "$d" --scenario 2-1_basic-turn | jq -r '.verdict')"
[[ "$got" == "suspect" ]] && pass "an undeclared scenario is NOT waived" \
                          || fail "an undeclared scenario is NOT waived" "suspect" "$got"
got="$(bash "$SCRIPT" "$d" | jq -r '.verdict')"
[[ "$got" == "suspect" ]] && pass "no --scenario means no waiver" \
                          || fail "no --scenario means no waiver" "suspect" "$got"

echo "== presession proc- rows are not driven sessions =="
d="$(stage presession "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"proc-80648","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:01Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":3,"ts":"2026-08-05T17:19:40Z","kind":"state_transition","session_id":"$sid","prev_state":"working","new_state":"ready"}
EOF
)"
assert_verdict "a working proc- row alone doesn't fire" "complete" "$d"

echo "== without session.uuids, every non-proc session counts =="
d="$(stage nouuids <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:40Z","kind":"state_transition","session_id":"$sid","prev_state":"working","new_state":"ready"}
EOF
)"
assert_verdict "falls back to all real sessions" "complete" "$d"

echo "== a child session killed mid-flight (the 3-5 shape) =="
child="agent-ad85cefda5579a437"
d="$(stage child "$sid" "$child" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:05Z","kind":"state_transition","session_id":"$child","prev_state":"ready","new_state":"working"}
{"seq":3,"ts":"2026-08-05T17:19:40Z","kind":"state_transition","session_id":"$sid","prev_state":"working","new_state":"ready"}
EOF
)"
assert_verdict "child left working -> suspect" "suspect" "$d"
assert_reason "names the child session" "$child" "$d"

echo "== no state transitions at all =="
d="$(stage notransitions "$sid" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"transcript_new","session_id":"proc-100","adapter":"copilot"}
EOF
)"
assert_verdict "no transitions -> suspect" "suspect" "$d"
assert_reason "names the no-transition reason" "no_state_transitions" "$d"

echo "== run-cell.sh staging ROOT shape (events nested under replaydata/) =="
d="$TMP/stagingroot"
mkdir -p "$d/replaydata/agents/copilot/scenarios/2-6_long-agentic-session-stress"
printf '%s\n' "$sid" > "$d/session.uuids"
cat > "$d/replaydata/agents/copilot/scenarios/2-6_long-agentic-session-stress/events.jsonl" <<EOF
{"seq":1,"ts":"2026-08-05T17:19:00Z","kind":"state_transition","session_id":"$sid","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-08-05T17:19:22Z","kind":"debounce_coalesced","session_id":"$sid"}
EOF
assert_verdict "nested staging tree is found" "suspect" "$d"
assert_reason "and reports the trailing tail" "trailing_unresolved_activity" "$d"

echo "== missing staging dir / events.jsonl is not a crash =="
assert_verdict "missing dir -> unknown" "unknown" "$TMP/does-not-exist"
d="$TMP/noevents"; mkdir -p "$d"
assert_verdict "missing events.jsonl -> unknown" "unknown" "$d"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "completeness-check_test: ALL PASS"
else
  echo "completeness-check_test: $fails FAILED" >&2
  exit 1
fi
