#!/usr/bin/env bash
# recipe-runtime-mutation_test.sh — the RED half of recipe-runtime_test.sh.
#
# recipe-runtime.sh is all-new checking: every assertion in its unit test
# passes the moment the code is written, so a green suite there proves the
# tests run, not that they REACH anything. AGENTS.md's rule for a check a
# change adds is to mutate what it protects and confirm the check goes red —
# and to commit the mutation as a fixture rather than describe it in a PR body
# nothing re-runs. This file is that fixture.
#
# It copies recipe-runtime.sh, breaks ONE clause, points the unit suite at the
# broken copy, and asserts the suite FAILS. A mutation that leaves the suite
# green is reported as a hole in the tests, which is the finding worth having.
#
# It is deliberately its own file rather than a mode of the unit test: the two
# answer different questions, and a mutation harness that shares a process with
# the suite it mutates is one `source` away from testing itself.
#
# Runs in ~60s (measured: eight forked copies of the unit suite, each of which
# waits on a real socket). It is the slowest file in scripts/lib/ by an order
# of magnitude, and that is the price of the evidence — but it is stated here
# rather than guessed, because the first version of this line said "~2s".
# Wired into the same places recipe-runtime_test.sh is.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
SUBJECT="$DIR/recipe-runtime.sh"
SUITE="$DIR/recipe-runtime_test.sh"

command -v jq >/dev/null || { echo "recipe-runtime-mutation_test: jq is required" >&2; exit 2; }
for f in "$SUBJECT" "$SUITE"; do
  [[ -f "$f" ]] || { echo "recipe-runtime-mutation_test: missing $f" >&2; exit 2; }
done

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
MUT_PORT_SEQ=0

# run_mutation <name> <expected-failing-assertion> <sed-expr>
#   Applies the sed expression to a COPY of recipe-runtime.sh in a scratch dir
#   alongside a copy of the suite, runs the suite there, and asserts that the
#   suite fails AND that the named assertion is among the failures.
#
#   Three guards, because a mutation harness that silently stops mutating is
#   the canonical instance of AGENTS.md's "a verification mechanism must fail
#   loudly when it cannot run":
#     1. the sed must actually CHANGE the file (a clause someone reworded
#        would otherwise mutate to a no-op and report a green suite as proof);
#     2. the mutated copy must still be syntactically valid bash (a mutation
#        that only breaks the parse proves nothing about the check);
#     3. the failure must be the EXPECTED one. This third guard was added after
#        the harness caught itself: the bare_mode mutation below first reddened
#        `bare_mode true` — it had broken the jq filter outright rather than
#        loosening it — so a mutation aimed at the string/boolean distinction
#        was being credited for breaking something else entirely. "Some
#        assertion went red" is not evidence about THIS clause.
run_mutation() {
  local name="$1" want_fail="$2" expr="$3"
  local d="$TMP/$name"
  mkdir -p "$d"
  # Each mutation gets its OWN port. All eight copies used to inherit the same
  # hardcoded one, so they could collide with each other and with any stray
  # listener — and the collision did not fail, it made the readiness assertion
  # observe a socket belonging to something else.
  MUT_PORT_SEQ=$((MUT_PORT_SEQ + 1))
  local mut_port=$(( 19900 + ($$ % 50) * 10 + MUT_PORT_SEQ ))
  cp "$SUITE" "$d/recipe-runtime_test.sh"
  sed -E "$expr" "$SUBJECT" > "$d/recipe-runtime.sh"

  if cmp -s "$SUBJECT" "$d/recipe-runtime.sh"; then
    echo "  FAIL $name: the mutation changed nothing — the clause it targets has moved or been reworded" >&2
    fails=$((fails + 1))
    return
  fi
  if ! bash -n "$d/recipe-runtime.sh" 2>/dev/null; then
    echo "  FAIL $name: the mutated copy does not parse — it proves nothing about the check" >&2
    fails=$((fails + 1))
    return
  fi

  # `timeout`: a mutation can hang the suite — the readiness probe's deadline is
  # the obvious one, and a mutant that neutered it would wait forever. Without a
  # bound, one bad mutation stalls the whole gate with no output. macOS has no
  # timeout(1) by default, so fall back to running it bare rather than skipping
  # the mutation entirely: a missing bound is worse than no bound only if it is
  # silent, and this says so.
  local out rc
  if command -v timeout >/dev/null 2>&1; then
    out="$(IRR_TEST_NC_PORT="$mut_port" timeout 30 bash "$d/recipe-runtime_test.sh" 2>&1)"; rc=$?
    if [[ "$rc" -eq 124 ]]; then
      echo "  FAIL $name: the mutated suite TIMED OUT after 30s — it hung rather than failing" >&2
      fails=$((fails + 1))
      return
    fi
  else
    out="$(IRR_TEST_NC_PORT="$mut_port" bash "$d/recipe-runtime_test.sh" 2>&1)"; rc=$?
  fi
  if [[ "$rc" -eq 0 ]]; then
    echo "  FAIL $name: suite stayed GREEN under the mutation — nothing tests this clause" >&2
    fails=$((fails + 1))
    return
  fi
  # -F: want_fail is a literal assertion name, not a pattern. It was a
  # regex for exactly one run, and "(string)" silently matched "string" —
  # the harness reported a hole that was not there.
  if ! grep -qF "FAIL $want_fail" <<<"$out"; then
    echo "  FAIL $name: suite went red, but NOT on \"$want_fail\" — the mutation broke something else" >&2
    grep 'FAIL ' <<<"$out" | sed 's/^ */    saw: /' >&2
    fails=$((fails + 1))
    return
  fi
  echo "  ok   $name → RED on \"$want_fail\""
}

echo "== mutations: each must turn recipe-runtime_test.sh red =="

# A: accept a mock package anywhere in the repo. The confinement is what stops
#    a recipe naming an arbitrary package that run-cell.sh then BUILDS and RUNS.
run_mutation package-confinement-dropped 'mock package outside recording tree' \
  's|^    \./tools/onboarding-factory/recording/mock-\*\) ;;|    *) ;;|'

# A1: accept a port that is not a JSON number. This is the half that stops a
#     quoted "0900" from ever reaching bash arithmetic, where an invalid octal
#     literal ERRORS rather than evaluating false and the guard's
#     `[[ ! … ]] || (( … ))` shape then accepted it.
run_mutation port-type-check-dropped 'mock port as a JSON string' \
  's/if \(\.mock\.port \| type\) == "number" then/if true then/'

# A2: stop requiring request_log_pattern. Without it run-cell.sh's
#     assert_mock_was_used has no pattern to count, and "the mock served
#     nothing" becomes indistinguishable from "I was given no way to look".
run_mutation request-log-pattern-not-required 'mock without request_log_pattern' \
  's/^  if \[\[ -z "\$\(jq -r .\.mock\.request_log_pattern \/\/ empty. <<<"\$cell"\)" \]\]; then/  if false; then/'

# B: accept any port. Without this a recipe can ask the rig to bind :80.
run_mutation port-range-dropped 'mock privileged port' \
  's/\(\( 10#\$port < 1024 \|\| 10#\$port > 65535 \)\)/(( 0 ))/'

# C: treat a truthy-looking string as bare_mode true. A recipe spelling
#    "bare_mode": "true" would then run WITHOUT --bare and reach the real
#    provider with the operator's real credentials.
run_mutation bare_mode-string-accepted 'bare_mode "true" (string) is not true' \
  's/if \.bare_mode == true then/if (.bare_mode | tostring) == "true" then/'

# D: stop refusing an unsatisfiable mock placeholder. The agent would be handed
#    a literal ANTHROPIC_BASE_URL=http://{{MOCK_ADDR}} — configured-looking and
#    non-functional.
run_mutation unresolved-placeholder-passed-through 'unknown placeholder survives substitution' \
  's/^    if \[\[ "\$v" =~ \\\{\\\{\[A-Za-z0-9_\]\+\\\}\\\} \]\]; then/    if false; then/'

# E: stop refusing a newline in an env value — it would become one variable
#    plus one silently-dropped garbage line.
run_mutation newline-in-env-value-accepted 'env value contains a newline' \
  "s/^    if \[\[ \"\\\$v\" == \*\\\$'\\\\n'\* \]\]; then/    if false; then/"

# F: stop refusing an env name that is not a shell identifier.
run_mutation env-name-unchecked 'env name is not a shell identifier' \
  's/^    if \[\[ ! "\$k" =~ \^\[A-Za-z_\]\[A-Za-z0-9_\]\*\$ \]\]; then/    if false; then/'

# G: report no driver gaps. This is the refusal that stops a recipe recording
#    against the real provider on a driver that ignores env entirely.
run_mutation driver-gaps-always-empty 'env on a driver that does NOT' \
  's/^recipe_runtime_driver_gaps\(\) \{/recipe_runtime_driver_gaps() { return 0;/'

# H: make the readiness probe always succeed. A mock that never came up would
#    then be driven against, and the recording would be of nothing.
run_mutation readiness-probe-always-ready 'nothing listening' \
  's/^recipe_runtime_wait_listening\(\) \{/recipe_runtime_wait_listening() { return 0;/'

# --- the two post-drive guards (#1803 review) --------------------------------
# These decide whether a recording is trustworthy and had NO committed fixture
# until the review said so: they were exercised by three live recordings and
# nothing else. Each mutation below is a way the guard could pass while the
# recording was actually made against the real provider.

# I: accept a missing receipt. A driver that ignores $STAGING/driver-env then
#    records a healthy-looking fixture against production credentials.
run_mutation env-receipt-not-required 'env requested, NO receipt written' \
  's/^  if \[\[ ! -f "\$staging\/driver-env.applied" \]\]; then/  if false; then/'

# J: stop comparing want against got. A driver that applied SOME of the set —
#    env but not --bare, the case that silently restores the keychain — passes.
run_mutation env-receipt-not-compared 'env receipted but --bare silently dropped' \
  's/^  if \[\[ "\$want" != "\$got" \]\]; then/  if false; then/'

# K: treat an unreadable mock log as "fine". This is the inability-to-look half
#    of the finding: a missing log would report success.
run_mutation mock-log-unreadable-ignored 'mock declared but its log is unreadable' \
  's/^  if \[\[ ! -r "\$staging\/mock.log" \]\]; then/  if false; then/'

# L: accept zero matches. The mock ran, served nothing, and the recording is of
#    the agent talking to the real provider.
run_mutation mock-zero-hits-accepted 'banner only → the mock served nothing' \
  's/^  if \[\[ "\$hits" -eq 0 \]\]; then/  if false; then/'

# M: THE finding itself — hardcode claudecode's log format instead of reading
#    the cell's declared pattern. Every non-claudecode mock then counts zero
#    however many requests it served, and the guard reports the exact opposite
#    of the truth.
run_mutation mock-pattern-hardcoded 'a NON-claudecode mock log is counted by its own pattern' \
  's/^  pattern="\$\(recipe_runtime_mock_pattern "\$cell"\)"/  pattern="POST \/v1\/"/'

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "recipe-runtime-mutation_test: ALL PASS (every mutation was seen red)"
else
  echo "recipe-runtime-mutation_test: $fails FAILED" >&2
  exit 1
fi
