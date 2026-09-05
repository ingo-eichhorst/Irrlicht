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
# jq is a hard dependency of the run-cell.sh gate section at the bottom (it
# reads the ERROR manifest the gate writes). Declared, not discovered: a suite
# that skips its last section on a host without jq reports the same green as one
# that ran it.
command -v jq >/dev/null || { echo "tmux-teardown-check_test: jq is required" >&2; exit 2; }
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

# ===========================================================================
# run-cell.sh's teardown gate: what it DOES with the verdict, and WHEN
# ===========================================================================
# Everything above grades the LIBRARY. This section grades run-cell.sh's use of
# it, which is where #1825's review found the gate could still be skipped: the
# measurement printed, and the VERDICT — the ERROR manifest, the error code, the
# "kill the survivor with" line, the exit — sat ~64 lines further down, behind
# `stop_recipe_mock` and two `recipe_runtime_assert_* || exit 5` gates. A driver
# that aborts skips its teardown AND never writes its env receipt, so those two
# fire together; the cell then exited 5 with NO manifest, classify-failure.sh
# had nothing to read and graded the run `unknown`, and the record skill's
# "never retry driver_session_leaked blind" rule never fired — the retry started
# a second live agent beside the leaked one.
#
# HOW IT GETS AT IT. run-cell.sh is a top-to-bottom script: sourcing it would
# spawn a daemon and drive a real agent. So the two spans that carry the
# decisions are delimited by `# BEGIN <name>` / `# END <name>` markers,
# EXTRACTED from the script's own source text here, and run in a generated
# harness against stubs — the same extract-and-execute idiom
# run-cell-multi-teardown_test.sh uses, and it is what stops this suite from
# grading a copy that has drifted from the one that ships. The extraction
# REFUSES loudly rather than grading an empty string.
#
# THE MUTATION FIXTURE (AGENTS.md: an ordering a change ADDS has no "before the
# fix" to run red, so mutate what it protects and commit the mutation). The
# pre-fix ordering is not described here, it is RECONSTRUCTED: the verdict block
# is lifted out of the shipped text and re-appended after the two `|| exit 5`
# gates, and the same leak-plus-failing-receipt fixture is run through it. It
# exits 5 and writes no manifest, which is exactly the harm — so the shipped
# block's green is the difference between two orderings of the same lines.

RUN_CELL="$SCRIPTS_DIR/run-cell.sh"

# extract_block <file> <name> — the lines strictly between the markers.
extract_block() {
  awk -v n="$2" '
    $0 ~ ("^# BEGIN " n "([ \t]|$)") { inb = 1; next }
    inb && $0 ~ ("^# END " n "[ \t]*$") { inb = 0; next }
    inb { print }
  ' "$1"
}

# require_block <name> — extract, or stop the suite. "The marker moved" and "the
# gate is fine" must not both produce a green run.
require_block() {
  local name="$1" src
  src="$(extract_block "$RUN_CELL" "$name")"
  if [[ -z "$src" ]]; then
    echo "tmux-teardown-check_test: REFUSING — no '# BEGIN $name' … '# END $name' block in $RUN_CELL." >&2
    echo "  Every case below would have graded an empty string. Restore the markers or rename them here too." >&2
    exit 1
  fi
  printf '%s\n' "$src"
}

PID_BLOCK="$(require_block driver_pid_capture)"
GATE_BLOCK="$(require_block driver_teardown_gate)"

echo "== the suite is grading the shipped spans, not a copy =="
assert_eq "the pid-capture span really captures the pid" "yes" \
  "$(grep -q 'DRIVER_PID=' <<< "$PID_BLOCK" && echo yes || echo no)"
assert_eq "the gate span really contains the verdict" "yes" \
  "$(grep -q 'driver_tmux_session_survived' <<< "$GATE_BLOCK" && echo yes || echo no)"
assert_eq "…and both recipe-runtime gates it must precede" "2" \
  "$(grep -cE '^recipe_runtime_assert_.* \|\| exit 5$' <<< "$GATE_BLOCK")"

# mutant_block — the PRE-FIX ordering, rebuilt from the shipped lines: the
# verdict lifted out and re-appended after everything else in the span.
mutant_block() {
  awk '
    /^# BEGIN tmux_teardown_verdict$/ { inv = 1; next }
    /^# END tmux_teardown_verdict$/   { inv = 0; next }
    inv { v = v $0 "\n"; next }
    { print }
    END { printf "%s", v }
  ' <<< "$GATE_BLOCK"
}
MUTANT_BLOCK="$(mutant_block)"
assert_eq "the mutant really is a reordering, not a copy" "yes" \
  "$([[ "$MUTANT_BLOCK" != "$GATE_BLOCK" ]] && echo yes || echo no)"
assert_eq "…and it kept every line (only their order changed)" "yes" \
  "$([[ "$(sort <<< "$MUTANT_BLOCK")" == "$(grep -v -e '^# BEGIN tmux_teardown_verdict$' -e '^# END tmux_teardown_verdict$' <<< "$GATE_BLOCK" | sort)" ]] && echo yes || echo no)"

# --- The harness -----------------------------------------------------------
# Per-case knobs, reset by run_gate's caller. Deliberately globals rather than
# nine positional arguments: a case reads as the fixture it sets up.
CASE_ENV_RECEIPT_RC=0    # what recipe_runtime_assert_env_receipt returns
CASE_DRIVER_BODY=":"     # extra shell the stub driver runs before exiting
CASE_PIDFILE_PRE=""      # "dir" → pre-create $STAGING/driver.pid AS A DIRECTORY
CASE_TMUX="clean"        # clean | leak | broken

# Outputs.
GATE_RC=0
GATE_OUT=""
GATE_MANIFEST=""
GATE_DRIVER_RAN=""

# run_gate <case-name> <block-text> — generate a harness around the SHIPPED
# span, run it in its own bash so an `exit` inside it is the thing under test
# rather than the end of this suite, and report through the globals above.
run_gate() {
  local name="$1" block="$2"
  local dir="$TMP/$name" staging
  rm -rf "$dir"; mkdir -p "$dir/staging"
  staging="$dir/staging"

  # The stub driver. It always drops a marker, so "the wrapper never ran the
  # driver" is observed rather than inferred from a missing pid.
  {
    echo '#!/usr/bin/env bash'
    printf 'touch %s\n' "$dir/driver-ran"
    # The wrapper shifts the pid path off before exec'ing, so the driver's own
    # $1 is $STAGING. A case that wants to clobber the pid file gets its path
    # by name instead of guessing at a positional.
    printf 'PIDFILE=%s\n' "$staging/driver.pid"
    printf '%s\n' "$CASE_DRIVER_BODY"
    echo 'exit 0'
  } > "$dir/driver.sh"
  chmod +x "$dir/driver.sh"

  [[ "$CASE_PIDFILE_PRE" == "dir" ]] && mkdir -p "$staging/driver.pid"

  {
    echo '#!/usr/bin/env bash'
    echo 'set -euo pipefail'
    printf 'source %s\n' "$DIR/tmux-teardown-check.sh"
    echo 'AWAIT_GONE_POLL_SECONDS=0.02'
    printf 'STAGING=%s\n' "$staging"
    printf 'DRIVER=%s\n' "$dir/driver.sh"
    echo 'ADAPTER=kiro-cli'
    echo 'FOLDER=error-exit-nonzero'
    echo 'UUID=00000000-0000-0000-0000-000000000000'
    echo 'DRIVER_INPUT=hello'
    echo 'TIMEOUT_S=10'
    echo 'SCRIPT_JSON='"'"'[{"send":"hi"}]'"'"''
    echo 'CELL_JSON='"'"'{}'"'"''
    echo 'MOCK_ADDR='
    case "$CASE_TMUX" in
      leak)   echo 'tmux() { printf "%s\n" "kiro-clidrv-$(tr -d "[:space:]" < "$STAGING/driver.pid" 2>/dev/null)-1787641617-r1"; return 0; }' ;;
      broken) echo 'tmux() { echo "lost server" >&2; return 1; }' ;;
      *)      echo 'tmux() { echo "no server running on /private/tmp/tmux-501/default" >&2; return 1; }' ;;
    esac
    echo 'stop_recipe_mock() { return 0; }'
    printf 'recipe_runtime_assert_env_receipt() { echo "runtime_gap: no receipt" >&2; return %s; }\n' "$CASE_ENV_RECEIPT_RC"
    echo 'recipe_runtime_assert_mock_used() { return 0; }'
    printf '%s\n' "$PID_BLOCK"
    printf '%s\n' "$block"
  } > "$dir/harness.sh"

  GATE_RC=0
  GATE_OUT="$(bash "$dir/harness.sh" 2>&1)" || GATE_RC=$?
  GATE_MANIFEST="$(cat "$staging/run-manifest.json" 2>/dev/null || true)"
  GATE_DRIVER_RAN="$([[ -e "$dir/driver-ran" ]] && echo yes || echo no)"
  return 0
}

reset_case() {
  CASE_ENV_RECEIPT_RC=0
  CASE_DRIVER_BODY=":"
  CASE_PIDFILE_PRE=""
  CASE_TMUX="clean"
  return 0
}

manifest_field() { jq -r "$1 // empty" <<< "$GATE_MANIFEST" 2>/dev/null || true; }

# `case` inside a "$( … )" does not parse on the bash 3.2 this repo targets — the
# pattern's `)` closes the substitution. Both live in functions for that reason.
contains() { case "$1" in *"$2"*) echo yes ;; *) echo no ;; esac; }
is_whole_number() { case "$1" in '' | *[!0-9]*) echo no ;; *) echo yes ;; esac; }

echo "== the harness runs the shipped span at all =="
reset_case
run_gate healthy "$GATE_BLOCK"
assert_eq "a clean cell with a clean receipt passes the gate" "0" "$GATE_RC"
assert_eq "the driver ran" "yes" "$GATE_DRIVER_RAN"
assert_eq "no ERROR manifest is written" "" "$GATE_MANIFEST"
assert_contains "and the measurement was printed" "tmux teardown: clean" "$GATE_OUT"

echo "== MUTATION — a leak AND a failing env receipt: the LEAK is what fails the cell =="
# The exact pairing the review named: one aborting driver skips its teardown and
# never writes its receipt. Before the reorder this exited 5 with no manifest.
reset_case
CASE_TMUX="leak"
CASE_ENV_RECEIPT_RC=1
run_gate leak_and_failing_receipt "$GATE_BLOCK"
assert_eq "the cell fails under the tmux exit, not the recipe-runtime one" "1" "$GATE_RC"
assert_eq "an ERROR manifest exists at all" "yes" \
  "$([[ -n "$GATE_MANIFEST" ]] && echo yes || echo no)"
assert_eq "under the leak's own error code" "driver_tmux_session_survived" "$(manifest_field .error)"
assert_eq "so classify-failure.sh can read a detail" "leaked" "$(manifest_field .tmux_teardown)"
assert_contains "the survivor is named so it can be killed" "kiro-clidrv-" "$(manifest_field .tmux_teardown_detail)"
assert_contains "and the operator is told how" "tmux kill-session -t" "$GATE_OUT"

echo "== MUTATION — the PRE-FIX ordering, on the same fixture: exit 5, no manifest =="
# The verdict lifted out of the shipped text and re-appended after the two
# `|| exit 5` gates. Same lines, same fixture, different order — and the leak
# vanishes. This case is the committed proof that the block above is red when
# the ordering regresses, not merely green today.
reset_case
CASE_TMUX="leak"
CASE_ENV_RECEIPT_RC=1
run_gate leak_and_failing_receipt_prefix_order "$MUTANT_BLOCK"
assert_eq "the recipe-runtime gate exits first" "5" "$GATE_RC"
assert_eq "and the leak leaves NO manifest for classify-failure.sh to read" "" "$GATE_MANIFEST"
assert_eq "so nothing names the error" "" "$(manifest_field .error)"

echo "== the same shipped fixture, flipped clean: the red came from the leak =="
reset_case
CASE_TMUX="clean"
CASE_ENV_RECEIPT_RC=1
run_gate clean_and_failing_receipt "$GATE_BLOCK"
assert_eq "with no leak the recipe-runtime gate is reached and fails on its own" "5" "$GATE_RC"
assert_eq "and no tmux manifest is written" "" "$GATE_MANIFEST"

echo "== nothing that can exit sits between the measurement and the verdict =="
# The static form of the rule the case above proves dynamically: a gate inserted
# into that span would swallow the leak again, and a reviewer should be told by a
# red suite rather than by a leaked agent. Comment lines are stripped first —
# the span is thick with prose about `|| exit 5`.
span_exits() {   # <file> → the executable `exit` lines between the two
  awk '
    /^  echo "tmux teardown: / { ins = 1; next }
    /^# BEGIN tmux_teardown_verdict$/ { ins = 0 }
    ins && $0 !~ /^[[:space:]]*#/ && $0 ~ /(^|[^[:alnum:]_])exit([^[:alnum:]_]|$)/ { print }
  ' "$1"
}
assert_eq "the span is empty of exits in the shipped script" "" "$(span_exits "$RUN_CELL")"

SPAN_MUTANT="$TMP/run-cell-with-a-gate-in-the-span.sh"
awk '
  { print }
  /^  echo "tmux teardown: / { print "some_new_gate \"$STAGING\" || exit 7" }
' "$RUN_CELL" > "$SPAN_MUTANT"
assert_eq "the mutant really is different" "yes" \
  "$(cmp -s "$RUN_CELL" "$SPAN_MUTANT" && echo no || echo yes)"
assert_contains "…and the tripwire SEES it (so its green is not vacuous)" \
  "exit 7" "$(span_exits "$SPAN_MUTANT")"

# ---------------------------------------------------------------------------
# driver.pid: a write failure is a driver.pid problem, not a tmux problem
# ---------------------------------------------------------------------------
# `bash -c 'printf "%s\n" "$$" > "$1" || exit 1; shift; exec "$@"'` exits 1
# WITHOUT running the driver when it cannot write its pid file. DRIVER_PID is
# then empty and the cell used to fail as driver_tmux_teardown_unreadable
# saying "the driver pid must be a whole number, got ''" — under a heading with
# the word tmux in it, so the operator goes and looks at tmux. The three ways
# the pid can be unusable are three different first moves, so they get three
# different sentences — and (#1828) the "never written" one now gets its OWN
# error code, driver_pid_unrecorded, rather than sharing
# driver_tmux_teardown_unreadable with the other two: it is the one case where
# the driver almost certainly never ran at all, so tmux was never the right
# place to look. Each fixture drives the SHIPPED wrapper.

echo "== MUTATION — the pid wrapper cannot write driver.pid: the driver never runs =="
# The injected failure: driver.pid is a DIRECTORY, so the wrapper's redirect
# fails exactly as it does on an unwritable $STAGING. Staging itself stays
# writable, so the manifest can still be written and asserted on.
reset_case
CASE_PIDFILE_PRE="dir"
CASE_TMUX="leak"
run_gate pidfile_unwritable "$GATE_BLOCK"
assert_eq "the driver never ran" "no" "$GATE_DRIVER_RAN"
assert_eq "the cell fails" "1" "$GATE_RC"
assert_eq "under its OWN code (#1828), not the tmux-unreadable one" "driver_pid_unrecorded" \
  "$(manifest_field .error)"
PIDFILE_DETAIL="$(manifest_field .tmux_teardown_detail)"
assert_contains "the detail names driver.pid" "driver.pid was never written" "$PIDFILE_DETAIL"
assert_contains "…and says it is not a tmux problem" "NOT a tmux one" "$PIDFILE_DETAIL"
assert_eq "…and does NOT hand the operator the old tmux-flavoured sentence" "no" \
  "$(contains "$PIDFILE_DETAIL" "must be a whole number")"
assert_eq "…and the ERROR line does not frame it as a 'driver tmux teardown' problem" "no" \
  "$(contains "$GATE_OUT" "ERROR: driver tmux teardown")"
assert_contains "…the ERROR line is just the detail itself" "ERROR: driver.pid was never written" "$GATE_OUT"
assert_eq "…and it does NOT get the 'kill the survivor' hint (nothing to kill)" "no" \
  "$(contains "$GATE_OUT" "tmux kill-session")"

echo "== MUTATION — driver.pid exists but is EMPTY: a different sentence =="
# The wrapper ran, so the driver DID start; its sessions may be out there under
# a name nothing can match. Collapsing this into the case above would tell the
# operator the driver never ran, which is the opposite of true.
reset_case
CASE_DRIVER_BODY=': > "$PIDFILE"'
CASE_TMUX="leak"
run_gate pidfile_empty "$GATE_BLOCK"
assert_eq "the driver DID run" "yes" "$GATE_DRIVER_RAN"
assert_eq "the cell fails" "1" "$GATE_RC"
assert_eq "as an unreadable teardown" "driver_tmux_teardown_unreadable" "$(manifest_field .error)"
EMPTY_DETAIL="$(manifest_field .tmux_teardown_detail)"
assert_contains "the detail says the file is there but unusable" "is empty or unreadable" "$EMPTY_DETAIL"
assert_contains "…and that the driver may have left sessions behind" "may have left sessions behind" "$EMPTY_DETAIL"
assert_eq "…and it is NOT the same sentence as the missing-file case" "yes" \
  "$([[ "$EMPTY_DETAIL" != "$PIDFILE_DETAIL" ]] && echo yes || echo no)"

echo "== MUTATION — driver.pid holds a non-number: a third sentence, quoting it =="
reset_case
CASE_DRIVER_BODY='printf "not-a-pid\n" > "$PIDFILE"'
CASE_TMUX="leak"
run_gate pidfile_garbage "$GATE_BLOCK"
assert_eq "the cell fails" "1" "$GATE_RC"
assert_eq "as an unreadable teardown" "driver_tmux_teardown_unreadable" "$(manifest_field .error)"
GARBAGE_DETAIL="$(manifest_field .tmux_teardown_detail)"
assert_contains "the detail quotes the value it could not use" "'not-a-pid'" "$GARBAGE_DETAIL"
assert_contains "…and still says it is not a tmux problem" "Not a tmux problem" "$GARBAGE_DETAIL"
assert_eq "…and it is NOT the empty-file sentence" "yes" \
  "$([[ "$GARBAGE_DETAIL" != "$EMPTY_DETAIL" ]] && echo yes || echo no)"

echo "== the healthy path still reports a real pid, so the three arms are reachable =="
reset_case
CASE_TMUX="leak"
run_gate pidfile_healthy "$GATE_BLOCK"
assert_eq "a leak with a good pid is still a LEAK, not an unreadable check" \
  "driver_tmux_session_survived" "$(manifest_field .error)"
assert_eq "and the pid recorded in the manifest is a whole number" "yes" \
  "$(is_whole_number "$(manifest_field .driver_pid)")"

# ---------------------------------------------------------------------------
# ITEM 4 (#1828): the deadline clamp lives in ONE place
# ---------------------------------------------------------------------------
# clamp(lifetime/10, 1, 5) used to be computed twice — once here in run-cell.sh
# (around line 660), once in run-cell-multi.sh's record_driver_teardown (around
# line 377) — and tmux_teardown_deadline_for is now the only place it happens.

echo "== tmux_teardown_deadline_for: the clamp itself =="
assert_eq "floored at 1 (a 5s lifetime: 5/10=0)" "1" "$(tmux_teardown_deadline_for 5)"
assert_eq "floored at 1 (a 9s lifetime: 9/10=0)" "1" "$(tmux_teardown_deadline_for 9)"
assert_eq "the floor's own boundary (a 10s lifetime: 10/10=1)" "1" "$(tmux_teardown_deadline_for 10)"
assert_eq "exactly a tenth, no clamp needed (a 20s lifetime)" "2" "$(tmux_teardown_deadline_for 20)"
assert_eq "capped at 5 (a 60s lifetime: 60/10=6)" "5" "$(tmux_teardown_deadline_for 60)"
assert_eq "capped at 5 (a 900s lifetime)" "5" "$(tmux_teardown_deadline_for 900)"

echo "== both callers use the shared helper, and neither recomputes it =="
# A duplication tripwire, not a runtime assertion — proven the way every other
# check in this file is: mutate a COPY with the arithmetic re-inlined and show
# it goes RED. This is a narrow textual pattern (three greps over one file),
# not a general duplication detector — see the report for what that leaves
# uncovered (a THIRD call site computing the clamp under different variable
# names would not be caught by clamp_inlined's own literal "/ 10 ))" match, and
# nothing outside this suite runs it — this file is the only mechanical check).
RUN_CELL_MULTI="$SCRIPTS_DIR/run-cell-multi.sh"

clamp_inlined() {   # <file> → yes if the raw clamp arithmetic appears in it
  grep -qE '/ 10 \)\)' "$1" && grep -qE -- '-gt 5' "$1" && grep -qE -- '-lt 1' "$1" \
    && echo yes || echo no
}

assert_eq "run-cell.sh calls the shared helper" "yes" \
  "$(wired "$RUN_CELL" 'tmux_teardown_deadline_for')"
assert_eq "run-cell.sh does not inline the clamp itself" "no" "$(clamp_inlined "$RUN_CELL")"
assert_eq "run-cell-multi.sh calls the shared helper" "yes" \
  "$(wired "$RUN_CELL_MULTI" 'tmux_teardown_deadline_for')"
assert_eq "run-cell-multi.sh does not inline the clamp itself" "no" "$(clamp_inlined "$RUN_CELL_MULTI")"

echo "== MUTATION — a clamp re-inlined at a call site is caught =="
MUT_REINLINED="$TMP/run-cell-multi-with-reinlined-clamp.sh"
awk '
  /tmux_teardown_deadline_for "\$lifetime"/ {
    print "  deadline=$(( lifetime / 10 ))"
    print "  if [[ \"$deadline\" -gt 5 ]]; then deadline=5; fi"
    print "  if [[ \"$deadline\" -lt 1 ]]; then deadline=1; fi"
    next
  }
  { print }
' "$RUN_CELL_MULTI" > "$MUT_REINLINED"
assert_eq "the mutant really differs from the shipped file" "yes" \
  "$(cmp -s "$RUN_CELL_MULTI" "$MUT_REINLINED" && echo no || echo yes)"
assert_eq "…and the duplication tripwire catches the re-inlined clamp" "yes" \
  "$(clamp_inlined "$MUT_REINLINED")"

echo ""

echo "== teardown_timings_json: absent, empty and populated are THREE answers (#1828) =="
# The whole point of the pre-created file. "This recipe tore nothing down" is a
# legitimate, common answer, and it must not read the same as "the rig never
# set the path" or "the staging dir was unwritable". If those collapse, an
# instrumentation outage reports as a clean run with nothing to measure — the
# shape AGENTS.md forbids, and the shape a naive `rows == 0` check produces.
TJ_DIR="$(mktemp -d)"
trap 'rm -rf "$TJ_DIR"' EXIT

assert_eq "an absent file is unrecorded, not empty" "unrecorded" \
  "$(teardown_timings_json "$TJ_DIR/never-created.tsv" | jq -r '.status')"
assert_eq "no path at all is unrecorded too" "unrecorded" \
  "$(teardown_timings_json "" | jq -r '.status')"

: >"$TJ_DIR/empty.tsv"
assert_eq "an empty file means the recipe tore nothing down" "no_exit_clean" \
  "$(teardown_timings_json "$TJ_DIR/empty.tsv" | jq -r '.status')"
assert_eq "...and reports no maximum rather than a zero one" "null" \
  "$(teardown_timings_json "$TJ_DIR/empty.tsv" | jq -r '.max_s')"

printf '1\tsA\t0.4\tobserved\n1\tsB\t15.0\tcapped\n1\tsC\t2.6\tobserved\n' >"$TJ_DIR/rows.tsv"
TJ="$(teardown_timings_json "$TJ_DIR/rows.tsv")"
assert_eq "rows are recorded"        "recorded" "$(jq -r '.status' <<<"$TJ")"
assert_eq "...counted"               "3"        "$(jq -r '.rows'   <<<"$TJ")"
assert_eq "...with the maximum"      "15.0"     "$(jq -r '.max_s'  <<<"$TJ")"
# Reported SEPARATELY, never folded into max_s as if it were a measurement: a
# capped row is censored — the session was still alive when the deadline fired,
# so its true teardown time is unknown and GREATER than the cap. A table fitted
# without that distinction comes out tighter than the behaviour it describes.
assert_eq "...and the censored rows counted apart" "1" "$(jq -r '.capped' <<<"$TJ")"

if [[ "$fails" -eq 0 ]]; then
  echo "tmux-teardown-check_test: ALL PASS"
  exit 0
fi
echo "tmux-teardown-check_test: $fails FAILURE(S)" >&2
exit 1
