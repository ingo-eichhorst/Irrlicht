#!/usr/bin/env bash
# opencode-v2-driver-mutations_test.sh — committed mutation fixture for #2109
# P5a's API driver (replaydata/agents/opencode-v2/). Each mutation breaks one of
# the driver's own guards and requires its stub-service test
# (tools/onboarding-factory/scripts/lib/opencode-v2-driver_test.sh) to go red
# with the named message:
#
#   1. wait_turn without its deadline → a service that never idles is no
#      longer ended as `timeout` (the test's watchdog kills the driver);
#   2. the one-service check accepting any count → the refusal no longer
#      names the count (measured: the run is still refused, by the next guard —
#      the one running service must be the one service.json names — so what
#      this guard adds is the reason an operator can act on);
#   3. no location boot before the plugin wait → the plugin never logs a load
#      and the run is refused (measured live: a created session does not boot
#      its location, #2109's first opencode-v2 run);
#   4. the plugin-load check ignoring "failed to load plugin" → a run with a
#      plugin the service rejected is driven.
#
# P5b-1's steps (#2109), each guard mutated and its refusal case gone red:
#   5. a {"session": N} target for a slot that does not exist accepted;
#   6. a session step with no slot accepted;
#   7. the model step sending an unchecked provider/model;
#   8. revert staging an id that is not a message id;
#   9. revert's turn count unchecked;
#  10. "fewer user messages than asked" read as an id;
#  11. await_child using a child id that is not a session id;
#  12. a recipe that signals the service run without the service-window
#      opt-in (OC2_SERVICE_WINDOW=1), checked before any session exists;
#  13. sigkill without re-checking that the pid is still the service;
#  14. a switch that does not save the active slot's idle mark (review F1);
#  15. reset_session archiving the old session before the new one exists
#      (review F2).
#
# Mutation 1 runs until the test's watchdog (the cell deadline + 8s); the
# driver's readiness waits run on the test's short knobs. Kept apart from
# opencode-v2-rig-mutations_test.sh because it drives the stub service.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
LOCK_TEST="tools/onboarding-factory/scripts/lib/opencode-v2-driver_test.sh"

[[ -x "$MUTATE_SH" ]] || { echo "FAIL: opencode-v2-driver-mutations — $MUTATE_SH is missing or not executable" >&2; exit 1; }

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "opencode-v2-driver-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/opencode-v2-driver-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0
quote_output() { sed 's/^/      | /'; return 0; }

assert_red() { # <label> <file> <anchor> <replacement> <want> <section>
  local label="$1" out rc
  # Only the section that proves this mutation runs (the lock test's
  # OC2_DRIVER_TEST_ONLY): each mutation then costs seconds, not the whole
  # suite. A section name the lock test does not have fails it without a FAIL:
  # line, which the "could not run" branch below reports.
  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$2" "$3" "$4" \
    bash -c "OC2_DRIVER_TEST_ONLY='$6' bash $LOCK_TEST 2>&1; echo SH_TEST_RC=\$?" 2>&1)"; rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc): a STALE or ambiguous anchor, not a clean guard."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if grep -q 'SH_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the driver test stayed GREEN under the mutation."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if ! grep -q 'FAIL:' <<<"$out"; then
    echo "FAIL: $label — the driver test exited non-zero WITHOUT reporting a failure: it could"
    echo "      not run under the mutation, which is not evidence about the guard."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if grep -q 'syntax error' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the driver's parse, not the guard."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if ! grep -qF -- "$5" <<<"$out"; then
    echo "FAIL: $label — the driver test failed, but not with the expected message."
    echo "      wanted to find: $5"
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  echo "ok  $label"
}

# The section filter itself: a name the lock test does not have must fail it,
# with no FAIL: line — so a typo below reads as "could not run", never green.
filter_out="$(cd "$REPO_ROOT" && OC2_DRIVER_TEST_ONLY=no-such-section bash "$LOCK_TEST" 2>&1)"; filter_rc=$?
if [[ $filter_rc -eq 0 ]] || grep -q 'FAIL:' <<<"$filter_out" || ! grep -q "no section named 'no-such-section'" <<<"$filter_out"; then
  echo "FAIL: the lock test's section filter accepted a section it does not have (rc $filter_rc)"
  echo "$filter_out" | quote_output; fails=$((fails + 1))
else
  echo "ok  the lock test refuses an unknown section"
fi

DRIVER="replaydata/agents/opencode-v2/driver-interactive.sh"
SERVICE_API="replaydata/agents/opencode-v2/service-api.sh"

# ── 1. wait_turn without its deadline ──
assert_red "wait_turn ignoring the deadline" "$DRIVER" \
  $'    if (( $(remaining_seconds) <= 0 )); then\n      log "wait_turn: no new idle' \
  $'    if false; then\n      log "wait_turn: no new idle' \
  "FAIL: exit reason timeout" never

# ── 2. the one-service check accepting any count ──
assert_red "two services accepted" "$SERVICE_API" \
  '  if [[ "$n" != "1" ]]; then' '  if [[ "$n" == "0" ]]; then' \
  "FAIL: …naming the count" refusals

# ── 3. no location boot ──
assert_red "no location boot before the plugin wait" "$DRIVER" \
  '  if ! oc2_api GET "/api/command?location%5Bdirectory%5D=' \
  '  if false && ! oc2_api GET "/api/command?location%5Bdirectory%5D=' \
  "FAIL: exit reason ok" turn

# ── 4. a failed plugin load ignored ──
assert_red "a failed plugin load ignored" "$SERVICE_API" \
  $'  grep -qF "failed to load plugin\\" target=$plugin " <<<"$tail_lines" && return 2\n' "" \
  "FAIL: exit reason nonzero(2) — expected [nonzero(2)] got [ok]" badplugin

# ── 5. an unknown slot accepted ──
assert_red "an unknown session slot accepted" "$DRIVER" \
  '  if [[ ! "$n" =~ ^[1-9][0-9]*$ ]] || (( n > ${#SLOT_SID[@]} )); then' '  if false; then' \
  "wanted to find [invalid session slot '3' (have 1)]" slots

# ── 6. a session step with no slot accepted ──
assert_red "a session step with no slot accepted" "$DRIVER" \
  '    session)          [[ -n "$tgt" ]] || {' '    session)          true || {' \
  "FAIL: a session step with no slot: refused" slots

# ── 7. the model step unchecked ──
assert_red "an unchecked model ref sent" "$DRIVER" \
  '  if ! oc2_valid_model_ref "$provider" "$model"; then' '  if false; then' \
  "FAIL: a malformed model id: refused" model

# ── 8. revert staging a non-message id ──
assert_red "a revert target that is not a message id staged" "$DRIVER" \
  '  if ! oc2_valid_message_id "$mid"; then' '  if false; then' \
  "FAIL: a revert target that is not a message id: refused" revert

# ── 9. revert's turn count unchecked ──
assert_red "revert's turn count unchecked" "$DRIVER" \
  '  if [[ ! "$turns" =~ ^[1-9][0-9]*$ ]]; then' '  if false; then' \
  "wanted to find [turns must be a positive integer]" revert

# ── 10. no such user message read as an id ──
assert_red "too few user messages read as an id" "$SERVICE_API" \
  $'  [[ -n "$out" ]] || { echo "[driver] $sid has fewer than $n user message(s) in $db" >&2; return 1; }\n' "" \
  "wanted to find [has fewer than 2 user message(s)]" revert

# ── 11. await_child using a non-session child id ──
assert_red "a child id that is not a session id used" "$DRIVER" \
  '  if ! oc2_valid_sid "$child"; then' '  if false; then' \
  "wanted to find [not a session id]" await_child

# ── 12. sigkill without the service-window opt-in ──
assert_red "the service signalled without a service window" "$DRIVER" \
  'if [[ "${OC2_SERVICE_WINDOW:-}" != "1" ]] && jq -e' 'if false && jq -e' \
  "FAIL: …and the service was not signalled" window

# ── 13. sigkill without re-checking the pid ──
assert_red "the service pid not re-checked before the signal" "$DRIVER" \
  $'  if ! oc2_is_service_args $args; then\n    log "$sig: refusing' $'  if false; then\n    log "$sig: refusing' \
  "FAIL: …and it was not signalled" window

# ── 14. a switch that loses the active slot's idle mark ──
assert_red "a slot switch without saving the idle mark" "$DRIVER" \
  $'  save_active\n  load_slot "$n"\n' $'  load_slot "$n"\n' \
  "FAIL: slot 2's idle mark survived the switch to slot 1" slots

# ── 15. reset_session archiving first ──
assert_red "reset_session archiving before the new session exists" "$DRIVER" \
  $'  new_session || return 1\n  step_set_archived true "$old" || return 1\n' $'  step_set_archived true "$old" || return 1\n  new_session || return 1\n' \
  "FAIL: …after the new one existed" reset

if [[ $fails -gt 0 ]]; then
  echo "opencode-v2-driver-mutations: $fails FAILED"
  exit 1
fi
echo "opencode-v2-driver-mutations: ALL PASS"
