#!/usr/bin/env bash
# error-retention-mutations_test.sh — the committed mutation fixtures for
# #1815's error-retention guards.
#
# WHY THIS FILE EXISTS. #1815 adds three things that have no "before the fix" to
# run red: two startup-sweep exemptions and a structural tripwire. Per AGENTS.md
# and docs/testing-philosophy.md, such a check earns its place only by having
# been seen to fail when the thing it protects is broken — and "commit that
# mutation as a fixture rather than only describing it in a PR body nothing
# re-runs". These mutations were run by hand during #1815 and were correctly
# red; a hand-run mutation proves the guard worked THAT DAY and nothing after.
# Here they run on every `tools/preflight.sh --only tools`.
#
# WHAT EACH ROW ASSERTS, and why these three:
#
#   1. THE REAPER TRIPWIRE, mutated by removing the exemption from a delegate
#      predicate (reapsIdleTopLevel). This is the case an earlier version of
#      the tripwire got WRONG: it accepted a hand-typed list of delegate names,
#      so deleting the guard from a listed predicate left it green — the caller
#      was still calling a name on the list. The tripwire now derives delegation
#      from the call graph, and this row is what keeps that true.
#
#   2. THE STARTUP SWEEP exemption in isStartupZombie — the deleter #1815 was
#      filed about.
#
#   3. THE SEED-TIME sweep exemption in seedAlivePIDs' PID>0 branch — the
#      SECOND deleter, which the issue did not name. Exempting only the first
#      leaves the row to die at this one, so a mutation that only proved (2)
#      would not have caught shipping (3) unexempted.
#
# Rows 2 and 3 are separate on purpose: they fail independently, and a single
# combined mutation could pass while one of them was missing.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this file
# must not re-improvise: the stale-anchor guard (an anchor that no longer
# matches is reported STALE, never a silent no-op — #1390/#1450), the no-op
# replacement refusal, and the byte-for-byte restore that never touches git
# state (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).
#
# Convention follows tools/lib/mutate_test.sh: plain bash, hand-rolled asserts,
# a `fails` counter, "ALL PASS" / "N FAILED" at the end.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as a
# PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: error-retention-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: error-retention-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS. This suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guards were verified" and "the guards could not
# be checked at all" produce byte-identical results at the gate — the exact
# shape AGENTS.md forbids, and the same shape as the tripwire blind spot this
# very fixture exists to prevent regressing.
#
# So it is a HARD FAILURE wherever the answer is load-bearing (CI, and any
# caller that sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on a
# developer's dirty worktree, where failing would only train people to delete
# the fixture. The local skip still prints, and still says what to do.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "error-retention-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/error-retention-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

PKG="./core/application/services/"
fails=0

# assert_mutation_is_red applies one mutation and requires the named test to
# FAIL under it, and to name `want_match` in its output.
#
# Both halves matter. A mutation that leaves the test green means the guard does
# not reach what it claims to cover. A mutation that goes red for the WRONG
# reason (a compile error, a different test) would otherwise read as success —
# so the expected failure text is asserted too, not just the exit status.
assert_mutation_is_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" run="$5" want_match="$6"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    go test "$PKG" -run "$run" -count=1 2>&1)"
  rc=$?

  # mutate.sh exits 0 when the mutation applied and the tree was restored,
  # WHATEVER the inner test did — the inner result is in the captured text. A
  # non-zero rc is mutate.sh itself refusing (STALE anchor, dirty tree, ...),
  # which is a fixture bug, not a finding about the guard.
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the code moved and"
    echo "      this fixture needs its anchor updated; it does NOT mean the guard is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi

  # The mutation must make the test fail...
  if ! grep -qE '^(FAIL|--- FAIL)' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi

  # ...and fail for the RIGHT reason.
  if ! grep -qF "$want_match" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want_match"
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi

  echo "ok  $label"
}

# ── 1. The reaper tripwire, via a DELEGATE ───────────────────────────────────
# Removing the guard from reapsIdleTopLevel leaves sweepStaleSnapshot still
# calling it, so only a derived delegation relation catches this.
assert_mutation_is_red \
  "reaper tripwire catches a guard removed from a delegate predicate" \
  "core/application/services/pid_manager.go" \
  $'\tif pm.retainsError(snap.state) {\n\t\treturn false\n\t}\n' \
  $'\t// mutated by tools/lib/error-retention-mutations_test.sh\n' \
  'TestEveryReaperConsultsTheErrorExemption|TestStaleSweep_KeepsARetainedErrorRow' \
  'sweepStaleSnapshot deletes session rows but nothing on its call path consults retainsError'

# ── 2. The startup zombie sweep exemption ────────────────────────────────────
assert_mutation_is_red \
  "isStartupZombie exemption is load-bearing" \
  "core/application/services/pid_manager.go" \
  $'\tif pm.retainsError(state) {\n\t\treturn false\n\t}\n\tif state.PID > 0 {' \
  $'\tif state.PID > 0 {' \
  'TestStartupSweeps_KeepARetainedErrorRow' \
  'the startup zombie sweep still deletes an errored session with a dead PID'

# ── 3. The seed-time dead-PID exemption (the SECOND deleter) ─────────────────
assert_mutation_is_red \
  "seedAlivePIDs exemption is load-bearing, independently of isStartupZombie" \
  "core/application/services/pid_manager.go" \
  $'\tif pm.retainsError(state) {\n\t\tpm.log.LogInfo(logComponentSessionDetectorSeed, state.SessionID,' \
  $'\tif false {\n\t\tpm.log.LogInfo(logComponentSessionDetectorSeed, state.SessionID,' \
  'TestStartupSweeps_KeepARetainedErrorRow' \
  'the seed-time dead-PID path deleted 1 errored session(s)'

if [[ $fails -gt 0 ]]; then
  echo "error-retention-mutations: $fails FAILED"
  exit 1
fi
echo "error-retention-mutations: ALL PASS"
