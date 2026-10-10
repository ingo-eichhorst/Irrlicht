#!/usr/bin/env bash
# codex-released-root-mutations_test.sh — committed mutation fixture for issue
# #2080: a codex root whose still-live PID (the shared managed app-server
# daemon) released its rollout ends, and a paginated thread's root follows its
# rollout to the newer segment first.
#
# WHY THIS FILE EXISTS. The red-first tests for #2080 (the lone and newest
# released roots, and the segment rollover) were seen red before the fix. What
# they cannot show is that each guard the fix ADDS reaches what it protects:
# the locks for those guards pass on the unfixed code by construction, and the
# codex probe and the rollover predicate are new functions. Each mutation below
# breaks one guard and requires the named test to go red with the named
# message:
#
#   1. codex.ReleasedPID treats a probe error as "released" → the inconclusive
#      lock goes red: a root whose probe could not answer is ended.
#   2. codex.ReleasedPID loses its pid/path guard → TestReleasedPID's zero-pid
#      row goes red: HoldsForWriting's (false, nil) for a pid that names
#      nothing reads as "released".
#   3. one "released" answer is enough → the streak lock goes red on sweep 1.
#   4. a non-released answer carries the streak instead of resetting it → the
#      streak lock goes red on sweep 3 (released, held, released).
#   5. the release probe runs without observe consent → the scope lock's
#      consent row goes red.
#   6. the release probe runs for a subagent and a transcript-less pre-session
#      → the scope lock's subagent row goes red.
#   7./8. either call site of followRolledTranscript is dropped → the rollover
#      test's subtest for that event type goes red, each on its own run.
#   9. the rollover follows any adapter, not only one declaring ReleasedPID →
#      the muse lock goes red: muse's shadow file replaces the transcript that
#      carries the session.
#  10. the rollover follows an OLDER file → TestTranscriptRolledOver's "older
#      candidate" row goes red: a late event from an old segment would move the
#      session back.
#  11. no last-moment discovery check before ending a root → the non-holder
#      test goes red: a root bound to a live pid that never held its rollout
#      is ended while another process writes it.
#  12. a discovery that could not run reads as "no owner" → that row of the
#      discovery test goes red.
#  13. codex's filename fallback keeps a segment file's suffix → the segment
#      test's header-less row goes red: the segment id, not the thread id.
#  14. a root found misbound is asked again → the discovery test's "another
#      live owner" row goes red: a whole-table discovery scan every second
#      sweep for as long as the misbinding lasts.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on codex-shared-pid-owner-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: codex-released-root-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: codex-released-root-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "codex-released-root-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/codex-released-root-mutations_test.sh" >&2
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

SERVICES="./core/application/services/"
CODEX_FILE="core/adapters/inbound/agents/codex/pid.go"
PM_FILE="core/application/services/pid_manager.go"
SD_FILE="core/application/services/session_detector_activity.go"

# The inconclusive lock skips as root (a mode-000 directory stays searchable),
# so mutation 1 cannot go red there. Say so by name rather than report a pass.
if [[ "$(id -u)" == "0" ]]; then
  echo "NOT RUN: mutation 1 (codex.ReleasedPID treating a probe error as released) — running"
  echo "         as root, where the inconclusive lock it needs skips"
else
  # ── 1. a probe error reads as "released" ──
  assert_go_test_goes_red \
    "codex.ReleasedPID treating a probe error as released" \
    "$CODEX_FILE" \
    $'\treturn err == nil && !held' \
    $'\treturn err != nil || !held' \
    "$SERVICES" \
    '^TestCheckPIDLiveness_InconclusiveReleaseProbeNeverEndsCodexRoot$' \
    "a codex root whose release probe could not answer was ended by the liveness sweep"
fi

# ── 2. no pid/path guard in front of the probe ──
assert_go_test_goes_red \
  "codex.ReleasedPID without its pid/path guard" \
  "$CODEX_FILE" \
  $'\tif pid <= 0 || transcriptPath == "" {\n\t\treturn false\n\t}\n\theld, err := processlifecycle.HoldsForWriting(pid, transcriptPath)\n\treturn err == nil && !held' \
  $'\theld, err := processlifecycle.HoldsForWriting(pid, transcriptPath)\n\treturn err == nil && !held' \
  "./core/adapters/inbound/agents/codex/" \
  '^TestReleasedPID$/^zero_pid$' \
  '", 0) = true, want false'

# ── 3. one "released" answer ends a root ──
assert_go_test_goes_red \
  "release streak threshold of 1" \
  "$PM_FILE" \
  'const releaseStreakThreshold = 2' \
  'const releaseStreakThreshold = 1' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ReleaseStreakResetsOnAnyOtherAnswer$' \
  "sweep 1 (probe answered released=true) ended the root"

# ── 4. a non-released answer keeps the streak ──
assert_go_test_goes_red \
  "release streak carried over a non-released answer" \
  "$PM_FILE" \
  $'\t\tif !pm.releasedTranscript(snap) {\n\t\t\tcontinue\n\t\t}' \
  $'\t\tif !pm.releasedTranscript(snap) {\n\t\t\tstreaks[key] = pm.releaseStreaks[key]\n\t\t\tcontinue\n\t\t}' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ReleaseStreakResetsOnAnyOtherAnswer$' \
  "sweep 3 (probe answered released=true) ended the root"

# ── 5. the probe runs without observe consent ──
assert_go_test_goes_red \
  "release probe ignoring the consent gate" \
  "$PM_FILE" \
  $'\tif !pm.observeAllowed(snap.adapter) {' \
  $'\tif false {' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ReleaseProbeScope$/^observe_consent_withheld$' \
  "case observe consent withheld: the release probe ran"

# ── 6. the probe runs for a subagent ──
assert_go_test_goes_red \
  "release probe asking about a subagent" \
  "$PM_FILE" \
  $'\tif snap.parentSessionID != "" || snap.transcriptPath == "" {' \
  $'\tif snap.transcriptPath == "" {' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ReleaseProbeScope$/^subagent$' \
  "case subagent: the release probe ran"

# ── 7./8. each call site of the rollover follow ──
assert_go_test_goes_red \
  "rollover not followed on a create event" \
  "$SD_FILE" \
  $'\t\tif d.followRolledTranscript(existing, ev) {\n\t\t\tchanged = true\n\t\t}\n' \
  '' \
  "$SERVICES" \
  '^TestSessionDetector_CodexRolloverFollowsNewerSegment$/^create_event$' \
  "session 01a1181a-0349-7712-8ec0-c750ef098f2e points at"

assert_go_test_goes_red \
  "rollover not followed on an activity event" \
  "$SD_FILE" \
  $'\td.followRolledTranscript(state, ev)\n' \
  '' \
  "$SERVICES" \
  '^TestSessionDetector_CodexRolloverFollowsNewerSegment$/^activity_event$' \
  "session 01a1181a-0349-7712-8ec0-c750ef098f2e points at"

# ── 9. the rollover follows every adapter ──
assert_go_test_goes_red \
  "rollover followed for an adapter without a release probe" \
  "$SD_FILE" \
  $'\tif !d.pidMgr.probesRelease(state.Adapter) || !transcriptRolledOver(' \
  $'\tif !transcriptRolledOver(' \
  "$SERVICES" \
  '^TestSessionDetector_NonOptInSessionKeepsItsTranscriptPath$' \
  "muse session points at"

# ── 10. the rollover follows an older file ──
assert_go_test_goes_red \
  "rollover following an older transcript" \
  "$SD_FILE" \
  $'\treturn next.ModTime().After(prev.ModTime())' \
  $'\treturn next != nil && prev != nil' \
  "$SERVICES" \
  '^TestTranscriptRolledOver$/^older_candidate$' \
  "transcriptRolledOver(rollout-current.jsonl, rollout-older.jsonl) = true, want false"

# ── 11. no discovery check before a root ends ──
assert_go_test_goes_red \
  "a released root ended without asking discovery for another owner" \
  "$PM_FILE" \
  $'\tif err != nil || owner > 0 {' \
  $'\tif false {' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_RootBoundToANonHolderIsNotEnded$' \
  "was ended although pid"

# ── 12. a discovery error reads as "no owner" ──
assert_go_test_goes_red \
  "a discovery error read as no owner" \
  "$PM_FILE" \
  $'\tif err != nil || owner > 0 {' \
  $'\tif owner > 0 {' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ReleasedRootEndsOnlyWhenDiscoveryFindsNoOwner$/^discovery_could_not_run$' \
  "case discovery could not run: root ended = true"

# ── 13. a segment file's filename fallback names the segment ──
assert_go_test_goes_red \
  "codex filename fallback keeping a segment suffix" \
  "core/adapters/inbound/agents/codex/session_meta.go" \
  $'\tif i := strings.IndexByte(base, \'_\'); i >= 0 {\n\t\tbase = base[:i]\n\t}\n' \
  '' \
  "./core/adapters/inbound/agents/codex/" \
  '^TestSessionIDFromPath_SegmentFileMapsToItsThread$' \
  "want the thread id"

# ── 14. a misbound root is asked again ──
assert_go_test_goes_red \
  "a root found misbound asked again on later sweeps" \
  "$PM_FILE" \
  $'\t\tif pm.misboundRoots[key] {\n\t\t\tmisbound[key] = true\n\t\t\tcontinue\n\t\t}\n' \
  '' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ReleasedRootEndsOnlyWhenDiscoveryFindsNoOwner$/^another_live_owner$' \
  "case another live owner: four sweeps ran the release probe 4 times and discovery 2 times, want 2 and 1"

if [[ $fails -gt 0 ]]; then
  echo "codex-released-root-mutations: $fails FAILED"
  exit 1
fi
echo "codex-released-root-mutations: ALL PASS"
