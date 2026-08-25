#!/usr/bin/env bash
# tmux-teardown-check_test.sh — unit tests for tmux-teardown-check.sh. Plain
# bash, no framework. Run directly or via scripts/smoke-test.sh.
#
# `tmux` is shadowed by a function, the same "shadow the external command" idiom
# unapplied-grants-check_test.sh uses for curl and spawn-record-daemon_test.sh
# uses for the kill builtin. What is under test is the DECISION — does a session
# carrying this run's driver pid fail the cell, and does a lookup that could not
# be made fail it just as loudly — never tmux's own client/server protocol. A
# shadowed tmux makes every arm reachable with no server running anywhere, which
# also means these tests are the only place the "no tmux binary" and "tmux
# answered but not with a session list" arms are ever exercised: a real recording
# host always has tmux and always has a server up by the time the gate runs.
#
# THE MUTATION FIXTURES (AGENTS.md: a check a change ADDS has no "before the fix"
# to run red, so mutate what it protects and commit the mutation). This check
# protects two different things, so there are two families:
#
#   the RUNTIME check — the cases below marked "MUTATION". Each one hands the
#   library a tmux that behaves like the defect #1825 describes (a session that
#   outlived its driver) or like a lookup that silently fails (no binary, an
#   error that is not "no server", a success that lists nothing), and asserts a
#   non-zero verdict. Flip any of them to the healthy fixture and the assertion
#   goes red, which is what makes the passing green mean something.
#
#   the WIRING — the last section. A perfectly unit-tested library wired to
#   nothing is exactly the failure mode #1825 is about, so run-cell.sh's source
#   text is asserted to actually call it. That tripwire is itself run against a
#   MUTATED COPY of run-cell.sh with the call deleted, so it cannot pass
#   vacuously the day someone removes the call.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPTS_DIR="$(cd "$DIR/.." && pwd)"
# shellcheck source=tmux-teardown-check.sh
source "$DIR/tmux-teardown-check.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

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
assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) pass "$label" ;;
    *) fail "$label" "*$needle*" "$haystack" ;;
  esac
  return 0
}

# Spend no real time between looks. Read by the SOURCED await-gone.sh (its
# `sleep "$AWAIT_GONE_POLL_SECONDS"`), not by this file — the linter does not
# follow a source, so it cannot see the consumer.
# shellcheck disable=SC2034
AWAIT_GONE_POLL_SECONDS=0.02

# The deadline/lifetime pair every case uses. 1s is await_gone_bound's floor and
# 10s is the smallest lifetime that clears its 10:1 wall, so the survivor case
# spends one second and no more.
DEADLINE=1
LIFETIME=10

# The pid whose sessions belong to "this run". Not a real process: the check
# matches session NAMES, and asserting on a live pid would make the suite depend
# on process scheduling it does not test.
PID=95883

# fake_tmux <mode> [args…] — shadow tmux with one that models a given server
# state. Every mode ignores its arguments the way `tmux list-sessions -F …`
# would be called, because what is graded is how the library reads the RESULT.
fake_tmux() {
  local mode="$1"; shift
  FAKE_TMUX_NAMES=("$@")
  case "$mode" in
    sessions)   # a live server naming the sessions it owns
      tmux() { printf '%s\n' "${FAKE_TMUX_NAMES[@]}"; return 0; } ;;
    noserver)   # tmux's own spelling of "there are zero sessions"
      tmux() { echo "no server running on /private/tmp/tmux-501/default" >&2; return 1; } ;;
    broken)     # any other failure: a lookup that did NOT happen
      tmux() { echo "lost server" >&2; return 1; } ;;
    empty0)     # answered 0 but listed nothing — impossible for a live server
      tmux() { return 0; } ;;
    *) echo "fake_tmux: unknown mode $mode" >&2; return 1 ;;
  esac
  return 0
}

# run_check — call the library and report its verdict as one word, so a case
# reads as the answer rather than as a return code.
run_check() {
  local rc=0
  check_tmux_teardown "$@" >/dev/null 2>&1 || rc=$?
  case "$rc" in
    0) echo "gone" ;;
    1) echo "survived" ;;
    2) echo "cannot-look" ;;
    *) echo "unexpected-$rc" ;;
  esac
  return 0
}

echo "== the driver's sessions are gone: the cell passes =="
# Other runs' and the operator's own sessions are present throughout, so a pass
# here also proves the check is scoped to THIS run rather than to "any tmux".
fake_tmux sessions "claudecode-onboard-1787641617-11111" "irc" "work"
assert_eq "a server with only other people's sessions" "gone" "$(run_check "$PID" "$DEADLINE" "$LIFETIME")"

echo "== MUTATION — a session outlives the driver (#1825's defect): the cell FAILS =="
fake_tmux sessions "work" "claudecode-onboard-1787641617-$PID" "irc"
assert_eq "verdict" "survived" "$(run_check "$PID" "$DEADLINE" "$LIFETIME")"
check_tmux_teardown "$PID" "$DEADLINE" "$LIFETIME" >/dev/null 2>&1
assert_eq "names the survivor so it can be killed" \
  "claudecode-onboard-1787641617-$PID" "$TMUX_TEARDOWN_SURVIVORS"

echo "== MUTATION — every driver's naming scheme is matched, not just claudecode's =="
# One session per naming scheme actually in the tree, each carrying $PID as a
# '-'-delimited field. A scheme that stopped matching would leak silently, which
# is the failure this whole gate exists to remove.
for name in \
  "claudecode-onboard-1787641617-$PID" \
  "claudecode-onboard-1787641617-$PID-2" \
  "codex-onboard-1787641617-$PID-r1" \
  "pi-onboard-1787641617-$PID-r1" \
  "kiro-clidrv-$PID-1787641617-r1" \
  "geminidrv-$PID-1787641617-r1" \
  "antigravitydrv-$PID-1787641617-r1" \
  "mistral-vibedrv-$PID-1787641617-resume1" \
  "copilotdrv-$PID-1787641617-1" \
  "hermesdrv-$PID-1787641617-1" \
  "ocdrv-$PID-1787641617" \
  "aider-onboard-deadbeef-$PID"
do
  fake_tmux sessions "$name" "some-unrelated-session"
  assert_eq "$name" "survived" "$(run_check "$PID" "$DEADLINE" "$LIFETIME")"
done

echo "== the pid matches a whole field only, never a substring =="
# 958831 and 9588 both CONTAIN 95883. Matching on substrings would fail cells
# because of a neighbouring run, and a gate that cries wolf gets switched off.
fake_tmux sessions "codex-onboard-1787641617-958831-r1" "geminidrv-9588-1787641617-r1" "x-95883x-y"
assert_eq "neighbouring pids do not count as this run" "gone" "$(run_check "$PID" "$DEADLINE" "$LIFETIME")"

echo "== no server running: legitimately ZERO sessions, so the cell passes =="
fake_tmux noserver
assert_eq "tmux's exit-1 'no server running' is an empty list, not a failure" \
  "gone" "$(run_check "$PID" "$DEADLINE" "$LIFETIME")"

echo "== MUTATION — tmux fails for any OTHER reason: cannot look, so the cell FAILS =="
fake_tmux broken
assert_eq "verdict" "cannot-look" "$(run_check "$PID" "$DEADLINE" "$LIFETIME")"
check_tmux_teardown "$PID" "$DEADLINE" "$LIFETIME" >/dev/null 2>&1
assert_contains "the reason quotes what tmux actually said" "lost server" "$TMUX_TEARDOWN_REASON"

echo "== MUTATION — tmux exits 0 but names nothing: unparseable, so the cell FAILS =="
# A live server always owns at least one session (it exits with the last), so an
# empty success is something answering for tmux without listing. Reading it as
# "zero sessions" is the exact conflation #1825 is about.
fake_tmux empty0
assert_eq "verdict" "cannot-look" "$(run_check "$PID" "$DEADLINE" "$LIFETIME")"

echo "== MUTATION — no tmux binary at all: cannot look, so the cell FAILS =="
# The one arm that needs PATH rather than a shadow: `command -v` finds a shell
# function whatever PATH says, so the function has to go away for the library to
# see what a host without tmux sees.
unset -f tmux
mkdir -p "$TMP/emptybin"
SAVED_PATH="$PATH"
# shellcheck disable=SC2123  # PATH is exactly what this case is manipulating:
# emptying the search path is the only way to show the library what a host with
# no tmux sees. Restored four lines down, before anything external runs again.
PATH="$TMP/emptybin"
NO_TMUX_VERDICT="$(run_check "$PID" "$DEADLINE" "$LIFETIME")"
check_tmux_teardown "$PID" "$DEADLINE" "$LIFETIME" >/dev/null 2>&1
NO_TMUX_REASON="$TMUX_TEARDOWN_REASON"
PATH="$SAVED_PATH"
assert_eq "a missing binary is a broken lookup, never an empty session list" \
  "cannot-look" "$NO_TMUX_VERDICT"
assert_contains "the reason names the missing binary" "no tmux binary on PATH" "$NO_TMUX_REASON"

echo "== MUTATION — no run identity: cannot look, so the cell FAILS =="
# run-cell.sh reads the pid out of a file the driver's own process wrote. If that
# file never appeared, every surviving session would read as somebody else's and
# the gate would pass vacuously — so an unusable pid is a refusal, not a pass.
fake_tmux sessions "claudecode-onboard-1787641617-$PID"
assert_eq "empty pid" "cannot-look" "$(run_check "" "$DEADLINE" "$LIFETIME")"
assert_eq "non-numeric pid" "cannot-look" "$(run_check "not-a-pid" "$DEADLINE" "$LIFETIME")"
check_tmux_teardown "" "$DEADLINE" "$LIFETIME" >/dev/null 2>&1
assert_contains "the reason says why identity matters" "run's identity" "$TMUX_TEARDOWN_REASON"

echo "== MUTATION — a grace as long as the run itself is refused =="
# await_gone_bound's wall from above. At that ratio a session whose agent died
# BY ITSELF reads exactly like one the driver tore down, so the poll would stop
# asserting anything — and a refusal is the honest answer, not a pass.
fake_tmux sessions "claudecode-onboard-1787641617-$PID"
assert_eq "deadline not 10x under the driver timeout" "cannot-look" "$(run_check "$PID" 60 120)"
assert_eq "a zero deadline is not 'look exactly once'" "cannot-look" "$(run_check "$PID" 0 120)"

echo "== a survivor is polled for, not slept on =="
# The elapsed time is reported so a failure line can say how long it waited, and
# the poll is bounded by the deadline it was given rather than running on.
fake_tmux sessions "claudecode-onboard-1787641617-$PID"
check_tmux_teardown "$PID" "$DEADLINE" "$LIFETIME" >/dev/null 2>&1
assert_eq "waited the deadline and no longer" "yes" \
  "$([[ "$TMUX_TEARDOWN_ELAPSED" -ge "$DEADLINE" && "$TMUX_TEARDOWN_ELAPSED" -le $((DEADLINE + 2)) ]] && echo yes || echo no)"
echo "== a clean run reports at once =="
fake_tmux sessions "somebody-elses-session"
check_tmux_teardown "$PID" "$DEADLINE" "$LIFETIME" >/dev/null 2>&1
assert_eq "no wait at all when the sessions are already gone" "0" "$TMUX_TEARDOWN_ELAPSED"

unset -f tmux

echo "== the gate is WIRED: run-cell.sh actually calls it (#1825) =="
# A perfectly unit-tested library that nothing calls is the failure mode this
# issue is about, one layer out. Each assertion below is paired with the same
# assertion against a MUTATED COPY of run-cell.sh — the mutation is committed
# here rather than described in a PR body, so a tripwire that stopped being able
# to look is red instead of green.
RUN_CELL="$SCRIPTS_DIR/run-cell.sh"
assert_eq "run-cell.sh is readable at all" "yes" \
  "$([[ -r "$RUN_CELL" && -s "$RUN_CELL" ]] && echo yes || echo no)"

wired() {   # <file> <pattern> → yes/no
  grep -qE "$2" "$1" && echo yes || echo no
}

MUTANT="$TMP/run-cell-without-the-gate.sh"
# The mutation: delete every line that names the library or its entry point.
grep -v -e 'tmux-teardown-check' -e 'check_tmux_teardown' -e 'DRIVER_PID_FILE' "$RUN_CELL" > "$MUTANT"
assert_eq "the mutant really is different from the original" "yes" \
  "$(cmp -s "$RUN_CELL" "$MUTANT" && echo no || echo yes)"

assert_eq "sources the library" "yes" "$(wired "$RUN_CELL" 'lib/tmux-teardown-check\.sh')"
assert_eq "  …and the mutant does not (the tripwire can go red)" "no" \
  "$(wired "$MUTANT" 'lib/tmux-teardown-check\.sh')"

assert_eq "calls check_tmux_teardown" "yes" "$(wired "$RUN_CELL" '^[[:space:]]*check_tmux_teardown ')"
assert_eq "  …and the mutant does not" "no" "$(wired "$MUTANT" '^[[:space:]]*check_tmux_teardown ')"

assert_eq "passes it the pid it captured from the driver" "yes" \
  "$(wired "$RUN_CELL" 'check_tmux_teardown "\$DRIVER_PID"')"
assert_eq "  …and the mutant does not" "no" \
  "$(wired "$MUTANT" 'check_tmux_teardown "\$DRIVER_PID"')"

assert_eq "captures that pid from the driver's own process" "yes" \
  "$(wired "$RUN_CELL" 'DRIVER_PID_FILE')"
assert_eq "  …and the mutant does not" "no" "$(wired "$MUTANT" 'DRIVER_PID_FILE')"

# Both verdicts have to be able to FAIL the cell, and under DIFFERENT error
# codes: "it leaked" and "nothing was checked" printing the same thing is the
# bug, not the fix.
assert_eq "a survivor fails the cell under its own error code" "yes" \
  "$(wired "$RUN_CELL" 'driver_tmux_session_survived')"
assert_eq "an unreadable lookup fails it under a DIFFERENT one" "yes" \
  "$(wired "$RUN_CELL" 'driver_tmux_teardown_unreadable')"

# The pid capture only works because the driver stays in the FOREGROUND and
# `exec`s into the process whose pid was written. A `&` on the driver line would
# hand back a pid too, but silently changes the driver's stdin and its SIGINT
# disposition — see the comment above the call in run-cell.sh.
assert_eq "the driver is not backgrounded" "no" \
  "$(wired "$RUN_CELL" '^[[:space:]]*"\$DRIVER".*&[[:space:]]*$')"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "tmux-teardown-check_test: ALL PASS"
  exit 0
fi
echo "tmux-teardown-check_test: $fails FAILURE(S)" >&2
exit 1
