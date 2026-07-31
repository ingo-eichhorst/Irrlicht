#!/usr/bin/env bash
# spawn-record-daemon_test.sh — unit tests for spawn-record-daemon.sh. Plain
# bash, no framework. Run directly or via scripts/smoke-test.sh.
#
# The lib is sourced (not run as a subprocess) so the tests drive its functions
# directly. The shutdown ladder is exercised against a fake `kill` that shadows
# the builtin and models a daemon which ignores a given set of signals: what the
# ladder owns is the ESCALATION DECISION — which signal it sends next, how long
# it waits, and which reason it records — not the kernel's signal delivery. A
# shadowed kill makes that deterministic and instant; driving it with real
# background processes instead made the outcome depend on bash's
# ignore-SIGINT-in-async-jobs rule, which is not what is under test.
#
# The last case is the one that matters most: it asserts neither run-cell.sh nor
# run-cell-multi.sh has grown its own copy of the lifecycle back. That
# duplication is the actual defect of #1214 — #1178's hook-config snapshot
# landed in one script only, and the other would still have exited leaving the
# user's agent config repointed at a dead port.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPTS_DIR="$(cd "$DIR/.." && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=spawn-record-daemon.sh
source "$DIR/spawn-record-daemon.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Floor under every case: $HOME feeds record_daemon_sock's production branch and
# hook-config-snapshot's file list, so point it somewhere harmless before the
# first case runs — a test must never reach the developer's real
# ~/.claude/settings.json (#1212).
HOME="$TMP/nohome"

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() {
  local label="$1" expected="$2" got="$3"
  echo "  FAIL: $label — expected [$expected] got [$got]"
  fails=$((fails + 1))
  return 0
}
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"
  return 0
}

# Spend no real time in the grace periods; the tick COUNTS stay >1 so the
# escalation still has to poll its way through each rung.
RECORD_DAEMON_POLL_TICK_S=0
RECORD_DAEMON_INT_TICKS=3
RECORD_DAEMON_TERM_TICKS=3
RECORD_DAEMON_SOCK_TICKS=3

# fresh_staging gives a case its own staging dir and clears the lib's state.
fresh_staging() {
  local name="$1"
  STAGING="$TMP/$name"
  mkdir -p "$STAGING/recordings"
  RECORD_DAEMON_PID=""
  RECORD_DAEMON_SOCK=""
  RECORD_DAEMON_STAGING="$STAGING"
  return 0
}

# fake_daemon <ignored-signals...> models a running daemon that survives the
# named signals. It shadows the `kill` builtin for the ladder cases: -0 answers
# the liveness probe, and a delivered signal kills the fake unless it is in the
# ignore list (SIGKILL can never be ignored, as in the kernel). SIGNALS_SENT
# records the escalation order.
fake_daemon() {
  FAKE_IGNORES=" $* "
  FAKE_ALIVE=1
  SIGNALS_SENT=""
  RECORD_DAEMON_PID=4242
  kill() {
    local arg="$1" sig
    case "$arg" in
      -0)  [[ "$FAKE_ALIVE" == "1" ]] && return 0 || return 1 ;;
      -*)  sig="${arg#-}"
           SIGNALS_SENT+="${SIGNALS_SENT:+,}$sig"
           if [[ "$sig" == "KILL" || "$FAKE_IGNORES" != *" $sig "* ]]; then FAKE_ALIVE=0; fi
           return 0 ;;
      # Not a flag, so not a liveness probe or a signal — nothing the ladder is
      # asserted on. Accept it the way the real builtin would.
      *) ;;
    esac
    return 0
  }
}

echo "== the socket path follows coexist isolation =="
assert_eq "coexist home owns the socket" "/tmp/onboard/irrlichd.sock" "$(record_daemon_sock /tmp/onboard)"
assert_eq "no home means the production layout" "$HOME/.local/share/irrlicht/irrlichd.sock" "$(record_daemon_sock "")"

echo "== the daemon env always carries the three recording knobs =="
unset IRRLICHT_READY_SESSION_TTL
assert_eq "production env" \
  "IRRLICHT_RECORDINGS_DIR=/s/recordings
IRRLICHT_BIND_ADDR=127.0.0.1:7837
IRRLICHT_PERMISSION_MODE=grant-all" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "")"

echo "== coexist adds IRRLICHT_HOME, and only then =="
assert_eq "coexist env" \
  "IRRLICHT_RECORDINGS_DIR=/s/recordings
IRRLICHT_BIND_ADDR=127.0.0.1:7838
IRRLICHT_PERMISSION_MODE=grant-all
IRRLICHT_HOME=/tmp/onboard" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7838 /tmp/onboard)"

echo "== a home containing spaces stays a single env entry =="
entries=0
while IFS= read -r _; do entries=$((entries + 1)); done \
  < <(record_daemon_env "/s/recordings" "127.0.0.1:7838" "/tmp/my onboard home")
assert_eq "four entries, not five" "4" "$entries"

echo "== a caller-set ready-session TTL is forwarded, absent otherwise =="
IRRLICHT_READY_SESSION_TTL=45s
assert_eq "TTL forwarded" "IRRLICHT_READY_SESSION_TTL=45s" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" | grep READY_SESSION_TTL)"
unset IRRLICHT_READY_SESSION_TTL
assert_eq "TTL absent" "" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" | grep READY_SESSION_TTL)"

echo "== a daemon that honors SIGINT ends at sigint =="
fresh_staging sigint
fake_daemon
kill_record_daemon
assert_eq "shutdown reason" "sigint" "$(cat "$STAGING/daemon.shutdown")"
assert_eq "no escalation past INT" "INT" "$SIGNALS_SENT"

echo "== a daemon deaf to SIGINT escalates to SIGTERM =="
fresh_staging sigterm
fake_daemon INT
kill_record_daemon
assert_eq "shutdown reason" "sigterm" "$(cat "$STAGING/daemon.shutdown")"
assert_eq "escalation order" "INT,TERM" "$SIGNALS_SENT"

echo "== a daemon deaf to both escalates to SIGKILL =="
fresh_staging sigkill
fake_daemon INT TERM
kill_record_daemon
assert_eq "shutdown reason" "sigkill" "$(cat "$STAGING/daemon.shutdown")"
assert_eq "escalation order" "INT,TERM,KILL" "$SIGNALS_SENT"

echo "== a daemon that never started records 'unknown' and still returns 0 =="
fresh_staging nodaemon
unset -f kill    # back to the real builtin: there is no fake process to probe
kill_record_daemon
rc=$?
assert_eq "returns 0" "0" "$rc"
assert_eq "shutdown reason" "unknown" "$(cat "$STAGING/daemon.shutdown")"

echo "== the teardown hands the shared agent config back =="
fresh_staging restore
mkdir -p "$TMP/restore-home/.claude"
HOME="$TMP/restore-home"
CODEX_HOME="$TMP/restore-codex"
mkdir -p "$CODEX_HOME"
printf '{"hooks":{"Stop":"localhost:7837"}}\n' > "$HOME/.claude/settings.json"
snapshot_hook_configs "$STAGING/hook-config-backup"
printf '{"hooks":{"Stop":"localhost:7838"}}\n' > "$HOME/.claude/settings.json"   # daemon repoints it
stop_record_daemon
assert_eq "settings.json restored" '{"hooks":{"Stop":"localhost:7837"}}' "$(cat "$HOME/.claude/settings.json")"
# The teardown owns BOTH halves of the trap spawn_record_daemon arms, so callers
# never write `trap` themselves. That also clears this file's own EXIT trap —
# re-arm it so $TMP is still cleaned up.
assert_eq "EXIT trap disarmed" "" "$(trap -p EXIT)"
trap 'rm -rf "$TMP"' EXIT
HOME="$TMP/nohome"

echo "== an unwritable backup dir refuses to start the daemon =="
# restore_hook_configs reads "no backup for this file" as "the daemon created
# it, remove it" — so a snapshot that silently did nothing would end the run by
# deleting the user's real config. Refuse before anything is spawned.
fresh_staging unwritable
: > "$TMP/unwritable/hook-config-backup"   # a FILE where the dir must go
err="$(spawn_record_daemon /nonexistent/irrlichd "$TMP/unwritable" 127.0.0.1:7838 "" 2>&1)"
rc=$?
assert_eq "returns non-zero" "1" "$rc"
[[ "$err" == *"cannot create the hook-config backup dir"* ]] && got=yes || got=no
assert_eq "says why" "yes" "$got"
[[ -f "$TMP/unwritable/daemon.log" ]] && got=spawned || got=none
assert_eq "nothing was spawned" "none" "$got"

echo "== waiting on a socket that never appears fails loudly =="
fresh_staging nosock
RECORD_DAEMON_SOCK="$STAGING/irrlichd.sock"
err="$(wait_for_record_daemon 2>&1)"
rc=$?
assert_eq "returns non-zero" "1" "$rc"
assert_eq "names the socket" "daemon socket never appeared: $STAGING/irrlichd.sock" "$err"

echo "== waiting succeeds once the socket is bound =="
fresh_staging withsock
RECORD_DAEMON_SOCK="$STAGING/irrlichd.sock"
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$RECORD_DAEMON_SOCK"
  wait_for_record_daemon
  assert_eq "returns 0" "0" "$?"
else
  echo "  SKIP: python3 not available to bind a unix socket"
fi

echo "== the lifecycle has exactly one owner (#1214) =="
# The defect this lib exists to prevent is a SECOND copy of the lifecycle
# growing back in a caller. Each rung of the lifecycle has a fingerprint: a
# signal sent to a process (the shutdown ladder), `snapshot_hook_configs` (the
# config snapshot), and an `IRRLICHT_RECORDINGS_DIR=` assignment (the env
# assembly). All three belong to the lib alone; a caller may only ask for the
# lifecycle by name. The signal pattern covers the spellings a re-implementation
# would plausibly use — `kill -INT`, `kill -s INT`, `kill -2` — rather than only
# the literal text that leaked last time.
SIGNAL_RE='kill +-(s +)?(INT|TERM|KILL|2|15|9)'
for script in run-cell.sh run-cell-multi.sh; do
  path="$SCRIPTS_DIR/$script"
  assert_eq "$script delegates to the lib" "yes" \
    "$(grep -q 'spawn_record_daemon' "$path" && echo yes || echo no)"
  assert_eq "$script signals no process of its own" "0" \
    "$(grep -cE "$SIGNAL_RE" "$path" | tr -d ' ')"
  assert_eq "$script has no hook-config snapshot of its own" "0" \
    "$(grep -c 'snapshot_hook_configs' "$path" | tr -d ' ')"
  assert_eq "$script assembles no daemon env of its own" "0" \
    "$(grep -c 'IRRLICHT_RECORDINGS_DIR=' "$path" | tr -d ' ')"
done

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "spawn-record-daemon_test: ALL PASS"
  exit 0
fi
echo "spawn-record-daemon_test: $fails FAILURE(S)" >&2
exit 1
