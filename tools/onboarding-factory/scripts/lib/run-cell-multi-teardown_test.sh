#!/usr/bin/env bash
# run-cell-multi-teardown_test.sh — the cross-adapter rig's tmux-teardown gate
# (#1825 / AC4). Plain bash, no framework. Run directly or via smoke-test.sh.
#
# lib/tmux-teardown-check_test.sh grades the LIBRARY: does a session carrying a
# given pid read as a leak, and does a lookup that could not be made refuse.
# This file grades what run-cell-multi.sh DOES with that verdict, which is a
# different set of decisions and the one the issue actually turns on:
#
#   * a leak is attributed to ONE adapter, by name, out of the several that
#     were running concurrently in the same workspace;
#   * one leaking driver fails the WHOLE run rather than that adapter's cell;
#   * "it leaked" and "the check could not be made" still fail under DIFFERENT
#     error codes, so the classifier can tell them apart (see
#     lib/classify-failure_test.sh);
#   * when both happened, the LEAK names the manifest — it is the finding with
#     an action attached — and the unreadable adapters stay listed.
#
# HOW IT GETS AT THEM. run-cell-multi.sh is a top-to-bottom script: sourcing it
# would spawn a daemon and launch real agents. So the two functions that carry
# the decisions are delimited by `# BEGIN <name>` / `# END <name>` markers,
# EXTRACTED from the script's source text here, and eval'd against a shadowed
# `tmux` and a stubbed write_error_manifest. That is the same extract-and-
# execute idiom tools/lib/*_test.sh uses for a workflow step's `run:` block,
# and it is what stops this suite from testing a copy of the logic that has
# drifted from the one that ships.
#
# The extraction is itself the thing most likely to rot — a renamed function or
# a dropped marker would leave every case below grading an empty string — so it
# refuses and exits rather than continuing with nothing, and the last section
# asserts the extracted functions are actually WIRED into the script's control
# flow, against a mutated copy with the wiring removed.
#
# THE MUTATION FIXTURES (AGENTS.md: a check a change ADDS has no "before the
# fix" to run red, so mutate what it protects and commit the mutation). Every
# case marked MUTATION below hands the gate a fake LEAKY DRIVER — a tmux whose
# session list carries that driver's pid — and asserts the run fails. Each is
# paired with the same fixture flipped clean, so the red is shown to come from
# the leak and not from the harness.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPTS_DIR="$(cd "$DIR/.." && pwd)"
RUN_CELL_MULTI="$SCRIPTS_DIR/run-cell-multi.sh"

command -v jq >/dev/null || { echo "run-cell-multi-teardown_test: jq is required" >&2; exit 2; }

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

# Spend no real time between looks. Read by the SOURCED await-gone.sh, which
# the linter cannot follow from here.
# shellcheck disable=SC2034
AWAIT_GONE_POLL_SECONDS=0.02

# --- Extract the two functions under test ----------------------------------

# extract_block <file> <name> — the lines strictly between `# BEGIN <name>` and
# `# END <name>`.
extract_block() {
  awk -v n="$2" '
    $0 ~ ("^# BEGIN " n "([ \t]|$)") { inb = 1; next }
    inb && $0 ~ ("^# END " n "[ \t]*$") { inb = 0; next }
    inb { print }
  ' "$1"
}

# load_block <name> — extract, refuse loudly if it came back unusable, eval.
# "The marker moved" and "the function is fine" must not both produce a green
# suite, so this exits rather than returning.
load_block() {
  local name="$1" src
  src="$(extract_block "$RUN_CELL_MULTI" "$name")"
  if [[ -z "$src" ]]; then
    echo "run-cell-multi-teardown_test: REFUSING — no '# BEGIN $name' … '# END $name' block in $RUN_CELL_MULTI." >&2
    echo "  Every case in this suite would have graded an empty string. Restore the markers or rename them here too." >&2
    exit 1
  fi
  if ! grep -q "^${name}() {" <<< "$src"; then
    echo "run-cell-multi-teardown_test: REFUSING — the '$name' marker block does not define ${name}()." >&2
    exit 1
  fi
  eval "$src" || { echo "run-cell-multi-teardown_test: REFUSING — '$name' does not eval" >&2; exit 1; }
  return 0
}

load_block record_driver_teardown
load_block tmux_teardown_verdict

echo "== the suite is grading the shipped functions, not a copy =="
assert_eq "record_driver_teardown was extracted and defined" "yes" \
  "$(declare -F record_driver_teardown >/dev/null && echo yes || echo no)"
assert_eq "tmux_teardown_verdict was extracted and defined" "yes" \
  "$(declare -F tmux_teardown_verdict >/dev/null && echo yes || echo no)"

# --- Harness ---------------------------------------------------------------

# fake_tmux <mode> [names…] — shadow tmux with one that models a server state.
# Same idiom (and the same three failure modes) as tmux-teardown-check_test.sh.
fake_tmux() {
  local mode="$1"; shift
  FAKE_TMUX_NAMES=("$@")
  case "$mode" in
    sessions) tmux() { printf '%s\n' "${FAKE_TMUX_NAMES[@]}"; return 0; } ;;
    noserver) tmux() { echo "no server running on /private/tmp/tmux-501/default" >&2; return 1; } ;;
    broken)   tmux() { echo "lost server" >&2; return 1; } ;;
    *) echo "fake_tmux: unknown mode $mode" >&2; return 1 ;;
  esac
  return 0
}

# The stub for the one thing the verdict reaches out to. Captures rather than
# writes, so a case can assert on the error code and the extras the real
# write_error_manifest would have put in run-manifest.json.
MANIFEST_CODE=""
MANIFEST_EXTRAS=""
MANIFEST_WRITES=0
write_error_manifest() {
  MANIFEST_CODE="$1"
  MANIFEST_EXTRAS="${2:-}"
  MANIFEST_WRITES=$((MANIFEST_WRITES + 1))
  return 0
}

# The run-level state record_driver_teardown accumulates into. Reset per case,
# exactly as run-cell-multi.sh initializes it before its wait loop.
reset_run() {
  TMUX_TEARDOWN_JSON="{}"
  # shellcheck disable=SC2034  # read and written by the EXTRACTED
  # record_driver_teardown / tmux_teardown_verdict (run-cell-multi.sh's
  # "# BEGIN …" blocks, eval'd by load_block above). The linter cannot follow an
  # eval, so it sees only this assignment — deleting it would leave each case
  # accumulating the previous case's leaks.
  TMUX_LEAKED=""
  # shellcheck disable=SC2034  # see above — a directive covers only the next
  # command, and this is the other half of the pair.
  TMUX_UNREADABLE=""
  MANIFEST_CODE=""
  MANIFEST_EXTRAS=""
  MANIFEST_WRITES=0
  return 0
}

# A 10s lifetime keeps await_gone_bound's 10:1 wall satisfied at the 1s floor
# the function computes, so a leaked case costs one second rather than five.
LIFETIME=10
PID_A=95883      # kiro-cli's driver
PID_B=95884      # claudecode's driver

measure() {   # <adapter> <pid> — one wait-loop iteration's worth of the gate
  record_driver_teardown "$1" "$2" "$LIFETIME" >/dev/null 2>&1
  return 0
}

# run_verdict — sets VERDICT to pass / fail-the-run. Deliberately NOT a
# function whose answer is captured with $(…): the verdict's whole job is the
# SIDE EFFECT of calling write_error_manifest, and a command substitution runs
# it in a subshell where the stub's captures are thrown away — which reads as
# "the gate wrote no manifest" no matter what it did.
VERDICT=""
run_verdict() {
  if tmux_teardown_verdict >/dev/null 2>&1; then VERDICT="pass"; else VERDICT="fail-the-run"; fi
  return 0
}

# --- Cases -----------------------------------------------------------------

echo "== both drivers tore their sessions down: the run passes =="
# Somebody else's sessions are present throughout, so a pass here also shows
# the gate is scoped to THIS run rather than to "any tmux on the host".
reset_run
fake_tmux sessions "irc" "work" "claudecode-onboard-1787641617-11111"
measure kiro-cli "$PID_A"
measure claudecode "$PID_B"
run_verdict
assert_eq "verdict" "pass" "$VERDICT"
assert_eq "no ERROR manifest is written" "0" "$MANIFEST_WRITES"
assert_eq "both adapters recorded clean" "clean clean" \
  "$(jq -r '[.["kiro-cli"].status, .claudecode.status] | join(" ")' <<< "$TMUX_TEARDOWN_JSON")"

echo "== MUTATION — ONE of two drivers leaks: the WHOLE run fails, and it is named =="
# The decision this file exists to pin. A cross-adapter run stages one fixture
# per adapter but curates every one of them over the SHARED workspace, so a
# still-live agent is inside all of them; there is also only one
# run-manifest.json, so "adapter A is fine" is not a state this rig can express.
reset_run
fake_tmux sessions "work" "kiro-clidrv-$PID_A-1787641617-r1" "irc"
measure kiro-cli "$PID_A"
measure claudecode "$PID_B"
run_verdict
assert_eq "verdict" "fail-the-run" "$VERDICT"
assert_eq "exactly one ERROR manifest" "1" "$MANIFEST_WRITES"
assert_eq "under the leak's own error code" "driver_tmux_session_survived" "$MANIFEST_CODE"
assert_eq "the leaking adapter is named" "kiro-cli" \
  "$(jq -r '.tmux_teardown_leaked' <<< "$MANIFEST_EXTRAS")"
assert_eq "the OTHER adapter is not blamed" "clean" \
  "$(jq -r '.tmux_teardown.claudecode.status' <<< "$MANIFEST_EXTRAS")"
assert_contains "the survivor is named so it can be killed" \
  "kiro-clidrv-$PID_A-1787641617-r1" \
  "$(jq -r '.tmux_teardown["kiro-cli"].detail' <<< "$MANIFEST_EXTRAS")"

echo "== the same fixture, flipped clean: the run passes (the red came from the leak) =="
reset_run
fake_tmux sessions "work" "kiro-clidrv-11111-1787641617-r1" "irc"
measure kiro-cli "$PID_A"
measure claudecode "$PID_B"
run_verdict
assert_eq "verdict" "pass" "$VERDICT"

echo "== MUTATION — a driver whose teardown CANNOT BE CHECKED fails, under a different code =="
# Not the same failure and not the same answer: nothing was established either
# way, so there is no session to go kill. Collapsing the two is the bug #1825
# is about, one layer up.
reset_run
fake_tmux noserver
measure kiro-cli "$PID_A"
fake_tmux broken
measure claudecode "$PID_B"
run_verdict
assert_eq "verdict" "fail-the-run" "$VERDICT"
assert_eq "under the unreadable code, NOT the leak code" "driver_tmux_teardown_unreadable" "$MANIFEST_CODE"
assert_eq "the unreadable adapter is named" "claudecode" \
  "$(jq -r '.tmux_teardown_unreadable' <<< "$MANIFEST_EXTRAS")"
assert_eq "nothing is reported as leaked" "" \
  "$(jq -r '.tmux_teardown_leaked' <<< "$MANIFEST_EXTRAS")"
assert_contains "the reason quotes what tmux actually said" "lost server" \
  "$(jq -r '.tmux_teardown.claudecode.detail' <<< "$MANIFEST_EXTRAS")"

echo "== MUTATION — a leak AND an unreadable check: the LEAK names the manifest =="
# The leak is the finding with an action attached, so it is the code an operator
# reads first; the unreadable adapter still has to survive into the detail.
reset_run
fake_tmux sessions "kiro-clidrv-$PID_A-1787641617-r1"
measure kiro-cli "$PID_A"
fake_tmux broken
measure claudecode "$PID_B"
run_verdict
assert_eq "verdict" "fail-the-run" "$VERDICT"
assert_eq "the leak wins the error code" "driver_tmux_session_survived" "$MANIFEST_CODE"
assert_eq "and the unreadable adapter is not lost" "claudecode" \
  "$(jq -r '.tmux_teardown_unreadable' <<< "$MANIFEST_EXTRAS")"
assert_eq "a one-line summary carries both" \
  "leaked=[kiro-cli] unreadable=[claudecode]" \
  "$(jq -r '.tmux_teardown_detail' <<< "$MANIFEST_EXTRAS")"

echo "== a driver with no usable pid is a refusal, never a pass =="
# run-cell-multi.sh takes the pid from `$!`, so an empty one means the launch
# loop changed shape. Every surviving session would then read as somebody
# else's and the gate would pass vacuously.
reset_run
fake_tmux sessions "kiro-clidrv-$PID_A-1787641617-r1"
measure kiro-cli ""
run_verdict
assert_eq "verdict" "fail-the-run" "$VERDICT"
assert_eq "under the unreadable code" "driver_tmux_teardown_unreadable" "$MANIFEST_CODE"

unset -f tmux

# --- The gate is WIRED into the script's control flow ----------------------
# Two perfectly-tested functions that nothing calls is the failure mode #1825 is
# about, one layer out. Each assertion is paired with the same assertion against
# a MUTATED COPY with the wiring deleted, so a tripwire that has stopped being
# able to look is red rather than green.

echo "== the gate is WIRED: run-cell-multi.sh actually runs it (#1825) =="
assert_eq "run-cell-multi.sh is readable at all" "yes" \
  "$([[ -r "$RUN_CELL_MULTI" && -s "$RUN_CELL_MULTI" ]] && echo yes || echo no)"

wired() { grep -qE "$2" "$1" && echo yes || echo no; }

MUTANT="$TMP/run-cell-multi-without-the-gate.sh"
grep -v -e 'tmux-teardown-check' -e 'record_driver_teardown ' -e 'tmux_teardown_verdict ||' \
        -e 'DRV_TIMEOUTS' "$RUN_CELL_MULTI" > "$MUTANT"
assert_eq "the mutant really is different from the original" "yes" \
  "$(cmp -s "$RUN_CELL_MULTI" "$MUTANT" && echo no || echo yes)"

assert_eq "sources the library" "yes" "$(wired "$RUN_CELL_MULTI" 'lib/tmux-teardown-check\.sh')"
assert_eq "  …and the mutant does not" "no" "$(wired "$MUTANT" 'lib/tmux-teardown-check\.sh')"

assert_eq "measures inside the wait loop" "yes" \
  "$(wired "$RUN_CELL_MULTI" '^[[:space:]]*record_driver_teardown "\$\{DRV_ADAPTERS\[\$i\]\}" "\$\{DRV_PIDS\[\$i\]\}"')"
assert_eq "  …and the mutant does not" "no" \
  "$(wired "$MUTANT" '^[[:space:]]*record_driver_teardown "')"

assert_eq "acts on the verdict, and a failure exits" "yes" \
  "$(wired "$RUN_CELL_MULTI" '^tmux_teardown_verdict \|\| exit 1')"
assert_eq "  …and the mutant does not" "no" "$(wired "$MUTANT" '^tmux_teardown_verdict \|\| exit 1')"

assert_eq "captures each driver's own timeout for the bound" "yes" \
  "$(wired "$RUN_CELL_MULTI" 'DRV_TIMEOUTS\+=\("\$timeout_s"\)')"
assert_eq "  …and the mutant does not" "no" "$(wired "$MUTANT" 'DRV_TIMEOUTS')"

# Both verdicts must be able to fail the run, under DIFFERENT codes — the same
# pair lib/classify-failure.sh has arms for.
assert_eq "a survivor fails the run under its own error code" "yes" \
  "$(wired "$RUN_CELL_MULTI" 'driver_tmux_session_survived')"
assert_eq "an unreadable lookup fails it under a DIFFERENT one" "yes" \
  "$(wired "$RUN_CELL_MULTI" 'driver_tmux_teardown_unreadable')"

# The pid this rig matches session names against is `$!`, which is the driver's
# own `$$` only because the backgrounded command is a SIMPLE command bash execs
# in the forked child. A pipeline or a `( … )` there would hand back a
# different process's pid and the gate would match nothing, forever, silently.
assert_eq "the driver is launched as a simple backgrounded command" "yes" \
  "$(wired "$RUN_CELL_MULTI" '^[[:space:]]*>"\$sub/driver\.out" 2>&1 &$')"
assert_eq "and its pid is taken straight from \$!" "yes" \
  "$(wired "$RUN_CELL_MULTI" '^[[:space:]]*DRV_PIDS\+=\(\$!\)$')"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "run-cell-multi-teardown_test: ALL PASS"
  exit 0
fi
echo "run-cell-multi-teardown_test: $fails FAILURE(S)" >&2
exit 1
