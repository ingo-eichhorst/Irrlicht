#!/usr/bin/env bash
# Permanent checker-side mutation evidence for driverteardown (#1829).
#
# The package fixtures mutate drivers. These rows mutate the checker itself.
# Each exact source mutation must make the focused Go test fail and name the
# behavior that the mutation removed. tools/mutate.sh owns exact-once matching,
# byte-safe restoration, and the clean-tree proof.
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$DIR/../.." && pwd)
MUTATE_SH=$REPO_ROOT/tools/mutate.sh
PACKAGE=./tools/onboarding-factory/internal/driverteardown/...

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: driverteardown-checker-mutations — $1 not found" >&2; exit 1; }; }
need bash
need git
need go
need grep

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: driverteardown-checker-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "driverteardown-checker-mutations: CANNOT RUN — tools/mutate.sh needs a clean worktree" >&2
  echo "  Commit or clean up, then run: bash tools/lib/driverteardown-checker-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "FAIL: driverteardown-checker-mutations — dirty worktree in CI/strict mode" >&2
    exit 1
  fi
  exit 0
fi

baseline=$(cd "$REPO_ROOT" && go test "$PACKAGE" -count=1 2>&1)
baseline_rc=$?
if [[ $baseline_rc -ne 0 ]]; then
  echo "FAIL: driverteardown-checker-mutations — unmutated package failed; mutation results would prove nothing" >&2
  echo "$baseline" | sed 's/^/      | /'
  exit 1
fi

fails=0

# assert_mutation_red <label> <file> <anchor> <replacement> <test-regex> <expected-output>
assert_mutation_red() {
  local label=$1 file=$2 anchor=$3 replacement=$4 test_regex=$5 expected=$6
  local out rc

  out=$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    go test "$PACKAGE" -run "$test_regex" -count=1 -v 2>&1)
  rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — tools/mutate.sh refused with exit $rc" >&2
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if ! grep -qE ' exit=[1-9][0-9]* ===$' <<<"$out"; then
    echo "FAIL: $label — the focused Go test stayed green under the mutation" >&2
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$expected" <<<"$out"; then
    echo "FAIL: $label — the Go test failed for the wrong reason; expected $expected" >&2
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

CHECKER=tools/onboarding-factory/internal/driverteardown/driverteardown.go
NAMEFLOW=tools/onboarding-factory/internal/driverteardown/nameflow.go
SHELLSOURCE=tools/onboarding-factory/internal/driverteardown/shellsource.go

assert_mutation_red \
  'INV-1 step-dispatch narrowing' \
  "$CHECKER" \
  'case st.Func == "" && isStepDispatchArm(st):' \
  'case false:' \
  '^TestCheckerGradesEveryFixture$/^inv1_step_dispatch_case_arm_ok\.sh$' \
  '--- FAIL: TestCheckerGradesEveryFixture/inv1_step_dispatch_case_arm_ok.sh'

assert_mutation_red \
  'INV-1 top-level kill-site recognition' \
  "$CHECKER" \
  'return "this top-level teardown", true' \
  'return "", false' \
  '^TestCheckerGradesEveryFixture$/^inv1_gated_final_sweep\.sh$' \
  '--- FAIL: TestCheckerGradesEveryFixture/inv1_gated_final_sweep.sh'

assert_mutation_red \
  'INV-3 positions keyed by function definition' \
  "$NAMEFLOW" \
  'f.markPos(funcRef{file: path, name: st.Func}, positional)' \
  'f.markPos(funcRef{name: st.Func}, positional)' \
  '^TestCheckerGradesEveryFixture$/^inv3_shadowed_alloc_slot_ok\.sh$' \
  '--- FAIL: TestCheckerGradesEveryFixture/inv3_shadowed_alloc_slot_ok.sh'

assert_mutation_red \
  'unterminated quote refusal' \
  "$SHELLSOURCE" \
  $'\tif open != 0 {\n\t\treturn "", nil, 0, fmt.Errorf(' \
  $'\tif false {\n\t\treturn "", nil, 0, fmt.Errorf(' \
  '^TestCheckerRefusesRatherThanReportingClean$/^vacuous_unterminated_quote\.sh$' \
  '--- FAIL: TestCheckerRefusesRatherThanReportingClean/vacuous_unterminated_quote.sh'

assert_mutation_red \
  'INV-4 fail-closed shape remains graded' \
  "$CHECKER" \
  $'func (q staleVerdict) failsClosed() bool {\n\tfor _, a := range q.ep.assigns(q.src, q.write.v) {' \
  $'func (q staleVerdict) failsClosed() bool {\n\treturn true\n\tfor _, a := range q.ep.assigns(q.src, q.write.v) {' \
  '^TestCheckerGradesEveryFixture$/^inv4_unconditional_write\.sh$' \
  '--- FAIL: TestCheckerGradesEveryFixture/inv4_unconditional_write.sh'

assert_mutation_red \
  'graded refusal keeps prior findings' \
  "$CHECKER" \
  'return refuse(findings, err)' \
  'return nil, err' \
  '^TestARefusalStillNamesTheFindingThatCausedIt$' \
  '--- FAIL: TestARefusalStillNamesTheFindingThatCausedIt'

assert_mutation_red \
  'INV-4 guarded-sentinel shape remains accepted' \
  "$CHECKER" \
  'case q.ep.setsAny(q.src, others):' \
  'case false:' \
  '^TestCheckerGradesEveryFixture$/^inv4_renamed_sentinel_ok\.sh$' \
  '--- FAIL: TestCheckerGradesEveryFixture/inv4_renamed_sentinel_ok.sh'

assert_mutation_red \
  'INV-3 mint-site recognition' \
  "$NAMEFLOW" \
  $'func composedLiteral(word string) (string, bool) {\n\tif _, _, isVar := soleVar(word); isVar {' \
  $'func composedLiteral(word string) (string, bool) {\n\treturn "", false\n\tif _, _, isVar := soleVar(word); isVar {' \
  '^TestCheckerGradesEveryFixture$/^good_driver\.sh$' \
  '--- FAIL: TestCheckerGradesEveryFixture/good_driver.sh'

# The comment guard is a separate obligation from the eight checker mutations.
assert_mutation_red \
  'cross-file shell line-number comment guard' \
  "$CHECKER" \
  $'// a tidy-up. Locate that reader with:\n//' \
  $'// a tidy-up. The reader is at run-cell.sh:443. Locate it with:\n//' \
  '^TestPackageCommentsDoNotUseCrossFileLineNumbers$' \
  'hard-coded cross-file shell line'

assert_mutation_red \
  'inline shell line-number comment guard' \
  'tools/onboarding-factory/internal/driverteardown/testdata/good_driver.sh' \
  '  tmux kill-session -t "$SESSION" 2>/dev/null || true' \
  '  tmux kill-session -t "$SESSION" 2>/dev/null || true;# run-cell.sh:443' \
  '^TestPackageCommentsDoNotUseCrossFileLineNumbers$' \
  'hard-coded cross-file shell line'

if [[ $fails -gt 0 ]]; then
  echo "driverteardown-checker-mutations: $fails FAILED" >&2
  exit 1
fi
echo 'driverteardown-checker-mutations: ALL PASS'
