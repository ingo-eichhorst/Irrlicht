#!/usr/bin/env bash
# drive-lib_test.sh — unit tests for the shared interactive-driver helpers
# extracted in #508 #3 (lib/drive/slots.sh + contracts.sh) and #1009
# (lib/drive/dialogs.sh). Plain bash, no framework. The interactive drivers
# themselves can't run in CI (they need a live agent CLI + tmux + daemon), so
# these pure helpers — slot bookkeeping, staging-contract emission, and the
# mid-turn dialog-dismiss poll, which touch only bash arrays + the filesystem
# (dialogs.sh's tmux calls are faked below) — are the automated net for the
# extraction. Run directly or via scripts/smoke-test.sh.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=slots.sh
source "$DIR/slots.sh"
# shellcheck source=contracts.sh
source "$DIR/contracts.sh"
# shellcheck source=dialogs.sh
source "$DIR/dialogs.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Driver-owned globals the helpers read/write.
STAGING="$TMP"
DRIVER_LOG="$TMP/driver.log"
DRIVE_MARKER_PREFIX="$TMP/.fake-start-marker"
SES_SESSION=(); SES_TRANSCRIPT=(); SES_UUID=(); SES_EXPECTED=()
SES_MARKER=(); SES_CWD=(); SES_ALIVE=()
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
( EXIT_REASON="ok";            drive_exit ); assert_eq "ok → 0"            0   "$?"
( EXIT_REASON="timeout";       drive_exit ); assert_eq "timeout → 124"    124 "$?"
( EXIT_REASON="nonzero(2)";    drive_exit ); assert_eq "nonzero(2) → 2"   2   "$?"
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
  case "$1" in
    capture-pane) printf '%s\n' "$FAKE_PANE" ;;
    send-keys)    shift; SENDKEYS_LOG="$*" ;;
  esac
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

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "drive-lib_test: ALL PASS"
else
  echo "drive-lib_test: $fails FAILED" >&2
  exit 1
fi
