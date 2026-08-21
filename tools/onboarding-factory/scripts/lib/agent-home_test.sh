#!/usr/bin/env bash
# agent-home_test.sh — unit tests for agent-home.sh. Plain bash (no framework),
# matching the style of golden-scope_test.sh. Run directly or via
# scripts/smoke-test.sh.
#
# Why this exists. agent_home_isolate replaced two hand-written per-adapter
# blocks — copilot's staging default in run-cell.sh AND run-cell-multi.sh, and
# codex's opt-in absolute-only guard in run-cell.sh only — neither of which had
# any coverage. Per AGENTS.md, "a rewritten guard replays its predecessor's
# cases or it is not known to be a superset", so the copilot and codex cases
# below are transcribed from the blocks that were deleted, not invented for the
# new function:
#
#   copilot  unset  → exported to the staging default, directory created
#   copilot  set    → the caller's value wins over the staging default
#   codex    unset  → nothing exported (isolation is opt-in for credentials)
#   codex    set    → exported, directory created
#   codex    relative → HARD FAILURE, because the daemon's agentpaths.FromEnv
#                     ignores a relative value and falls back to $HOME (#1388)
#
# Everything else here is new behaviour and therefore owes a deliberate
# mutation rather than a "before" run. The mutations are committed beside the
# assertions rather than described in a PR body: `mutation_*` below re-declares
# a deliberately WRONG table or a deliberately WRONG guard and asserts the
# suite's own claims flip. Without them, a function that exported nothing at all
# would satisfy several of the assertions above (an unset opt-in var is
# indistinguishable from a broken exporter).

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=agent-home.sh
source "$DIR/agent-home.sh"

fails=0
pass() { local label="$1"; echo "  PASS: $label"; return 0; }
fail() { local label="$1" expected="$2" got="$3"; echo "  FAIL: $label — expected [$expected] got [$got]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  local label="$1" expected="$2" got="$3"
  [[ "$got" == "$expected" ]] && pass "$label" || fail "$label" "$expected" "$got"
  return 0
}

TMP="$(mktemp -d -t agent-home-test)"
mkdir -p "$TMP/probe"
trap 'rm -rf "$TMP"' EXIT

# run_isolate <adapter> <staging-default> [VAR=value ...] runs
# agent_home_isolate in a CHILD bash and publishes three globals: RUN_STATUS,
# RUN_VALUE (the adapter's variable as a CHILD process sees it) and RUN_OUT (its
# stdout+stderr, newlines flattened).
#
# The value is read back through `env` in a child rather than from the calling
# shell, because that is the only thing that distinguishes the property under
# test from a plain assignment: the daemon is spawned as a child process and
# inherits the environment, so a function that set the variable without
# EXPORTing it would leave every in-shell assertion green and the daemon on the
# real home. That is this file's own version of "assert the operation actually
# happened".
#
# Three separate files rather than one delimited string: the reported output is
# multi-line by design (the relative-path refusal prints four lines), and a
# field-splitting parse over it silently mis-assigns every column — which is how
# this helper's first draft reported fifteen failures that were all its own.
RUN_STATUS=""; RUN_VALUE=""; RUN_OUT=""
run_isolate() {
  local adapter="$1" staging="$2"; shift 2
  local var
  var="$(agent_home_var "$adapter")"
  env "$@" bash -c '
    source "$1/agent-home.sh"
    agent_home_isolate "$2" "$3"
    st=$?
    printf "%d" "$st" > "$4/status"
    # Read back through a CHILD `env`, never from this shell: an unexported
    # assignment is invisible to the daemon and to nothing else.
    if [[ -n "$5" ]]; then env | sed -n "s/^$5=//p" > "$4/value"; else : > "$4/value"; fi
  ' _ "$DIR" "$adapter" "$staging" "$TMP/probe" "$var" >"$TMP/probe/out" 2>&1
  RUN_STATUS="$(cat "$TMP/probe/status" 2>/dev/null)"
  RUN_VALUE="$(cat "$TMP/probe/value" 2>/dev/null)"
  RUN_OUT="$(cat "$TMP/probe/out" 2>/dev/null | tr '\n' ' ')"
  return 0
}

echo "== the table is not empty (vacuity guard) =="
# Every assertion below is satisfied by an empty table: agent_home_var returns
# nothing, agent_home_isolate takes the "no row" branch and exits 0, and the
# whole suite passes having exercised no adapter at all.
rows="$(agent_home_table | grep -c .)"
[[ "$rows" -ge 4 ]] && pass "table declares $rows adapters" \
  || fail "table declares at least 4 adapters" ">=4" "$rows"

echo "== every row is well formed =="
malformed="$(agent_home_table | awk 'NF != 3 || $2 !~ /^[A-Z_]+$/ || ($3 != "default" && $3 != "optin") { print NR": "$0 }')"
assert_eq "no malformed rows" "" "$malformed"

echo "== predecessor's cases: copilot (policy=default) =="
run_isolate copilot "$TMP/cop" -u COPILOT_HOME
assert_eq "unset → exit 0" "0" "$RUN_STATUS"
assert_eq "unset → exported to the staging default" "$TMP/cop" "$RUN_VALUE"
[[ -d "$TMP/cop" ]] && pass "unset → staging dir created" || fail "unset → staging dir created" "dir" "missing"

run_isolate copilot "$TMP/cop-default" COPILOT_HOME="$TMP/cop-explicit"
assert_eq "explicit value wins over the staging default" "$TMP/cop-explicit" "$RUN_VALUE"

echo "== predecessor's cases: codex (policy=optin) =="
run_isolate codex "$TMP/unused" -u CODEX_HOME
assert_eq "unset → exit 0" "0" "$RUN_STATUS"
assert_eq "unset → NOTHING exported (credentials live in CODEX_HOME)" "" "$RUN_VALUE"
[[ -d "$TMP/unused" ]] && fail "unset → no staging dir created" "missing" "dir" \
  || pass "unset → no staging dir created"
case "$RUN_OUT" in
  *"NOT isolated"*) pass "unset → says so out loud" ;;
  *) fail "unset → says so out loud" "a 'NOT isolated' line" "$RUN_OUT" ;;
esac

run_isolate codex "$TMP/unused2" CODEX_HOME="$TMP/codex-home"
assert_eq "set → exit 0" "0" "$RUN_STATUS"
assert_eq "set → exported" "$TMP/codex-home" "$RUN_VALUE"

# That last assertion does NOT discriminate against a missing `export`, and the
# measurement is why this arm exists: run_isolate hands the value in through
# `env VAR=…`, so the child already carries it exported and re-exporting is a
# no-op. Deleting the export from agent_home_isolate reddens exactly ONE of the
# assertions above — copilot's staging default, the only case where the function
# introduces a variable that was previously unset.
#
# An operator who sets the variable as a plain shell variable and runs the rig
# in the same shell is the case that needs the export, so it is driven directly
# here rather than through run_isolate.
got="$(bash -c '
  source "$1/agent-home.sh"
  CODEX_HOME="$2"          # set, deliberately NOT exported
  agent_home_isolate codex /unused >/dev/null 2>&1
  env | sed -n "s/^CODEX_HOME=//p"
' _ "$DIR" "$TMP/codex-noexport")"
assert_eq "an unexported caller variable is exported onward to the daemon" "$TMP/codex-noexport" "$got"

run_isolate codex "$TMP/unused3" CODEX_HOME="relative/codex"
assert_eq "RELATIVE → hard failure" "1" "$RUN_STATUS"
case "$RUN_OUT" in
  *"must be absolute"*) pass "relative → names why" ;;
  *) fail "relative → names why" "'must be absolute'" "$RUN_OUT" ;;
esac

echo "== the two adapters this file was added for =="
for pair in "kiro-cli KIRO_HOME" "mistral-vibe VIBE_HOME"; do
  set -- $pair
  a="$1"; v="$2"
  assert_eq "$a declares $v" "$v" "$(agent_home_var "$a")"
  assert_eq "$a is opt-in" "optin" "$(agent_home_policy "$a")"
  run_isolate "$a" "$TMP/unused-$a" -u "$v"
  assert_eq "$a unset → nothing exported" "" "$RUN_VALUE"
  run_isolate "$a" "$TMP/unused-$a" "$v=$TMP/$a-home"
  assert_eq "$a set → exported" "$TMP/$a-home" "$RUN_VALUE"
  run_isolate "$a" "$TMP/unused-$a" "$v=rel/$a"
  assert_eq "$a relative → hard failure" "1" "$RUN_STATUS"
done

echo "== an adapter with no row is reported, never silent =="
run_isolate aider "$TMP/unused-aider"
assert_eq "no row → exit 0" "0" "$RUN_STATUS"
case "$RUN_OUT" in
  *"no home override is wired"*) pass "no row → says so out loud" ;;
  *) fail "no row → says so out loud" "a 'no home override' line" "$RUN_OUT" ;;
esac

echo "== refusals =="
out="$(agent_home_isolate 2>&1)"; st=$?
assert_eq "no adapter → exit 1" "1" "$st"
case "$out" in *usage*) pass "no adapter → usage line" ;; *) fail "no adapter → usage line" "usage" "$out" ;; esac

# A 'default' row with no staging directory to fall back on is a caller bug, and
# the one shape where doing nothing would look identical to working: the var
# stays unset, the adapter records against the real home, and every other
# assertion here is still green.
out="$(env -u COPILOT_HOME bash -c 'source "$1/agent-home.sh"; agent_home_isolate copilot 2>&1' _ "$DIR")"; st=$?
assert_eq "default policy + no staging dir → exit 1" "1" "$st"
case "$out" in
  *"no staging directory"*) pass "default policy + no staging dir → names why" ;;
  *) fail "default policy + no staging dir → names why" "'no staging directory'" "$out" ;;
esac

# ---------------------------------------------------------------------------
# Committed mutation evidence. Each block re-declares one piece of the lib the
# WRONG way and asserts the property above actually flips — because an assertion
# that would pass against a broken implementation is not evidence of anything.
# ---------------------------------------------------------------------------
echo "== mutation: an exporter that assigns without exporting =="
# The whole point of running before the daemon spawns is that the daemon
# INHERITS the value. A local assignment satisfies every in-shell check.
mut="$(env -u COPILOT_HOME bash -c '
  source "$1/agent-home.sh"
  agent_home_isolate() { local v; v="$(agent_home_var "$1")"; eval "$v=\"\$2\""; return 0; }
  agent_home_isolate copilot "$2" >/dev/null
  env | grep -c "^COPILOT_HOME=" || true
' _ "$DIR" "$TMP/mut-cop")"
assert_eq "MUTATION: unexported assignment is invisible to a child" "0" "$mut"

echo "== mutation: the relative-path guard removed =="
# Deleting the guard makes a relative value succeed, which is exactly the #1388
# shape: the driver uses "rel/codex" while the daemon silently falls back to
# $HOME/.codex.
mut="$(env CODEX_HOME="rel/codex" bash -c '
  source "$1/agent-home.sh"
  agent_home_isolate() {
    local v; v="$(agent_home_var "$1")"
    export "$v=${!v}"; return 0
  }
  agent_home_isolate codex "$2" >/dev/null 2>&1; printf "%d" "$?"
' _ "$DIR" "$TMP/unused-mut")"
assert_eq "MUTATION: without the guard a relative value passes" "0" "$mut"

echo "== mutation: a table row naming the wrong variable =="
# The rig exports what the table says. A row naming a variable the daemon does
# not read exports something nothing honours — daemon on the real home, CLI on
# the isolated one, empty fixture. Nothing in THIS file can catch that; it is
# what righome's Go tripwire grades, and the assertion here is only that the
# table is what decides.
mut="$(env -u KIRO_HOME bash -c '
  source "$1/agent-home.sh"
  agent_home_table() { printf "%s\n" "kiro-cli WRONG_HOME optin"; }
  agent_home_var kiro-cli
' _ "$DIR")"
assert_eq "MUTATION: the table alone decides the variable" "WRONG_HOME" "$mut"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "agent-home_test.sh: ALL PASS"
  exit 0
fi
echo "agent-home_test.sh: $fails FAILED" >&2
exit 1
