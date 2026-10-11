#!/usr/bin/env bash
# codex-held-rollout-admission-mutations_test.sh — committed mutation fixture
# for issue #2081's stale-transcript writer admission
# (core/application/services/session_detector_activity.go staleTranscriptHolder
# and core/application/services/pid_manager.go StaleTranscriptWriter).
#
# WHY THIS FILE EXISTS. #2081 admits a stale transcript at daemon start when a
# live process holds it open for writing. The admission tests in
# core/application/services/session_detector_stale_held_admission_test.go were
# seen red on the unfixed code. The guards around that admission were added by
# the change. The tests that pin mutations 1-4 and 6 were green on the unfixed
# code by construction (mutation 6's test went red there only on its log line),
# so a mutation below is their red evidence. Mutation 5's test was also seen
# red on the unfixed code; its mutation shows the adapter scope keeps it:
#
#   1. subagent guard dropped        → a held subagent rollout is admitted;
#   2. adapter scope dropped         → claude-code's discovery is asked at
#                                      admission and admits a stale sibling;
#   3. observe-consent gate dropped  → the probe runs for an adapter without
#                                      consent and admits its stale rollout;
#   4. a probe that found no writer names pid 1 → a stale rollout nobody holds
#                                      is admitted;
#   5. only codex is asked           → a stale dsh or muse session whose lock a
#                                      live process holds is not admitted;
#   6. a probe that could not run admits → a stale rollout is admitted on a
#                                      writer probe's error.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on
# tools/lib/codex-shared-pid-owner-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: codex-held-rollout-admission-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: codex-held-rollout-admission-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "codex-held-rollout-admission-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/codex-held-rollout-admission-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running this mutation)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -race -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating — it does NOT mean the"
    echo "      guard is fine."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard. A fixture that"
    echo "      cannot compile proves nothing about the behavior it is meant to exercise."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

PKG="./core/application/services/"
DET_FILE="core/application/services/session_detector_activity.go"
PM_FILE="core/application/services/pid_manager.go"
SCOPE_ANCHOR=$'\tif pm.sharedPIDOwners[adapter] == nil || pm.pidDiscovers[adapter] == nil || !pm.observeAllowed(adapter) {'

# ── 1. the subagent guard is gone ──
assert_go_test_goes_red \
  "staleTranscriptHolder admitting a held subagent rollout" \
  "$DET_FILE" \
  $'\tif ev.ParentSessionID != "" {' \
  $'\tif false {' \
  "$PKG" \
  "^TestSessionDetector_StaleHeldCodexSubagentIsNotAdmitted\$" \
  "a held subagent rollout says nothing about its parent"

# ── 2. every adapter with a PID discovery is asked, not only SharedPIDOwner ones ──
assert_go_test_goes_red \
  "StaleTranscriptWriter asking an adapter that declares no SharedPIDOwner" \
  "$PM_FILE" \
  "$SCOPE_ANCHOR" \
  $'\tif pm.pidDiscovers[adapter] == nil || !pm.observeAllowed(adapter) {' \
  "$PKG" \
  "^TestSessionDetector_StaleClaudeTranscriptIsNotProbedForAWriter\$" \
  "the cwd rescue declines a stale sibling that is not the newest"

# ── 3. the probe runs without the adapter's observe consent ──
assert_go_test_goes_red \
  "StaleTranscriptWriter probing without observe consent" \
  "$PM_FILE" \
  "$SCOPE_ANCHOR" \
  $'\tif pm.sharedPIDOwners[adapter] == nil || pm.pidDiscovers[adapter] == nil {' \
  "$PKG" \
  "^TestSessionDetector_StaleCodexRootIsNotProbedWithoutConsent\$" \
  "codex's observe consent is not granted"

# ── 4. a probe that found no writer still names one (pid 1) ──
assert_go_test_goes_red \
  "StaleTranscriptWriter naming a writer for a rollout nobody holds" \
  "$PM_FILE" \
  $'\treturn pm.discoverOwner(sessionID, adapter, cwd, transcriptPath)' \
  $'\tpid, _ := pm.discoverOwner(sessionID, adapter, cwd, transcriptPath)\n\treturn max(pid, 1), nil' \
  "$PKG" \
  "^TestSessionDetector_StaleUnheldCodexRootIsStillSkipped\$" \
  "no process holds its rollout open"

# ── 5. only codex is asked; dsh and muse, which also declare a SharedPIDOwner, are not ──
# One run per adapter, each -run anchored to a single subtest, so each has to go
# red on its own.
for adapter in dsh muse; do
  assert_go_test_goes_red \
    "StaleTranscriptWriter asking codex only ($adapter held lock)" \
    "$PM_FILE" \
    "$SCOPE_ANCHOR" \
    $'\tif adapter != "codex" || pm.sharedPIDOwners[adapter] == nil || pm.pidDiscovers[adapter] == nil || !pm.observeAllowed(adapter) {' \
    "$PKG" \
    "^TestSessionDetector_StaleHeldLockSessionsAreAdmitted\$/^${adapter}\$" \
    "was not admitted"
done

# ── 6. a probe that could not run admits the transcript ──
assert_go_test_goes_red \
  "staleTranscriptHolder admitting on a writer probe that could not run" \
  "$DET_FILE" \
  $'— trying the cwd rescue", err))\n\t\treturn 0' \
  $'— trying the cwd rescue", err))\n\t\treturn 1' \
  "$PKG" \
  "^TestSessionDetector_StaleCodexRootWhoseProbeFailedIsSkippedAndLogged\$" \
  "a probe that could not run is no evidence of a writer"

if [[ $fails -gt 0 ]]; then
  echo "codex-held-rollout-admission-mutations: $fails FAILED"
  exit 1
fi
echo "codex-held-rollout-admission-mutations: ALL PASS"
