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
# duplication is the actual defect of #1214 — #1178's config snapshot landed
# in one script only, and the other would still have exited leaving the
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
# managed-file-snapshot's file list, so point it somewhere harmless before the
# first case runs — a test must never reach the developer's real
# ~/.claude/settings.json (#1212).
HOME="$TMP/nohome"

# Assertion label reused by every shutdown-ladder case below.
SHUTDOWN_REASON="shutdown reason"

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
#
# All four are read by the SOURCED library, not by this file:
# spawn-record-daemon.sh sleeps on POLL_TICK_S (139, 152) and bounds each rung
# with SOCK_TICKS (137), INT_TICKS (165) and TERM_TICKS (167). The linter does
# not follow a source through a variable path, so it cannot see the consumer —
# hence one scoped disable per knob (a directive covers only the next command).
# shellcheck disable=SC2034
RECORD_DAEMON_POLL_TICK_S=0
# shellcheck disable=SC2034
RECORD_DAEMON_INT_TICKS=3
# shellcheck disable=SC2034
RECORD_DAEMON_TERM_TICKS=3
# shellcheck disable=SC2034
RECORD_DAEMON_SOCK_TICKS=3

# fresh_staging gives a case its own staging dir and clears the lib's state.
#
# shellcheck disable=SC2034  # RECORD_DAEMON_PID / _STAGING are the LIBRARY's
# state (declared at spawn-record-daemon.sh 36/38, read at 149/162/174) —
# seeding them is how a case puts the lib into the state it wants to grade.
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
# shellcheck disable=SC2034  # RECORD_DAEMON_PID is the library's state, read
# by its `kill -"$sig" "$RECORD_DAEMON_PID"` at spawn-record-daemon.sh:149 —
# which is exactly the call the shadowed `kill` below intercepts.
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

echo "== the daemon env always carries the four recording knobs =="
# IRRLICHT_ALLOW_SHARED_CONFIG_WRITES is asserted here, not merely allowed:
# without it a grant-all daemon refuses every hook install (#1449) and the rig
# records fixtures with no hook-delivered observations at all — which looks like
# an adapter that cannot report state rather than a rig that was refused.
unset IRRLICHT_READY_SESSION_TTL
assert_eq "production env" \
  "IRRLICHT_RECORDINGS_DIR=/s/recordings
IRRLICHT_BIND_ADDR=127.0.0.1:7837
IRRLICHT_PERMISSION_MODE=grant-all
IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "")"

echo "== coexist adds IRRLICHT_HOME, and only then =="
assert_eq "coexist env" \
  "IRRLICHT_RECORDINGS_DIR=/s/recordings
IRRLICHT_BIND_ADDR=127.0.0.1:7838
IRRLICHT_PERMISSION_MODE=grant-all
IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1
IRRLICHT_HOME=/tmp/onboard" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7838 /tmp/onboard)"

echo "== a home containing spaces stays a single env entry =="
entries=0
while IFS= read -r _; do entries=$((entries + 1)); done \
  < <(record_daemon_env "/s/recordings" "127.0.0.1:7838" "/tmp/my onboard home")
assert_eq "five entries, not six" "5" "$entries"

echo "== naming adapters forwards IRRLICHT_RECORD_ADAPTERS, absent otherwise =="
# #1769: naming the adapter(s) this call is recording narrows PermissionService's
# grant-all auto-grant to them, so a cell recording e.g. mistral-vibe no longer
# auto-grants (and Applies) every OTHER adapter's hook installer too.
assert_eq "adapters forwarded" "IRRLICHT_RECORD_ADAPTERS=mistral-vibe" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" mistral-vibe | grep RECORD_ADAPTERS)"
assert_eq "comma-joined pair forwarded verbatim" "IRRLICHT_RECORD_ADAPTERS=hermes,kiro-cli" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" hermes,kiro-cli | grep RECORD_ADAPTERS)"
assert_eq "absent when no adapters given" "" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" | grep RECORD_ADAPTERS)"

echo "== the claudecode SLUG is translated to the daemon's hyphenated identity =="
# claudecode is the one adapter whose replaydata/rig-CLI slug ("claudecode")
# differs from its daemon agent.Identity.Name ("claude-code") — see
# shard.go's SlugForAdapter for the same divergence in the opposite direction.
# Forwarding the slug unmodified would feed PermissionService.Start's
# exact-string recordAdapters lookup a name that never matches claudecode's
# OWN identity, silently scoping claudecode itself OUT of its own recording
# (caught in review, not by an earlier version of this test).
assert_eq "bare claudecode slug -> claude-code" "IRRLICHT_RECORD_ADAPTERS=claude-code" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" claudecode | grep RECORD_ADAPTERS)"
assert_eq "claudecode translated inside a comma-joined pair" "IRRLICHT_RECORD_ADAPTERS=claude-code,kiro-cli" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" claudecode,kiro-cli | grep RECORD_ADAPTERS)"
assert_eq "claudecode translated as the SECOND half of a pair too" "IRRLICHT_RECORD_ADAPTERS=kiro-cli,claude-code" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" kiro-cli,claudecode | grep RECORD_ADAPTERS)"
assert_eq "an adapter whose slug already matches its identity passes through" "IRRLICHT_RECORD_ADAPTERS=codex" \
  "$(record_daemon_env /s/recordings 127.0.0.1:7837 "" codex | grep RECORD_ADAPTERS)"

echo "== a caller-set ready-session TTL is forwarded, absent otherwise =="
# shellcheck disable=SC2034  # not read by this file: it is set so
# record_daemon_env picks it out of the ENVIRONMENT and forwards it, which is
# the assertion two lines down. The daemon is the eventual consumer
# (core/domain/config/config.go:168).
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
assert_eq "$SHUTDOWN_REASON" "sigint" "$(cat "$STAGING/daemon.shutdown")"
assert_eq "no escalation past INT" "INT" "$SIGNALS_SENT"

echo "== a daemon deaf to SIGINT escalates to SIGTERM =="
fresh_staging sigterm
fake_daemon INT
kill_record_daemon
assert_eq "$SHUTDOWN_REASON" "sigterm" "$(cat "$STAGING/daemon.shutdown")"
assert_eq "escalation order" "INT,TERM" "$SIGNALS_SENT"

echo "== a daemon deaf to both escalates to SIGKILL =="
fresh_staging sigkill
fake_daemon INT TERM
kill_record_daemon
assert_eq "$SHUTDOWN_REASON" "sigkill" "$(cat "$STAGING/daemon.shutdown")"
assert_eq "escalation order" "INT,TERM,KILL" "$SIGNALS_SENT"

echo "== a daemon that never started records 'unknown' and still returns 0 =="
fresh_staging nodaemon
unset -f kill    # back to the real builtin: there is no fake process to probe
kill_record_daemon
rc=$?
assert_eq "returns 0" "0" "$rc"
assert_eq "$SHUTDOWN_REASON" "unknown" "$(cat "$STAGING/daemon.shutdown")"

echo "== the teardown hands the shared agent config back =="
fresh_staging restore
mkdir -p "$TMP/restore-home/.claude"
HOME="$TMP/restore-home"
CODEX_HOME="$TMP/restore-codex"
mkdir -p "$CODEX_HOME"
printf '{"hooks":{"Stop":"localhost:7837"}}\n' > "$HOME/.claude/settings.json"
# The snapshot asks the daemon binary which files to protect (#1357), so stand
# one in that declares the shipped pair.
FAKE_DAEMON="$STAGING/irrlichd"
printf '%s\n%s\n' "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json" > "$STAGING/managed-files"
printf '#!/usr/bin/env bash\ncat %q\n' "$STAGING/managed-files" > "$FAKE_DAEMON"
chmod +x "$FAKE_DAEMON"
snapshot_managed_files "$STAGING/managed-file-backup" "$FAKE_DAEMON"
printf '{"hooks":{"Stop":"localhost:7838"}}\n' > "$HOME/.claude/settings.json"   # daemon repoints it
stop_record_daemon
assert_eq "settings.json restored" '{"hooks":{"Stop":"localhost:7837"}}' "$(cat "$HOME/.claude/settings.json")"
# The teardown owns BOTH halves of the trap spawn_record_daemon arms, so callers
# never write `trap` themselves. That also clears this file's own EXIT trap —
# re-arm it so $TMP is still cleaned up.
assert_eq "EXIT trap disarmed" "" "$(trap -p EXIT)"
trap 'rm -rf "$TMP"' EXIT
HOME="$TMP/nohome"

echo "== teardown refuses to overwrite a post-install concurrent change =="
fresh_staging concurrent_restore
mkdir -p "$TMP/concurrent-home/.claude"
HOME="$TMP/concurrent-home"
CODEX_HOME="$TMP/concurrent-codex"
mkdir -p "$CODEX_HOME"
printf '{"hooks":{"Stop":"before"}}\n' > "$HOME/.claude/settings.json"
FAKE_DAEMON="$STAGING/irrlichd"
printf '%s\n%s\n' "$HOME/.claude/settings.json" "$CODEX_HOME/hooks.json" > "$STAGING/managed-files"
printf '#!/usr/bin/env bash\ncat %q\n' "$STAGING/managed-files" > "$FAKE_DAEMON"
chmod +x "$FAKE_DAEMON"
snapshot_managed_files "$STAGING/managed-file-backup" "$FAKE_DAEMON"
printf '{"hooks":{"Stop":"expected-daemon"}}\n' > "$HOME/.claude/settings.json"
seal_managed_files
printf '{"hooks":{"Stop":"external-edit"}}\n' > "$HOME/.claude/settings.json"
restore_err="$(stop_record_daemon 2>&1)"
restore_rc=$?
assert_eq "stop returns the restore refusal" "1" "$restore_rc"
assert_eq "external bytes stay in place" '{"hooks":{"Stop":"external-edit"}}' "$(cat "$HOME/.claude/settings.json")"
case "$restore_err" in
  *"refusing to overwrite concurrent change"*) got=yes ;;
  *) got=no ;;
esac
assert_eq "the refusal names the concurrent change" "yes" "$got"
trap 'rm -rf "$TMP"' EXIT
HOME="$TMP/nohome"

echo "== an unwritable backup dir refuses to start the daemon =="
# A snapshot that cannot save the user's config must stop the run before the
# grant-all daemon has rewritten anything — there would be nothing to hand back.
fresh_staging unwritable
# shellcheck disable=SC2034  # managed-file-snapshot.sh's own state (declared
# at its line 44); clearing it here is what makes this case a FRESH snapshot
# rather than a second one over a spent handle.
MANAGED_FILE_BACKUP_DIR=""   # the case above left a spent snapshot behind
: > "$TMP/unwritable/managed-file-backup"   # a FILE where the dir must go
err="$(spawn_record_daemon /nonexistent/irrlichd "$TMP/unwritable" 127.0.0.1:7838 "" 2>&1)"
rc=$?
assert_eq "returns non-zero" "1" "$rc"
[[ "$err" == *"cannot create the backup dir"* ]] && got=yes || got=no
assert_eq "says why" "yes" "$got"
[[ -f "$TMP/unwritable/daemon.log" ]] && got=spawned || got=none
assert_eq "nothing was spawned" "none" "$got"

echo "== a snapshot that cannot complete refuses to start the daemon =="
# The rung ABOVE this one (an uncreatable backup dir) exits at the mkdir gate
# and never reaches the snapshot, so without this case nothing distinguishes the
# two: deleting the snapshot guard entirely left the whole suite green. Give it
# a perfectly writable staging dir and a daemon that cannot answer
# --print-managed-files, and nothing may be spawned.
fresh_staging nosnapshot
BAD_DAEMON="$STAGING/irrlichd"
printf '#!/usr/bin/env bash\nexit 1\n' > "$BAD_DAEMON"
chmod +x "$BAD_DAEMON"
err="$(spawn_record_daemon "$BAD_DAEMON" "$STAGING" 127.0.0.1:7838 "" 2>&1)"
rc=$?
assert_eq "returns non-zero" "1" "$rc"
[[ "$err" == *"refusing to spawn the recording daemon"* ]] && got=yes || got=no
assert_eq "says why" "yes" "$got"
[[ -f "$STAGING/daemon.log" ]] && got=spawned || got=none
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
# signal sent to a process (the shutdown ladder), `snapshot_managed_files` (the
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
  assert_eq "$script has no managed-file snapshot of its own" "0" \
    "$(grep -c 'snapshot_managed_files' "$path" | tr -d ' ')"
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
