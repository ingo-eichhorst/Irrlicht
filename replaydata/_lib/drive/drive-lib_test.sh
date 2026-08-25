#!/usr/bin/env bash
# drive-lib_test.sh — unit tests for the shared interactive-driver helpers
# extracted in #508 #3 (lib/drive/slots.sh + contracts.sh), #1009
# (lib/drive/dialogs.sh), and #1018 (lib/drive/teardown.sh). Plain bash, no
# framework. The interactive drivers themselves can't run in CI (they need a
# live agent CLI + tmux + daemon), so these pure helpers — slot bookkeeping,
# staging-contract emission, the mid-turn dialog-dismiss poll, and the
# session/pid-death polls, which touch only bash arrays + the filesystem
# (tmux/kill calls are faked below) — are the automated net for the
# extraction. Run directly or via scripts/smoke-test.sh.
#
# The last section grades something else: not a shared helper, but the
# `cleanup()` EXIT trap each interactive driver ships, extracted from the
# driver's own source text and executed here (#1825). See its banner for why
# that lives in this file rather than in a new one.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=slots.sh
source "$DIR/slots.sh"
# shellcheck source=contracts.sh
source "$DIR/contracts.sh"
# shellcheck source=dialogs.sh
source "$DIR/dialogs.sh"
# shellcheck source=teardown.sh
source "$DIR/teardown.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Driver-owned globals the helpers read/write.
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh (alloc_slot) and contracts.sh (emit_session_contract)
STAGING="$TMP"
DRIVER_LOG="$TMP/driver.log"
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh:56 (alloc_slot)
DRIVE_MARKER_PREFIX="$TMP/.fake-start-marker"
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_SESSION=()
# shellcheck disable=SC2034  # driver-owned slot array; read by the sourced replaydata/_lib/drive/slots.sh (save_active/load_slot) and contracts.sh (emit_session_contract)
SES_TRANSCRIPT=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_UUID=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_EXPECTED=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_MARKER=()
# shellcheck disable=SC2034  # driver-owned slot array written by the sourced replaydata/_lib/drive/slots.sh:63 (alloc_slot); kept current here for the shared slot model
SES_CWD=()
# shellcheck disable=SC2034  # driver-owned slot array written by the sourced replaydata/_lib/drive/slots.sh:64 (alloc_slot); kept current here for the shared slot model
SES_ALIVE=()
N_SLOTS=0; ACTIVE=0
SESSION=""; TRANSCRIPT=""; UUID=""; EXPECTED_TURNS=0; MARKER=""

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_eq() { local label="$1" expected="$2" actual="$3"; [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"; return 0; }

echo "== daemon_sid: basename minus .jsonl =="
assert_eq "rollout path → stem" "2026-05-28T10_abc" "$(daemon_sid /x/y/2026-05-28T10_abc.jsonl)"
assert_eq "empty → empty" "" "$(daemon_sid "")"

echo "== alloc_slot / save_active / load_slot round-trip across 2 slots =="
alloc_slot "tmux-1" "$TMP/cwd1"
assert_eq "slot1: N_SLOTS"   1          "$N_SLOTS"
assert_eq "slot1: ACTIVE"    1          "$ACTIVE"
assert_eq "slot1: SESSION"   "tmux-1"   "$SESSION"
assert_eq "slot1: marker honors DRIVE_MARKER_PREFIX" "$TMP/.fake-start-marker.1" "$MARKER"
[[ -f "$TMP/.fake-start-marker.1" ]] && pass "slot1: marker file created" || fail "slot1 marker file" exists missing
# Mutate the view, persist it, then allocate a 2nd slot.
TRANSCRIPT="/t/ses1.jsonl"; UUID="uuid-1"; EXPECTED_TURNS=3
save_active
alloc_slot "tmux-2" "$TMP/cwd2"
assert_eq "slot2: N_SLOTS"   2          "$N_SLOTS"
assert_eq "slot2: TRANSCRIPT cleared on alloc" "" "$TRANSCRIPT"
TRANSCRIPT="/t/ses2.jsonl"; UUID="uuid-2"; EXPECTED_TURNS=5
save_active
# Switch back to slot 1 — its persisted state must reload exactly.
load_slot 1
assert_eq "load slot1: TRANSCRIPT" "/t/ses1.jsonl" "$TRANSCRIPT"
assert_eq "load slot1: UUID"       "uuid-1"        "$UUID"
assert_eq "load slot1: EXPECTED"   3               "$EXPECTED_TURNS"
load_slot 2
assert_eq "load slot2: TRANSCRIPT" "/t/ses2.jsonl" "$TRANSCRIPT"
assert_eq "load slot2: UUID"       "uuid-2"        "$UUID"

echo "== emit_session_contract: primary + multi-session lists =="
EXIT_REASON="ok"
: > "$DRIVER_LOG.stdout.1"   # so the combined-stdout cat has something to read
emit_session_contract "primary-sid"
assert_eq "session.uuid = primary arg" "primary-sid" "$(cat "$TMP/session.uuid")"
assert_eq "transcript.path = slot1"    "/t/ses1.jsonl" "$(cat "$TMP/transcript.path")"
assert_eq "driver.exit-reason"         "ok"           "$(cat "$TMP/driver.exit-reason")"
assert_eq "session.uuids = daemon_sid per slot" "$(printf 'ses1\nses2')" "$(cat "$TMP/session.uuids")"
assert_eq "transcript.paths per slot"  "$(printf '/t/ses1.jsonl\n/t/ses2.jsonl')" "$(cat "$TMP/transcript.paths")"

echo "== drive_exit: EXIT_REASON → exit code =="
# EXIT_REASON is the INPUT to drive_exit, read by the sourced
# replaydata/_lib/drive/contracts.sh — each subshell below sets it and the
# function under test consumes it. One directive per assignment because a
# directive covers only the next command, and shellcheck picks a single site
# per variable to report: pinning only the one it happens to name today would
# leave the other three to fail the gate the moment that choice moved.
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/contracts.sh (drive_exit)
( EXIT_REASON="ok";            drive_exit ); assert_eq "ok → 0"            0   "$?"
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/contracts.sh (drive_exit)
( EXIT_REASON="timeout";       drive_exit ); assert_eq "timeout → 124"    124 "$?"
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/contracts.sh (drive_exit)
( EXIT_REASON="nonzero(2)";    drive_exit ); assert_eq "nonzero(2) → 2"   2   "$?"
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/contracts.sh (drive_exit)
( EXIT_REASON="weird";         drive_exit ); assert_eq "unknown → 1"      1   "$?"

echo "== dismiss_dialog_if_visible: poll+dismiss mechanics only (tmux faked) =="
# A fake `tmux` shell function shadows the real binary for the rest of this
# script (bash resolves a function before PATH), so the mechanics can be
# checked without a live tmux/agent: capture-pane replays $FAKE_PANE, and
# send-keys records its args in a plain variable (one call per test case,
# same shell — no file/mktemp needed) so a test can assert Enter reached the
# right session.
FAKE_PANE=""
SENDKEYS_LOG=""
tmux() {
  local subcmd="$1"
  case "$subcmd" in
    capture-pane) printf '%s\n' "$FAKE_PANE" ;;
    send-keys)    shift; SENDKEYS_LOG="$*" ;;
    *) ;;  # any other subcommand is uninteresting here — stay silent, succeed
  esac
  return 0
}

FAKE_PANE=$'some noise\nPermission for the bash tool\nmore noise'
SENDKEYS_LOG=""
dismiss_dialog_if_visible "vibe-sess" 'Permission for the|Allow for remainder of this session'
assert_eq "vibe marker present -> returns 0" 0 "$?"
assert_eq "sends Enter to the right session"  "-t vibe-sess Enter" "$SENDKEYS_LOG"

FAKE_PANE="nothing dialog-shaped here"
SENDKEYS_LOG=""
dismiss_dialog_if_visible "vibe-sess" 'Permission for the|Allow for remainder of this session'
assert_eq "marker absent -> returns 1" 1 "$?"
assert_eq "no Enter sent when marker absent" "" "$SENDKEYS_LOG"

FAKE_PANE="Requesting permission for run_command: rm -rf /tmp/x"
SENDKEYS_LOG=""
dismiss_dialog_if_visible "agy-sess" 'Requesting permission for|Do you want to proceed'
assert_eq "antigravity's own marker regex matches its own dialog" 0 "$?"
assert_eq "antigravity dismiss targets its own session" "-t agy-sess Enter" "$SENDKEYS_LOG"

FAKE_PANE="Permission for the bash tool"
SENDKEYS_LOG=""
dismiss_dialog_if_visible "agy-sess" 'Requesting permission for|Do you want to proceed'
assert_eq "adapters' marker regexes stay independent (vibe text doesn't trip agy's)" 1 "$?"

unset -f tmux

echo "== wait_tmux_session_gone: polls until has-session fails, capped at max_wait =="
# Fake tmux/sleep so the poll runs at wall-clock-zero speed: `sleep` is
# shadowed to a no-op tick counter (bash resolves a function before the
# builtin), and `has-session` returns true for a fixed number of calls before
# "the session is gone".
HAS_SESSION_CALLS=0; HAS_SESSION_TRUE_COUNT=0; SLEEP_CALLS=0
tmux() {
  local subcmd="$1" rc=0
  case "$subcmd" in
    has-session)
      HAS_SESSION_CALLS=$((HAS_SESSION_CALLS + 1))
      [[ $HAS_SESSION_CALLS -le $HAS_SESSION_TRUE_COUNT ]] || rc=1
      ;;
    *) ;;  # only has-session drives this poll — everything else is a no-op
  esac
  return "$rc"
}
sleep() { SLEEP_CALLS=$((SLEEP_CALLS + 1)); return 0; }

HAS_SESSION_TRUE_COUNT=0; HAS_SESSION_CALLS=0; SLEEP_CALLS=0
wait_tmux_session_gone "sess" 2
assert_eq "already gone: no sleep" 0 "$SLEEP_CALLS"
assert_eq "already gone: checks once" 1 "$HAS_SESSION_CALLS"

HAS_SESSION_TRUE_COUNT=3; HAS_SESSION_CALLS=0; SLEEP_CALLS=0
wait_tmux_session_gone "sess" 2
assert_eq "gone after 3 checks: sleeps 3x" 3 "$SLEEP_CALLS"
assert_eq "gone after 3 checks: checks 4x" 4 "$HAS_SESSION_CALLS"

HAS_SESSION_TRUE_COUNT=999; HAS_SESSION_CALLS=0; SLEEP_CALLS=0
wait_tmux_session_gone "sess" 1; rc=$?
assert_eq "never gone: capped at max_wait*5 ticks" 5 "$SLEEP_CALLS"
# LOCK, not a defect test (#1018, restated #1825): returning 0 on a capped-out
# poll is DELIBERATE here — this function is the settle used by callers that
# already killed the session themselves. The strict sibling below is what a
# caller with no other evidence must use. Do not "fix" this to return 1.
assert_eq "never gone: still returns 0 (deliberate best-effort contract)" 0 "$rc"

echo "== require_tmux_session_gone: strict sibling — non-zero when the cap expires =="
# Same fakes; the difference under test is purely the return contract.
HAS_SESSION_TRUE_COUNT=0; HAS_SESSION_CALLS=0; SLEEP_CALLS=0
require_tmux_session_gone "sess" 2; rc=$?
assert_eq "already gone: returns 0" 0 "$rc"
assert_eq "already gone: no sleep" 0 "$SLEEP_CALLS"
assert_eq "already gone: checks once" 1 "$HAS_SESSION_CALLS"

HAS_SESSION_TRUE_COUNT=3; HAS_SESSION_CALLS=0; SLEEP_CALLS=0
require_tmux_session_gone "sess" 2; rc=$?
assert_eq "gone after 3 checks: returns 0" 0 "$rc"
assert_eq "gone after 3 checks: sleeps 3x" 3 "$SLEEP_CALLS"
assert_eq "gone after 3 checks: checks 4x" 4 "$HAS_SESSION_CALLS"

# The arm the whole of #1825 turns on: the TUI ignored its exit key, the
# session is still there when the cap expires, and the caller MUST be told.
HAS_SESSION_TRUE_COUNT=999; HAS_SESSION_CALLS=0; SLEEP_CALLS=0
require_tmux_session_gone "sess" 1; rc=$?
[[ $rc -ne 0 ]] && pass "never gone: returns NON-ZERO" \
  || fail "never gone: returns NON-ZERO" "non-zero" "$rc"
assert_eq "never gone: same sleep budget as the best-effort poll" 5 "$SLEEP_CALLS"
# One observation per tick plus the final one that decided the failure — the
# verdict is a fresh has-session answer, never inferred from the tick counter.
assert_eq "never gone: verdict comes from an observation, not the counter" 6 "$HAS_SESSION_CALLS"

unset -f tmux sleep

echo "== wait_pid_gone: polls until kill -0 fails, capped at max_wait; no-op on empty pid =="
KILL_CALLS=0; KILL_TRUE_COUNT=0; SLEEP_CALLS=0
kill() {
  local sig="$1" rc=0
  if [[ "$sig" == "-0" ]]; then
    KILL_CALLS=$((KILL_CALLS + 1))
    [[ $KILL_CALLS -le $KILL_TRUE_COUNT ]] || rc=1
  fi
  return "$rc"
}
sleep() { SLEEP_CALLS=$((SLEEP_CALLS + 1)); return 0; }

wait_pid_gone "" 1
assert_eq "empty pid: no kill call"  0 "$KILL_CALLS"
assert_eq "empty pid: no sleep call" 0 "$SLEEP_CALLS"

KILL_TRUE_COUNT=2; KILL_CALLS=0; SLEEP_CALLS=0
wait_pid_gone "4242" 1
assert_eq "gone after 2 checks: sleeps 2x" 2 "$SLEEP_CALLS"
assert_eq "gone after 2 checks: checks 3x" 3 "$KILL_CALLS"

unset -f kill sleep

echo "== sigkill_and_wait: kills+waits when pid known, sleeps when empty =="
KILL9_LOG=""; KILL_CALLS=0; KILL_TRUE_COUNT=0; SLEEP_CALLS=0; SLEEP_ARG=""
kill() {
  local sig="$1" target="$2" rc=0
  if [[ "$sig" == "-9" ]]; then
    KILL9_LOG="$target"
  elif [[ "$sig" == "-0" ]]; then
    KILL_CALLS=$((KILL_CALLS + 1))
    [[ $KILL_CALLS -le $KILL_TRUE_COUNT ]] || rc=1
  fi
  return "$rc"
}
sleep() { local secs="$1"; SLEEP_CALLS=$((SLEEP_CALLS + 1)); SLEEP_ARG="$secs"; return 0; }

KILL_TRUE_COUNT=1; KILL_CALLS=0; SLEEP_CALLS=0; KILL9_LOG=""
sigkill_and_wait "4242" 1
assert_eq "known pid: sends kill -9"       "4242" "$KILL9_LOG"
assert_eq "known pid: waits via wait_pid_gone, not a flat sleep" 1 "$SLEEP_CALLS"

KILL9_LOG=""; SLEEP_CALLS=0; SLEEP_ARG=""
sigkill_and_wait "" 1
assert_eq "empty pid: no kill -9 sent" "" "$KILL9_LOG"
assert_eq "empty pid: falls back to a flat sleep" 1 "$SLEEP_CALLS"
assert_eq "empty pid: sleeps for max_wait"        1 "$SLEEP_ARG"

unset -f kill sleep

# =============================================================================
# The REACHED_EPILOGUE verdict guard, graded against every shipped cleanup()
# =============================================================================
# #1825 gave every interactive driver's EXIT trap a guard that REWRITES the
# verdict when the driver aborted before its epilogue: such a run never formed
# one, so EXIT_REASON still holds its initial "ok", and writing that reports
# SUCCESS for a failed run. Rewriting a verdict is the riskiest behaviour in
# that change and nothing in the tree executed it. The static tripwire
# (tools/onboarding-factory/internal/driverteardown, INV-4) checks only that a
# guard of that SHAPE exists, and says in its own doc comment that it cannot see
# whether the value written denotes failure or whether the sentinel is set in
# the right place. Running the handler is what closes that.
#
# HOW IT GETS AT THEM. A driver is a top-to-bottom script — sourcing one would
# launch a real agent under tmux — so each `cleanup()` is delimited in its driver
# by `# BEGIN cleanup` / `# END cleanup`, EXTRACTED from the driver's source text
# here, and eval'd against stubs. Same extract-and-execute idiom as
# scripts/lib/run-cell-multi-teardown_test.sh (run-cell-multi.sh's functions) and
# tools/lib/*_test.sh (a workflow step's `run:` block), and for the same reason:
# it stops this section grading a COPY of a handler that has drifted from the one
# that ships. A handler whose markers moved is a REFUSAL, not a silent pass —
# every arm below would otherwise grade an empty string.
#
# WHAT "DENOTES FAILURE" MEANS HERE, and why it is not a list of reason strings
# re-typed in a test: each written reason is fed back through `drive_exit` —
# contracts.sh's own EXIT_REASON→process-exit-code mapping, sourced at the top of
# this file — and the arms assert the CODE. So "the abort arm recorded a failure"
# is graded by the rig's own definition of failure.
#
# WHY THIS FILE and not a new cleanup-guard_test.sh: scripts/smoke-test.sh (the
# CI entry point, test.yml) names the replaydata/_lib/drive suites BY HAND, and
# the only directory it discovers by glob is scripts/lib/. A new file here would
# be a suite nothing runs — the same absence-reads-as-success shape #1825 is
# about. This file is already named there and already matches `*_test.sh`, so the
# section runs today and keeps running if that hand-written list becomes a
# shell_lib_suite_run over this directory. Split it out the moment it does.

echo ""
echo "== REACHED_EPILOGUE verdict guard: every driver's real cleanup(), executed (#1825) =="

# extract_block <file> <name> — the lines strictly between `# BEGIN <name>` and
# `# END <name>`.
extract_block() {
  awk -v n="$2" '
    $0 ~ ("^# BEGIN " n "([ \t]|$)") { inb = 1; next }
    inb && $0 ~ ("^# END " n "[ \t]*$") { inb = 0; next }
    inb { print }
  ' "$1"
}

# driver_constants <file> — the driver's own top-level LITERAL constant
# assignments (NONZERO_2='nonzero(2)', EXIT_DRIVER_FAULT="nonzero(2)", …). The
# fault reason a handler writes must come from the DRIVER, never be re-typed
# here, or these arms would grade this file's idea of the vocabulary. Two passes
# so neither pattern has to contain the other quote character; the double-quoted
# form excludes `$` and a backtick, so nothing eval'd from here can expand or run
# anything. A constant this misses is not a silent empty string: the arms run
# under `set -u`, so the handler aborts and the arm reports that nothing was
# written.
driver_constants() { # <driver>
  { grep -E "^[A-Z_][A-Z0-9_]*='[^']*'$" "$1" || true; }
  { grep -E '^[A-Z_][A-Z0-9_]*="[^"$`]*"$' "$1" || true; }
}

# run_cleanup_arm <driver> <block-src> <reached-epilogue> <initial-reason>
#   Executes the extracted handler and echoes the reason it wrote to
#   driver.exit-reason — or a loud NO-…-WRITTEN token carrying the handler's
#   stderr, so "the handler recorded nothing" can never read as "the handler
#   recorded the empty string".
run_cleanup_arm() {
  local file="$1" src="$2" reached="$3" initial="$4" dir consts
  dir="$(mktemp -d "$TMP/arm.XXXXXX")"
  consts="$(driver_constants "$file")"
  # shellcheck disable=SC2034  # every assignment in this subshell is read by the
  # EVAL'd cleanup() extracted from the driver above, which the linter cannot
  # follow. Deleting one would abort the arm under `set -u`, not weaken it.
  (
    set -u   # the drivers run under `set -euo pipefail`; -u is the half that
             # turns a constant this harness failed to supply into a loud abort
             # rather than an empty expansion.
    tmux() { return 0; }               # every handler's kill-session
    kill_tree() { return 0; }          # gemini-cli
    restore_settings() { return 0; }   # hermes
    N_SLOTS=1
    SES_SESSION=("" "fake-tmux-1")
    SES_TMUX=("" "fake-tmux-1")        # claudecode names its slot array this
    SES_PANE_PID=("" "424242")         # gemini-cli
    SESSION="fake-tmux-1"              # aider has no slot model, just a scalar
    STAGING="$dir"
    eval "$consts"
    eval "$src"
    REACHED_EPILOGUE="$reached"
    EXIT_REASON="$initial"
    cleanup
  ) >/dev/null 2>"$dir/arm.stderr"
  if [[ -f "$dir/driver.exit-reason" ]]; then
    cat "$dir/driver.exit-reason"
  else
    printf 'NO-driver.exit-reason-WRITTEN(%s)' "$(tr '\n' ' ' < "$dir/arm.stderr")"
  fi
  return 0
}

# drive_exit_code <reason> — what contracts.sh's OWN mapping turns that reason
# into. Subshelled because drive_exit never returns; it exits.
drive_exit_code() {
  local rc=0
  # shellcheck disable=SC2034  # EXIT_REASON is drive_exit's only input; the
  # linter cannot see the read because it lives in the sourced contracts.sh.
  ( EXIT_REASON="$1"; drive_exit ) || rc=$?
  echo "$rc"
  return 0
}

# strip_guard <block-src> — the MUTATION: delete the guard's `if … fi` so the
# handler writes "$EXIT_REASON" unconditionally, which is what every driver did
# before #1825 added the guard.
strip_guard() {
  awk '
    /if \[\[ "\$REACHED_EPILOGUE"/ { drop = 1; next }
    drop && /^[[:space:]]*fi[[:space:]]*$/ { drop = 0; next }
    drop { next }
    { print }
  ' <<< "$1"
}

assert_ne() {
  local label="$1" unexpected="$2" actual="$3"
  [[ "$actual" != "$unexpected" ]] && pass "$label (got [$actual])" || fail "$label" "anything but [$unexpected]" "$actual"
  return 0
}
assert_nonzero() {
  local label="$1" actual="$2"
  [[ "$actual" =~ ^[0-9]+$ && "$actual" -ne 0 ]] && pass "$label (exit $actual)" || fail "$label" "a non-zero exit code" "$actual"
  return 0
}

# --- Enumerate the subjects FROM DISK --------------------------------------
# Not a hand-written list: a driver added tomorrow is graded by this section
# without anyone remembering to add it, and an enumeration that comes back
# empty REFUSES rather than reporting a clean run over nothing.
AGENTS_DIR="$(cd "$DIR/../../agents" && pwd)"
TEMPLATE="$DIR/../../../tools/onboarding-factory/scripts/templates/drive-interactive.sh.tmpl"

ALL_DRIVERS=()
for f in "$AGENTS_DIR"/*/driver-interactive.sh; do
  [[ -e "$f" ]] && ALL_DRIVERS+=("$f")
done
if [[ ${#ALL_DRIVERS[@]} -eq 0 ]]; then
  echo "drive-lib_test: REFUSING — no */driver-interactive.sh under $AGENTS_DIR." >&2
  echo "  This section would have graded nothing and printed ALL PASS." >&2
  exit 1
fi

# The scaffold template mints the NEXT driver, so its handler is graded too — a
# template that lost the guard would hand it to every adapter onboarded after.
if [[ ! -r "$TEMPLATE" ]]; then
  echo "drive-lib_test: REFUSING — cannot read the scaffold template at $TEMPLATE." >&2
  echo "  If it moved, point this at the new path; a missing scaffold must not read as a clean run." >&2
  exit 1
fi

HANDLERS=(); HANDLER_NAMES=(); INLINE_TRAP_NAMES=()
for f in "${ALL_DRIVERS[@]}"; do
  agent="$(basename "$(dirname "$f")")"
  if grep -q '^cleanup() {' "$f"; then
    HANDLERS+=("$f"); HANDLER_NAMES+=("$agent")
  else
    INLINE_TRAP_NAMES+=("$agent")
  fi
done
if [[ ${#HANDLERS[@]} -eq 0 ]]; then
  echo "drive-lib_test: REFUSING — none of the ${#ALL_DRIVERS[@]} drivers defines a top-level cleanup()." >&2
  echo "  Either the handlers were renamed or this section stopped being able to find them." >&2
  exit 1
fi
HANDLERS+=("$TEMPLATE"); HANDLER_NAMES+=("scaffold-template")

# The hole this closes: a driver that declares the sentinel but whose handler
# this section cannot reach would be silently ungraded.
for f in "${ALL_DRIVERS[@]}"; do
  if grep -q '^REACHED_EPILOGUE=' "$f" && ! grep -q '^cleanup() {' "$f"; then
    echo "drive-lib_test: REFUSING — $f declares REACHED_EPILOGUE but defines no cleanup()." >&2
    echo "  Its guard would be ungraded here. Give it a marked cleanup(), or grade its trap another way." >&2
    exit 1
  fi
done

echo "  census: ${#ALL_DRIVERS[@]} driver-interactive.sh on disk, $(( ${#HANDLERS[@]} - 1 )) with a cleanup() handler, +1 scaffold template = ${#HANDLERS[@]} handlers graded"
echo "  census: graded: ${HANDLER_NAMES[*]}"
if [[ ${#INLINE_TRAP_NAMES[@]} -gt 0 ]]; then
  echo "  census: no cleanup() (inline EXIT trap, no REACHED_EPILOGUE sentinel): ${INLINE_TRAP_NAMES[*]}"
fi

# --- Three arms per handler, plus its guard-deleted mutant ------------------
ARMS_GRADED=0
MUTANTS_GRADED=0
for idx in "${!HANDLERS[@]}"; do
  f="${HANDLERS[$idx]}"; agent="${HANDLER_NAMES[$idx]}"
  src="$(extract_block "$f" cleanup)"
  if [[ -z "$src" ]]; then
    echo "drive-lib_test: REFUSING — no '# BEGIN cleanup' … '# END cleanup' block in $f." >&2
    echo "  Every arm for $agent would have graded an empty string. Restore the markers or rename them here too." >&2
    exit 1
  fi
  if ! grep -q '^cleanup() {' <<< "$src"; then
    echo "drive-lib_test: REFUSING — the cleanup marker block in $f does not define cleanup()." >&2
    exit 1
  fi

  # ARM 1 — aborted before the epilogue, no verdict ever formed. EXIT_REASON is
  # still its initial "ok"; recording that is the defect #1825 exists to stop.
  r="$(run_cleanup_arm "$f" "$src" 0 ok)"
  assert_ne     "$agent: abort with no verdict is NOT recorded as ok" "ok" "$r"
  assert_nonzero "$agent:   …and drive_exit reads what it wrote as a FAILURE" "$(drive_exit_code "$r")"

  # ARM 2 — aborted, but a verdict WAS formed. The guard must not overwrite it:
  # a timeout reported as a driver fault misdirects every triage that follows.
  r="$(run_cleanup_arm "$f" "$src" 0 timeout)"
  assert_eq "$agent: a verdict already formed survives the abort path" "timeout" "$r"
  assert_eq "$agent:   …and still maps to timeout's own exit code" "124" "$(drive_exit_code "$r")"

  # ARM 3 — the epilogue ran. EXIT_REASON is this run's real verdict.
  r="$(run_cleanup_arm "$f" "$src" 1 ok)"
  assert_eq "$agent: epilogue reached → ok is recorded as-is" "ok" "$r"

  ARMS_GRADED=$((ARMS_GRADED + 3))

  # MUTATION (AGENTS.md: a check a change ADDS has no "before the fix" to run
  # red, so mutate the thing it protects and COMMIT the mutation). The mutant is
  # DERIVED from the shipped block on every run rather than pinned as a static
  # copy, so it cannot drift away from the handler it mutates — and a deletion
  # that changed nothing is a refusal, because the arm below would then be
  # grading the unmutated handler and would pass for the wrong reason.
  mut="$(strip_guard "$src")"
  if [[ "$mut" == "$src" ]]; then
    echo "drive-lib_test: REFUSING — deleting the guard changed nothing in $f's cleanup()." >&2
    echo "  The guard's shape moved, so the counterfactual below would grade the UNMUTATED handler." >&2
    exit 1
  fi
  if grep -q 'REACHED_EPILOGUE' <<< "$mut"; then
    echo "drive-lib_test: REFUSING — $f's mutant still references REACHED_EPILOGUE; the deletion was partial." >&2
    exit 1
  fi
  r="$(run_cleanup_arm "$f" "$mut" 0 ok)"
  assert_eq "$agent: MUTANT (guard deleted) records ok for an aborted run" "ok" "$r"
  assert_eq "$agent:   …which drive_exit reads as SUCCESS — the defect the guard prevents" "0" "$(drive_exit_code "$r")"
  MUTANTS_GRADED=$((MUTANTS_GRADED + 1))
done

# The census has to add up: a `continue` or an early `break` added to the loop
# above would otherwise leave handlers ungraded and this section still green.
assert_eq "every handler was graded on all three arms" "$(( ${#HANDLERS[@]} * 3 ))" "$ARMS_GRADED"
assert_eq "every handler was also graded against its guard-deleted mutant" "${#HANDLERS[@]}" "$MUTANTS_GRADED"

# --- The exit_clean cap is ONE number, in one place -------------------------
# A LOCK, not a defect test: it pins what the #1825 review's finding 3 settled
# rather than reproducing a bug. Six drivers passed a literal 2 that had been
# inherited from a pre-#1018 `sleep 2` — free to overrun as a settle, ruinous as
# the hard deadline the strict poll turned it into. They now pass
# DRIVE_EXIT_CLEAN_CAP_S, whose definition in teardown.sh carries the
# justification. A literal creeping back in is a number with no justification
# attached, and the next one need not match the others.
echo ""
echo "== the exit_clean cap is DRIVE_EXIT_CLEAN_CAP_S everywhere, never a literal (#1825) =="
assert_eq "teardown.sh defines the constant" "15" "${DRIVE_EXIT_CLEAN_CAP_S:-<undefined>}"

CAP_SITES=0
CAP_LITERAL_SITES=""
for f in "${ALL_DRIVERS[@]}"; do
  while IFS= read -r hit; do
    [[ -z "$hit" ]] && continue
    lineno="${hit%%:*}"; text="${hit#*:}"
    # Skip lines that are pure comment — several drivers discuss the poll in
    # prose. The line-number prefix is stripped FIRST; testing the raw grep -n
    # output for a leading `#` never matches and would let a real literal hide.
    trimmed="${text#"${text%%[![:space:]]*}"}"
    [[ "$trimmed" == '#'* ]] && continue
    CAP_SITES=$((CAP_SITES + 1))
    case "$text" in
      *'require_tmux_session_gone "'*'" "$DRIVE_EXIT_CLEAN_CAP_S"'*) ;;
      *) CAP_LITERAL_SITES="$CAP_LITERAL_SITES $(basename "$(dirname "$f")"):$lineno" ;;
    esac
  done < <(grep -n 'require_tmux_session_gone ' "$f" || true)
done
# Zero call sites means the grep stopped matching, not that every site is
# compliant — the two must not print the same verdict.
if [[ "$CAP_SITES" -eq 0 ]]; then
  echo "drive-lib_test: REFUSING — no require_tmux_session_gone call site found in any driver." >&2
  echo "  The cap check below would have passed over an empty set." >&2
  exit 1
fi
echo "  census: $CAP_SITES require_tmux_session_gone call site(s) across ${#ALL_DRIVERS[@]} drivers"
assert_eq "no call site passes a literal cap" "" "$CAP_LITERAL_SITES"

# --- Every driver run-cell.sh exec()s is still EXECUTABLE -------------------
# A LOCK, not a defect test: nothing here is broken, and its green is not
# evidence of a fix. run-cell.sh:554 already guards
# (`[[ -x "$DRIVER" ]] || { echo "driver missing: $DRIVER" >&2; exit 1; }`), so a
# cleared exec bit DOES fail loudly — but only at record time, on a live run that
# has already spawned a daemon and is about to drive a real agent CLI. This moves
# the same finding into the suite, where it costs nothing.
#
# It earns its place because the failure is reachable by an ORDINARY edit rather
# than by carelessness: a scripted `awk … > tmp && mv tmp file` is the obvious
# way to make one change across eleven drivers, and it silently replaces every
# file it touches with a fresh 0644 one. That happened while the section above
# was being written and was caught by hand; nothing in the tree would have
# caught it, and the diff shows a mode change only if you go looking for one.
#
# BOTH driver kinds, because run-cell.sh chooses between them on $SCRIPT_JSON
# and exec()s whichever it picked: driver-interactive.sh for interactive cells,
# driver.sh for headless `prompt` cells.
echo ""
echo "== every driver run-cell.sh exec()s is executable (#1825) =="
EXEC_CANDIDATES=()
for f in "$AGENTS_DIR"/*/driver-interactive.sh "$AGENTS_DIR"/*/driver.sh; do
  [[ -e "$f" ]] && EXEC_CANDIDATES+=("$f")
done
# Zero drivers and eleven compliant drivers must not print the same verdict.
if [[ ${#EXEC_CANDIDATES[@]} -eq 0 ]]; then
  echo "drive-lib_test: REFUSING — no driver-interactive.sh or driver.sh under $AGENTS_DIR." >&2
  echo "  A tripwire that finds no drivers and prints ALL PASS is the failure it exists to prevent." >&2
  exit 1
fi

NOT_EXEC=""
N_HEADLESS=0
for f in "${EXEC_CANDIDATES[@]}"; do
  [[ "$f" == */driver.sh ]] && N_HEADLESS=$((N_HEADLESS + 1))
  # The offending PATH, not a count: "one driver is not executable" sends the
  # reader back through the whole fleet by hand.
  [[ -x "$f" ]] || NOT_EXEC="$NOT_EXEC $f"
done
echo "  census: ${#EXEC_CANDIDATES[@]} driver(s) checked — $(( ${#EXEC_CANDIDATES[@]} - N_HEADLESS )) interactive + $N_HEADLESS headless"
# Ties this census to the one the guard section already refused on when empty,
# so the two enumerations cannot silently disagree about what a driver is.
assert_eq "the interactive census matches the guard section's enumeration" \
  "${#ALL_DRIVERS[@]}" "$(( ${#EXEC_CANDIDATES[@]} - N_HEADLESS ))"
assert_eq "no driver has lost its exec bit" "" "$NOT_EXEC"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "drive-lib_test: ALL PASS"
else
  echo "drive-lib_test: $fails FAILED" >&2
  exit 1
fi
