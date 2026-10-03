#!/usr/bin/env bash
# execution-confidence-mutations_test.sh — the committed mutation fixtures for
# #737's execution-confidence score (Phase 1: scorer, accumulator, ledger,
# converter, merge, golden visibility).
#
# WHY THIS FILE EXISTS. The score is a derived figure and each rule behind it is
# something #737 ADDS — the decay that lets a recovered session go high, the
# zero weight on asking phrases (they already drive `waiting`), the category
# weights, the code-stripping, the per-message cap, the ledger restore, the
# merge-allowlist entry, the shared-converter assignment and the replay-golden
# surfacing. None has a "before the fix" to run red, so per AGENTS.md and
# docs/testing-philosophy.md each earns its place by being seen to fail when
# the thing it protects is broken. Every block below applies ONE mutation via
# tools/mutate.sh and requires the named check to go red, for the named reason.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards and the byte-for-byte restore that never
# touches git state (worktrees share the parent repo's .git dir, so
# `git checkout --` / `git restore` / `git reset --hard` are banned repo-wide).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: execution-confidence-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: execution-confidence-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS: this suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guard was verified" and "the guard could not
# be checked at all" produce byte-identical results at the gate. So it is a
# HARD FAILURE wherever the answer is load-bearing (CI, and any caller that
# sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on a
# developer's dirty worktree, where failing would only train people to delete
# the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "execution-confidence-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/execution-confidence-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
#
# Applies one mutation and requires the named Go test to FAIL under it, AND
# to fail for the right reason. Both halves matter: a mutation that leaves
# the test green means the contract does not reach what it claims to
# protect, and a mutation that goes red because the package no longer
# COMPILES would otherwise read as success.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -count=1 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the contract does not reach"
    echo "      what it claims to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the contract. A fixture that"
    echo "      cannot compile proves nothing about the assertion it is meant to exercise."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

SCORER=core/domain/session/execution_confidence.go
SESSION_PKG=./core/domain/session/...

# ── Decay removed: early doubt would haunt a recovered session forever ─────
assert_go_test_goes_red \
  "a half-life of 1000 messages (no effective decay) reddens the recovery test" \
  "$SCORER" \
  'const executionConfidenceHalfLifeMessages = 5.0' \
  'const executionConfidenceHalfLifeMessages = 1000.0' \
  "$SESSION_PKG" \
  'TestExecutionConfidence_DecayLetsARecoveredSessionGoHigh' \
  '5 hedge-heavy then 15 decisive messages scored'

assert_go_test_goes_red \
  "the same mutation reddens the tailer's end-to-end recovery test" \
  "$SCORER" \
  'const executionConfidenceHalfLifeMessages = 5.0' \
  'const executionConfidenceHalfLifeMessages = 1000.0' \
  "./core/pkg/tailer/..." \
  'TestTailer_ExecutionConfidenceRecoversAfterDecisiveMessages' \
  'decay must let a recovered session go high'

# ── Asking phrases weighted: a legitimate question would lower confidence ──
assert_go_test_goes_red \
  "a non-zero weight on asking phrases reddens the asking-scores-zero test" \
  "$SCORER" \
  $'\tHedgeAsking:              0,' \
  $'\tHedgeAsking:              1,' \
  "$SESSION_PKG" \
  'TestExecutionConfidence_AskingPhrasesScoreZero' \
  'want a zero-weight sample'

# ── A category's weight zeroed: that kind of doubt would stop counting ─────
assert_go_test_goes_red \
  "zeroing the explicit-uncertainty weight reddens the per-category test" \
  "$SCORER" \
  $'\tHedgeExplicitUncertainty: 3,' \
  $'\tHedgeExplicitUncertainty: 0,' \
  "$SESSION_PKG" \
  'TestExecutionConfidence_EachScoredCategoryCostsPoints' \
  'a scored category must cost points'

# ── Code no longer stripped: quoted output would read as the agent's voice ─
assert_go_test_goes_red \
  "not stripping fenced code reddens the code-is-ignored test" \
  "$SCORER" \
  $'\ttext = fencedCodeBlock.ReplaceAllString(text, " ")\n' \
  $'\n' \
  "$SESSION_PKG" \
  'TestExecutionConfidence_HedgesInCodeAreIgnored' \
  'hedges inside code counted'

# ── Per-message cap removed: one loud message would outweigh many quiet ones
assert_go_test_goes_red \
  "removing the per-message cap reddens the cap test" \
  "$SCORER" \
  'return &HedgeSample{Weight: math.Min(weight, limit), Words: words}' \
  'return &HedgeSample{Weight: weight + 0*limit, Words: words}' \
  "$SESSION_PKG" \
  'TestExecutionConfidence_OneMessageIsCapped' \
  'want it capped at'

# ── Merge allowlist entry dropped: the live path would lose the score ──────
assert_go_test_goes_red \
  "dropping ExecutionConfidence from newMergedMetrics reddens the merge test" \
  "core/domain/session/metrics.go" \
  $'\t\tExecutionConfidence:        newM.ExecutionConfidence,\n' \
  $'\n' \
  "$SESSION_PKG" \
  'TestMergeMetrics_CarriesExecutionConfidence' \
  "missing from newMergedMetrics' allowlist"

# ── Ledger restore dropped: a daemon restart would reset the score ─────────
assert_go_test_goes_red \
  "not restoring the accumulator from the ledger reddens the round-trip test" \
  "core/pkg/tailer/tailer_ledger.go" \
  $'\t\tt.executionConfidence = *s.ExecutionConfidence' \
  $'\t\t_ = s.ExecutionConfidence' \
  "./core/pkg/tailer/..." \
  'TestLedger_PersistsExecutionConfidence' \
  'tailer metrics carry no execution-confidence score'

# ── claudecode measures the display tail instead of the full text ──────────
assert_go_test_goes_red \
  "measuring claudecode's truncated tail reddens the full-text test" \
  "core/adapters/inbound/agents/claudecode/parser.go" \
  $'\t\tev.Hedge = session.MeasureHedging(full)' \
  $'\t\tev.Hedge = session.MeasureHedging(tailer.TruncateAssistantText(full))' \
  "./core/adapters/inbound/agents/claudecode/..." \
  'TestParser_Hedge_MeasuredFromFullText' \
  'want a weighted sample from the full text'

# ── pi keeps only the last text block's sample ─────────────────────────────
assert_go_test_goes_red \
  "replacing pi's per-block sum with last-block-wins reddens the multi-block test" \
  "core/adapters/inbound/agents/pi/parser.go" \
  $'\t\tev.Hedge = ev.Hedge.Plus(session.MeasureHedging(text))' \
  $'\t\tev.Hedge = session.MeasureHedging(text)' \
  "./core/adapters/inbound/agents/pi/..." \
  'TestParser_Hedge_SumsEveryTextBlock' \
  "want the first block's hedge counted"

# ── Rotation no longer resets the accumulator (#737 review, finding 1) ─────
assert_go_test_goes_red \
  "not resetting the accumulator on rotation reddens the rewrite test" \
  "core/pkg/tailer/rotation_detect.go" \
  $'\tt.executionConfidence = session.ExecutionConfidenceAccumulator{}\n' \
  $'\t_ = session.ExecutionConfidenceAccumulator{}\n' \
  "./core/pkg/tailer/..." \
  'TestTailer_ExecutionConfidenceResetsOnRewrite' \
  'the rotation reset must clear it'

# ── Keyed re-emissions observed every time (#737 review, finding 2) ────────
assert_go_test_goes_red \
  "dropping the keyed-dedupe skip reddens the re-emission test" \
  "core/pkg/tailer/tailer.go" \
  $'ok && prev == *parsed.Hedge {\n\t\t\treturn\n' \
  $'ok && prev == *parsed.Hedge {\n\t\t\t_ = prev\n' \
  "./core/pkg/tailer/..." \
  'TestTailer_ExecutionConfidenceDedupesKeyedReemission' \
  'Messages = 3, want 1'

assert_go_test_goes_red \
  "gemini-cli without its message-id HedgeKey reddens its keying test" \
  "core/adapters/inbound/agents/geminicli/parser.go" \
  $'\tev.HedgeKey, _ = raw["id"].(string)\n' \
  $'\n' \
  "./core/adapters/inbound/agents/geminicli/..." \
  'TestParser_Hedge_KeyedByMessageID' \
  'want the message id g9'

assert_go_test_goes_red \
  "junie's thought block without its stepId HedgeKey reddens its keying test" \
  "core/adapters/inbound/agents/junie/parser.go" \
  $'\t\tev.HedgeKey = str(agentEvent, "stepId")\n' \
  $'\n' \
  "./core/adapters/inbound/agents/junie/..." \
  'TestParser_Hedge_KeyedByStepID' \
  'thought: HedgeKey = ""'

assert_go_test_goes_red \
  "antigravity without its step HedgeKey reddens its keying test" \
  "core/adapters/inbound/agents/antigravity/parser.go" \
  $'\t\tev.HedgeKey = "step-" + strconv.FormatInt(intFromAny(raw["step_index"]), 10)\n' \
  $'\n' \
  "./core/adapters/inbound/agents/antigravity/..." \
  'TestParser_Hedge_KeyedByStepIndex' \
  'want step-3'

# ── Store folds and the muse merge drop the samples ────────────────────────
assert_go_test_goes_red \
  "the opencode store fold not observing samples reddens its fold test" \
  "core/adapters/inbound/agents/opencode/metrics.go" \
  $'\t\tconfidence.Observe(ev.Hedge)\n' \
  $'\n' \
  "./core/adapters/inbound/agents/opencode/..." \
  'TestComputeMetrics_ExecutionConfidence' \
  'want a low score with a tooltip'

assert_go_test_goes_red \
  "the hermes store fold not observing samples reddens its fold test" \
  "core/adapters/inbound/agents/hermes/metrics.go" \
  $'\tf.confidence.Observe(ev.Hedge)\n' \
  $'\n' \
  "./core/adapters/inbound/agents/hermes/..." \
  'TestComputeMetrics_ExecutionConfidence' \
  'want a low score with a tooltip'

assert_go_test_goes_red \
  "muse's child merge keeping only the last sample reddens the merge test" \
  "core/adapters/inbound/agents/muse/parser.go" \
  $'\tmerged.Hedge = merged.Hedge.Plus(ev.Hedge)' \
  $'\tif ev.Hedge != nil {\n\t\tmerged.Hedge = ev.Hedge\n\t}' \
  "./core/adapters/inbound/agents/muse/..." \
  'TestMergeChildEvents_SumsHedgeSamples' \
  'want {4 30}'

# ── claudecode scores the AskUserQuestion tool input as prose ──────────────
assert_go_test_goes_red \
  "scoring claudecode's AskUserQuestion fallback reddens the exclusion test" \
  "core/adapters/inbound/agents/claudecode/parser.go" \
  $'\t\t\tfull = askUserQuestion\n' \
  $'\t\t\tfull = askUserQuestion\n\t\t\tev.Hedge = session.MeasureHedging(full)\n' \
  "./core/adapters/inbound/agents/claudecode/..." \
  'TestParser_Hedge_IgnoresAskUserQuestionFallback' \
  'AskUserQuestion input is not assistant prose'

# ── Lexicon inflection and memo turnover ───────────────────────────────────
assert_go_test_goes_red \
  "dropping the uncertainty inflection reddens the inflection test" \
  "$SCORER" \
  '`uncertaint(?:y|ies)`, ' \
  '' \
  "$SESSION_PKG" \
  'TestExecutionConfidence_Inflections' \
  'There is some uncertainty about the API.'

assert_go_test_goes_red \
  "a memo that drops its working set at turnover reddens the turnover test" \
  "$SCORER" \
  $'\t\tc.prev, c.cur = c.cur, make(map[uint64]HedgeSample)' \
  $'\t\tc.prev, c.cur = nil, make(map[uint64]HedgeSample)' \
  "$SESSION_PKG" \
  'TestHedgeSampleMemo_WorkingSetSurvivesTurnover' \
  'working-set entries missed after one turnover'

# ── The shared converter stops deriving the score ──────────────────────────
CONVERTER_ANCHOR=$'\tsession.ApplyExecutionConfidence(result, m.ExecutionConfidence)'
CONVERTER_MUTANT=$'\t_ = m.ExecutionConfidence'
assert_go_test_goes_red \
  "removing the converter assignment reddens the converter test" \
  "core/application/replayengine/metrics.go" \
  "$CONVERTER_ANCHOR" "$CONVERTER_MUTANT" \
  "./core/application/replayengine/..." \
  'TestConvert_ExecutionConfidence' \
  'ExecutionConfidence = <nil>'

# ...and drifts the replay goldens that surface it (the triage's named
# instrument: the golden summary is sourced through that same converter).
assert_go_test_goes_red \
  "removing the converter assignment drifts the replay goldens" \
  "core/application/replayengine/metrics.go" \
  "$CONVERTER_ANCHOR" "$CONVERTER_MUTANT" \
  "./tools/onboarding-factory/cmd/replay/..." \
  '.' \
  'UPDATE_REPLAY_GOLDENS=1'

if [[ $fails -gt 0 ]]; then
  echo "execution-confidence-mutations: $fails FAILED"
  exit 1
fi
echo "execution-confidence-mutations: ALL PASS"
