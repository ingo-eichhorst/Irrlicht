#!/usr/bin/env bash
# herdr-usage-mutations_test.sh — committed mutation fixtures for issue #2061.
#
# #2061 adds cost and token provenance to the daemon (Phase A) and the
# `irrlicht-ls --herdr-usage` mode that turns it into Factory's schema-1 usage
# document (Phase B). Everything it adds is a guard or a derived value with no
# "before the fix" to run red, so each mutation below removes what one piece
# protects and requires a named test to go red with a named message.
#
# Most of them target TestHerdrUsage_PipelineKeepsUnknownsNull, which runs the
# whole path — tailer, tailer→domain conversion, coverage stamp, MergeMetrics,
# served JSON, formatter — so a provenance field dropped at ANY stage of it
# turns a null back into a zero there. The formatter goldens start from a JSON
# fixture and could not see that.
#
# tools/mutate.sh owns the mechanics (exact-once anchor, byte-for-byte
# restore) and this file must not re-improvise them.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
NAME=herdr-usage-mutations

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "FAIL: $NAME — $1 not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: $NAME — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "$NAME: CANNOT RUN — the worktree is dirty, and mutate.sh needs a clean tree" >&2
  echo "  for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/$NAME""_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass)" >&2
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
    bash -c "go test $pkg -run '$run' -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A stale or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating — not that the guard is fine."
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
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard."
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
  # The red line itself, so a run's log shows what failed and not only that
  # something did.
  grep -F -- "$want" <<<"$out" | head -1 | sed 's/^[[:space:]]*/      red: /'
  return 0
}

LS_PKG=./core/cmd/irrlicht-ls/
PIPELINE=TestHerdrUsage_PipelineKeepsUnknownsNull

# ── Phase A: the unpriced marking ───────────────────────────────────────────
# Without it an unpriced model's 0 is summed into the cost, and the document
# shows the priced part as the whole cost.
assert_go_test_goes_red \
  "not marking an unpriced model prints its partial cost instead of null" \
  "core/pkg/tailer/tailer_metrics.go" \
  $'\tp.unpriced[model] = true\n' \
  $'\t_ = model\n' \
  "$LS_PKG" "$PIPELINE" \
  "want null: local-unpriced's tokens were never priced"

# ── Phase A: the provider-cost early return's record ────────────────────────
# applyContribution drops a provider-cost turn's tokens; without the record the
# document prints the dropped tokens as counted zeros.
assert_go_test_goes_red \
  "not recording a provider-cost turn prints its dropped tokens as 0" \
  "core/pkg/tailer/tailer_metrics.go" \
  $'\t\tt.providerCostSeen = true\n\t\treturn\n' \
  $'\t\treturn\n' \
  "$LS_PKG" "$PIPELINE" \
  "the turn's tokens were dropped for its provider cost"

# ── Phase A: the tailer→domain conversion ───────────────────────────────────
assert_go_test_goes_red \
  "MetricsConverter.Convert dropping UnpricedModels" \
  "core/application/replayengine/metrics.go" \
  $'\t\tUnpricedModels:                    copyStrings(m.UnpricedModels),\n' \
  $'\n' \
  "$LS_PKG" "$PIPELINE" \
  "want null: local-unpriced's tokens were never priced"

# ── Phase A: the live merge allowlist ───────────────────────────────────────
assert_go_test_goes_red \
  "newMergedMetrics dropping TokensUnobserved" \
  "core/domain/session/metrics.go" \
  $'\t\tTokensUnobserved: newM.TokensUnobserved,\n' \
  $'\n' \
  "$LS_PKG" "$PIPELINE" \
  "the turn's tokens were dropped for its provider cost"

# ── Phase A: the coverage stamp ─────────────────────────────────────────────
# Without the adapter's declaration every token count is withheld, including
# the ones that were counted.
assert_go_test_goes_red \
  "the metrics adapter not stamping the adapter's coverage" \
  "core/adapters/outbound/metrics/adapter.go" \
  $'\ta.stampUsageCoverage(result, adapter)\n' \
  $'\n' \
  "$LS_PKG" "$PIPELINE" \
  "want 1000000"

# ── Phase A: the declaration agrees with adapters.json ──────────────────────
# No shipped column says token_usage is "absent", so the mutation creates one:
# Claude Code declares all four token types, and its column now says it has no
# token reporting.
assert_go_test_goes_red \
  "a column saying token_usage is absent while its adapter declares token types" \
  "replaydata/agents/adapters.json" \
  $'    "claudecode": {\n      "maturity": "beta",\n      "capabilities": {\n' \
  $'    "claudecode": {\n      "maturity": "beta",\n      "capabilities": {\n        "token_usage": "absent",\n' \
  "./tools/onboarding-factory/cmd/of/" "TestUsageCoverage_AgreesWithTheCapabilityModel" \
  "says token_usage is absent"

# ── Not here: Claude Code's ExcludesSubagents evidence ──────────────────────
# TestUsageCoverage_ParentIgnoresSubagentUsageSummary is held by two sites in
# claudecode/parser.go at once; as run on 2026-11-21 each single-site mutation
# left it green and only both together turned it red. mutate.sh takes one
# anchor, so that lock is documented in the test itself instead.

# ── Phase B: the requested pane, and only it ────────────────────────────────
assert_go_test_goes_red \
  "matching sessions regardless of pane answers for every pane" \
  "core/cmd/irrlicht-ls/herdrusage.go" \
  $'\t\tif l == nil || l.HerdrPaneID != paneID {\n' \
  $'\t\tif l == nil {\n' \
  "$LS_PKG" "TestHerdrUsage_Goldens/two_panes" \
  "document differs from testdata/herdr-usage/two-panes/want-p"

# ── Phase B: the ambiguous-pane refusal ─────────────────────────────────────
assert_go_test_goes_red \
  "answering a pane id seen on two herdr servers instead of refusing" \
  "core/cmd/irrlicht-ls/herdrusage.go" \
  $'\tif want == "" && len(sockets) > 1 {\n' \
  $'\tif false && want == "" && len(sockets) > 1 {\n' \
  "$LS_PKG" "TestHerdrUsage_SamePaneOnTwoServersIsRefused" \
  "exit 0, want 2"

if [[ $fails -gt 0 ]]; then
  echo "$NAME: $fails FAILED"
  exit 1
fi
echo "$NAME: ALL PASS"
