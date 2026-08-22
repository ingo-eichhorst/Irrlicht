#!/usr/bin/env bash
# pi-sessions-dir_test.sh — unit tests for
# replaydata/agents/pi/sessions-dir.sh, the resolution both pi drivers use to
# FIND the transcript pi just wrote. Plain bash, no framework, matching
# tools/lib/await-gone_test.sh. Run directly, or via tools/preflight.sh's
# `tools` gate and test.yml's tools/lib loop.
#
# WHAT IS BEING GRADED, and why the grader lives here rather than beside the
# library. The library is sourced by two scripts under replaydata/agents/pi/,
# a tree with no test runner of its own; tools/lib/*_test.sh is the corpus both
# CI and the pre-push hook already execute. So the test sits with its runner
# and reaches across, exactly as the workflow-guard tripwires in this directory
# reach into .github/workflows.
#
# Nothing here is a LOCK: the resolution is behaviour this change ADDS, so
# every row owes a mutation seen red. The mutations, all run against this file
# before the fix existed:
#
#   - restore `PI_SESSIONS_DIR="$HOME/.pi/agent/sessions"` in either driver →
#     the two DRIVER rows at the bottom go red (they are the regression tests
#     for the shipped defect, and they were red before the fix by definition:
#     that literal is what both drivers contained).
#   - drop the leading-slash test from either env branch → the two
#     "relative … is ignored" rows go red.
#   - let PI_CODING_AGENT_DIR win over PI_CODING_AGENT_SESSION_DIR → the
#     precedence row goes red.
#
# The precedence and the relative-value rule are not stylistic: they mirror
# core/adapters/inbound/agents/pi/adapter.go's sessionsDir() and
# agentpaths.FromEnv. A driver that resolved either differently would search a
# directory the DAEMON is not watching, which is the whole failure class here.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
LIB="$REPO_ROOT/replaydata/agents/pi/sessions-dir.sh"

# A missing subject is a hard failure, not a skip: exiting 0 here would read as
# a PASS to preflight's shell_lib_tests and to test.yml's loop, so the gate
# would go green having asserted nothing.
if [[ ! -f "$LIB" ]]; then
  echo "FAIL: pi-sessions-dir_test — subject not found at $LIB" >&2
  exit 1
fi
# shellcheck source=../../replaydata/agents/pi/sessions-dir.sh
source "$LIB"
if ! declare -F pi_sessions_dir >/dev/null; then
  echo "FAIL: pi-sessions-dir_test — sourcing $LIB defined no pi_sessions_dir" >&2
  exit 1
fi

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "$2" "$3"; fi
}

# Each row runs the function in a SUBSHELL with an explicit environment, so no
# row can leak a variable into the next one and pass on the previous row's
# setup.
resolve() (
  export HOME="$1"
  if [[ -n "$2" ]]; then export PI_CODING_AGENT_DIR="$2"; else unset PI_CODING_AGENT_DIR; fi
  if [[ -n "$3" ]]; then export PI_CODING_AGENT_SESSION_DIR="$3"; else unset PI_CODING_AGENT_SESSION_DIR; fi
  pi_sessions_dir
)

echo "== pi_sessions_dir =="

assert_eq "neither override set → the \$HOME default" \
  "/home/u/.pi/agent/sessions" "$(resolve /home/u '' '')"

assert_eq "PI_CODING_AGENT_DIR relocates the whole agent dir" \
  "/scratch/pi-home/sessions" "$(resolve /home/u /scratch/pi-home '')"

assert_eq "a trailing slash on PI_CODING_AGENT_DIR does not double up" \
  "/scratch/pi-home/sessions" "$(resolve /home/u /scratch/pi-home/ '')"

assert_eq "PI_CODING_AGENT_SESSION_DIR names the sessions dir itself" \
  "/scratch/sessions-only" "$(resolve /home/u '' /scratch/sessions-only)"

assert_eq "PI_CODING_AGENT_SESSION_DIR wins over PI_CODING_AGENT_DIR" \
  "/scratch/sessions-only" "$(resolve /home/u /scratch/pi-home /scratch/sessions-only)"

# agentpaths.FromEnv LOGS AND IGNORES a non-absolute override. A driver that
# honoured one would look under a path the daemon refused to watch.
assert_eq "a relative PI_CODING_AGENT_DIR is ignored, not honoured" \
  "/home/u/.pi/agent/sessions" "$(resolve /home/u relative/pi '')"

assert_eq "a relative PI_CODING_AGENT_SESSION_DIR falls through to the agent dir" \
  "/scratch/pi-home/sessions" "$(resolve /home/u /scratch/pi-home relative/sessions)"

assert_eq "an empty PI_CODING_AGENT_DIR is the same as unset" \
  "/home/u/.pi/agent/sessions" "$(resolve /home/u '' '')"

# --- the shipped defect, as a regression test -----------------------------
#
# Both pi drivers must resolve through the library rather than re-deriving the
# path. A grep is the right instrument here: the drivers are not sourceable
# (they run an agent), so the only thing that can be asserted statically is
# that the literal which caused the bug is gone and the resolver is called.
#
# The grep is asserted to have LOOKED: a missing driver file would make
# `grep -q` report "not found" for the bad literal and pass the row, which is
# absence and inability-to-look printing the same thing.
for d in driver.sh driver-interactive.sh; do
  path="$REPO_ROOT/replaydata/agents/pi/$d"
  if [[ ! -f "$path" ]]; then
    fail "driver $d exists to be checked" "a readable file at $path" "no such file"
    continue
  fi
  if grep -q 'HOME/\.pi/agent/sessions' "$path"; then
    fail "driver $d does not hardcode \$HOME/.pi/agent/sessions" \
      "no such literal" "$(grep -n 'HOME/\.pi/agent/sessions' "$path" | head -1)"
  else
    pass "driver $d does not hardcode \$HOME/.pi/agent/sessions"
  fi
  if grep -q 'pi_sessions_dir' "$path"; then
    pass "driver $d resolves through pi_sessions_dir"
  else
    fail "driver $d resolves through pi_sessions_dir" "a call to pi_sessions_dir" "none"
  fi
done

if (( fails > 0 )); then
  echo "pi-sessions-dir_test: $fails failure(s)"
  exit 1
fi
echo "pi-sessions-dir_test: all assertions passed"
