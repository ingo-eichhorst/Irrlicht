#!/usr/bin/env bash
# hook-turn-tail-mutations_test.sh — committed mutation fixture for issue #2109
# phase P5-0: the tail of a turn a hook already ended does not count as new
# activity, so the detector no longer forces it ready → working → ready.
#
# WHY THIS FILE EXISTS. The defect tests were seen red before the fix (the PR
# body carries the runs). What they cannot show is that each guard the change
# ADDS reaches what it protects: the four conditions of
# services.IsHookTurnTail, the marker's lifetime in the detector and in the
# sidecar replay, and the UserTurns counter on every path that produces or
# carries it. Their locks pass before and after the change by construction,
# so a mutation is the only red they can have. Each mutation below breaks one
# guard and requires the named test to go red with the named message:
#
#   1. IsHookTurnTail without condition 1 (a hook's own pass) → a PreToolUse
#      after a hook stop no longer forces.
#   2. HookTurnEndOf marking every ready, not only a hook's turn-done verdict
#      (condition 2, at the place it is decided) → a transcript-decided ready
#      exempts its next done pass, in the detector and (2b) in the sidecar.
#   3. IsHookTurnTail without condition 3 (no new user turn) → a collapsed new
#      turn after a hook stop (claudecode 5-6's shape) loses its bounce.
#   4. IsHookTurnTail without the zero-count fallback → a source that counts no
#      user turns (hermes) skips the force.
#   5. IsHookTurnTail without the BackgroundProcessCount check → a done pass
#      with a live background process stays ready, and the #937 probe, which
#      runs only for working sessions, never sees it.
#   6. forgetHookTurnEnd forgetting nothing → the marker outlives the ready it
#      describes, and a later transcript-only ready inherits it.
#   7. the sidecar without the mirror → the eight regenerated goldens differ
#      (TestFixtureReplayByteIdentity, the check tools/replay-fixtures.sh's
#      byte-identity half runs), and (7b) the sidecar's defect test.
#   8. the tailer counting on a bare ClearToolNames → a tool result counts as a
#      user turn.
#   9. the tailer keeping its count across a rewrite → surviving prompts are
#      counted twice.
#  10. MetricsConverter.Convert not copying UserTurns → every tailer-path
#      agent reads 0 and the rule never applies.
#  11. MergeMetrics not taking the fresh UserTurns → the count freezes.
#  12. the opencode store fold counting every row → assistant and idle rows
#      count as user turns.
#  13. the sidecar keeping the marker across a process teardown → the next
#      lifetime's first done pass is read as the old turn's tail.
#  14. forgetSessionScopedState not dropping the marker → a reaped session
#      leaves it behind, and a session re-created under the same ID (the
#      --continue branch) would read its first done pass as the old tail.
#
# Mutation 2 replaces the triage record's "drop condition 2 → goes red on
# TestSessionDetector_ForceReadyToWorking_LogsTransition": that test starts a
# session in ready with no transition, so no marker exists and dropping the
# check at the read site changes nothing it can see. The tier check lives where
# the marker is set, so that is where it is broken.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on opencode-v2-hooks-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: hook-turn-tail-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: hook-turn-tail-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "hook-turn-tail-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/hook-turn-tail-mutations_test.sh" >&2
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

RULE="core/application/services/hook_turn_tail.go"
HELPERS="core/application/services/session_detector_helpers.go"
SIDECAR="tools/onboarding-factory/cmd/replay/replay_sidecar.go"
TAILER="core/pkg/tailer/tailer.go"
ROTATION="core/pkg/tailer/rotation_detect.go"
CONVERTER="core/application/replayengine/metrics.go"
MERGE="core/domain/session/metrics.go"
FOLD="core/adapters/inbound/agents/opencode/metrics.go"
SERVICES="./core/application/services/"
REPLAY="./tools/onboarding-factory/cmd/replay/"
TAILER_PKG="./core/pkg/tailer/"
REPLAYENGINE="./core/application/replayengine/"
SESSION="./core/domain/session/"
OPENCODE="./core/adapters/inbound/agents/opencode/"

# ── 1. condition 1: a hook's own pass keeps the force ──
assert_go_test_goes_red \
  "IsHookTurnTail exempting a hook's own pass" \
  "$RULE" \
  $'\tif synthetic {\n' \
  $'\tif false && synthetic {\n' \
  "$SERVICES" \
  '^TestSessionDetector_HookTurnTail_SyntheticHookStillForces$' \
  "a PreToolUse hook after a hook stop forced 0 time(s), want 1"

# ── 2. condition 2, where it is decided: only a hook's turn-done marks ──
TIER_ANCHOR='if v.Rule != ruleAgentDone || v.Tier != session.TierOf(session.SignalTurnDone) {'
TIER_MUTANT='if false {'
assert_go_test_goes_red \
  "HookTurnEndOf marking a transcript-decided ready" \
  "$RULE" "$TIER_ANCHOR" "$TIER_MUTANT" \
  "$SERVICES" \
  '^TestSessionDetector_HookTurnTail_TranscriptReadyKeepsTheForce$' \
  "after a transcript-decided ready the pass forced 0 time(s), want 1"

# ── 2b. the same mutation, through the sidecar ──
assert_go_test_goes_red \
  "HookTurnEndOf marking a transcript-decided ready (sidecar)" \
  "$RULE" "$TIER_ANCHOR" "$TIER_MUTANT" \
  "$REPLAY" \
  '^TestSidecar_HookTurnTail_MarksByVerdictNotCause$' \
  "the done pass forced 0 time(s), want 1"

# ── 3. condition 3: a new user turn is new activity ──
assert_go_test_goes_red \
  "IsHookTurnTail ignoring a new user turn" \
  "$RULE" \
  'if m.UserTurns != end.UserTurns {' \
  'if false && m.UserTurns != end.UserTurns {' \
  "$SERVICES" \
  '^TestSessionDetector_HookTurnTail_NewUserTurnStillBounces$' \
  "a collapsed new user turn forced 0 time(s), want 1"

# ── 4. the zero-count fallback ──
assert_go_test_goes_red \
  "IsHookTurnTail without the zero-count fallback" \
  "$RULE" \
  'if end.UserTurns <= 0 {' \
  'if false && end.UserTurns <= 0 {' \
  "$SERVICES" \
  '^TestSessionDetector_HookTurnTail_ZeroCountForcesAsToday$' \
  "with UserTurns 0 the tail forced 0 time(s), want 1"

# ── 5. condition 4's background-process half (#937) ──
assert_go_test_goes_red \
  "IsHookTurnTail ignoring a background process" \
  "$RULE" \
  'return m.IsAgentDone() && m.BackgroundProcessCount == 0' \
  'return m.IsAgentDone()' \
  "$SERVICES" \
  '^TestSessionDetector_HookTurnTail_LiveBackgroundProcessStaysWorking$' \
  'with a live background process: state = "ready", want "working"'

# ── 6. the marker ends with the ready it describes ──
assert_go_test_goes_red \
  "forgetHookTurnEnd forgetting nothing" \
  "$RULE" \
  $'\tdelete(d.hookTurnEnds, sessionID)\n' \
  $'\t_ = sessionID\n' \
  "$SERVICES" \
  '^TestSessionDetector_HookTurnTail_StaleMarkerForces$' \
  "after a transcript-only ready the pass forced 0 time(s), want 1"

# ── 7. the sidecar mirror ──
MIRROR_ANCHOR='return !services.IsHookTurnTail(r.hookTurnEnd, ctx.cause == causeHook, domainMetrics)'
assert_go_test_goes_red \
  "the sidecar without the mirror (goldens)" \
  "$SIDECAR" "$MIRROR_ANCHOR" 'return true' \
  "$REPLAY" \
  '^TestFixtureReplayByteIdentity$' \
  "replay output differs from golden"

# ── 7b. the sidecar mirror, its own defect test ──
assert_go_test_goes_red \
  "the sidecar without the mirror (unit)" \
  "$SIDECAR" "$MIRROR_ANCHOR" 'return true' \
  "$REPLAY" \
  '^TestSidecar_HookTurnTail_DoesNotForce$' \
  "the tail of a hook-ended turn emitted 2 transition(s), want 0"

# ── 8. the tailer's count uses StartsNewUserTurn, not ClearToolNames ──
assert_go_test_goes_red \
  "the tailer counting on a bare ClearToolNames" \
  "$TAILER" \
  $'if parsed.StartsNewUserTurn() {\n\t\t// Counted here, beside the resets below, so it uses the same\n\t\t// definition of a user turn: a tool result does not count (#2109).\n\t\tt.userTurns++\n' \
  $'if parsed.ClearToolNames {\n\t\tt.userTurns++\n\t}\n\tif parsed.StartsNewUserTurn() {\n' \
  "$TAILER_PKG" \
  '^TestTailer_UserTurnsCountsGenuineUserTurns$' \
  "UserTurns = 3, want 2"

# ── 9. the tailer restarts its count on a rewrite ──
assert_go_test_goes_red \
  "resetAccumulatorsForRotation keeping the count" \
  "$ROTATION" \
  $'\tt.userTurns = 0\n' \
  $'\t_ = t.userTurns\n' \
  "$TAILER_PKG" \
  '^TestTailer_UserTurnsRestartsOnRewrite$' \
  "after a rewrite to one prompt: UserTurns = 3, want 1"

# ── 10. the tailer → domain conversion carries the count ──
assert_go_test_goes_red \
  "MetricsConverter.Convert dropping UserTurns" \
  "$CONVERTER" \
  $'\t\tUserTurns:                         m.UserTurns,\n' \
  '' \
  "$REPLAYENGINE" \
  '^TestTailerToDomain_CopiesUserTurns$' \
  "UserTurns = 0, want 3"

# ── 11. MergeMetrics takes the fresh count ──
assert_go_test_goes_red \
  "newMergedMetrics dropping UserTurns" \
  "$MERGE" \
  $'\t\tUserTurns:                   newM.UserTurns,\n' \
  '' \
  "$SESSION" \
  '^TestMergeMetrics_UserTurns$' \
  "a fresh count: UserTurns = 3, want 4"

# ── 12. the opencode fold counts user rows only ──
assert_go_test_goes_red \
  "sessionFold.observe counting every row" \
  "$FOLD" \
  $'\tif ev.StartsNewUserTurn() {\n\t\tm.UserTurns++\n' \
  $'\tif true {\n\t\tm.UserTurns++\n' \
  "$OPENCODE" \
  '^TestComputeMetrics_V2CountsUserTurns$' \
  "after one user row, three assistant rows and an idle row, want 1"

# ── 13. the sidecar drops the marker at a process teardown ──
assert_go_test_goes_red \
  "applyProcessTeardown keeping the marker" \
  "$SIDECAR" \
  $'\tr.hookTurnEnd = nil\n' \
  $'\t_ = r.hookTurnEnd\n' \
  "$REPLAY" \
  '^TestSidecar_HookTurnTail_TeardownDropsTheMarker$' \
  "the next lifetime's first done pass forced 0 time(s), want 1"

# ── 14. teardown drops the detector's marker ──
assert_go_test_goes_red \
  "forgetSessionScopedState keeping the marker" \
  "$HELPERS" \
  $'\t// otherwise skip its first force.\n\td.forgetHookTurnEnd(sessionID)\n' \
  $'\t// otherwise skip its first force.\n\t_ = sessionID\n' \
  "$SERVICES" \
  '^TestSessionDetector_ReapingASessionEvictsItsDwellAndHolds$' \
  "a reaped session must not leave a hook-turn marker behind"

if [[ $fails -gt 0 ]]; then
  echo "hook-turn-tail-mutations: $fails FAILED"
  exit 1
fi
echo "hook-turn-tail-mutations: ALL PASS"
