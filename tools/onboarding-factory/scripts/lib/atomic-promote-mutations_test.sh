#!/usr/bin/env bash
# atomic-promote-mutations_test.sh — the committed mutation fixture for
# atomic-promote.sh's known_failing exception (#1967).
#
# WHY THIS FILE EXISTS. The known_failing bypass added to atomic-promote.sh
# IS protected by a defect test — atomic-promote_test.sh's "validation fails
# but the cell declares known_failing:true" case was seen RED against the
# pre-#1967 gate (returns 3 / nothing written) before the fix existed; see
# the PR description for that captured output. But two ADJACENT guarantees
# have no equivalent "before" to run red here, so each gets its own mutation:
#
#   1. A cell NOT marked known_failing still gets refused outright — the
#      unchanged #1333/B2 behaviour, so atomic-promote_test.sh's "validation
#      fails: NOTHING is written (this is B2)" case passes by construction (a
#      LOCK, not evidence). The plausible regression is the known_failing
#      check silently degrading to "bypass always", reopening the exact hole
#      #1333 closed for EVERY sub-100% candidate, not just known_failing
#      ones.
#   2. known_failing must be an actual JSON BOOLEAN, not merely a value that
#      LOOKS true when read as a raw string — a real QA finding against the
#      first version of this fix (#1967), not a hypothetical: `jq -r
#      '.known_failing // false'` compared against the bash string "true"
#      let a JSON STRING "true" through exactly like the JSON boolean, while
#      the Go validator's ExpectedMeta.KnownFailing is a real `bool` and
#      hard-errors on a string there before grading anything — rc=2,
#      candidate committed, expected_pass_rate stamped EMPTY. The plausible
#      regression is a future edit reverting the type-strict `jq -e '...  ==
#      true'` check back to that raw-string comparison, which
#      atomic-promote_test.sh's "known_failing must be an actual JSON
#      boolean" block (the with-known-failing-string case specifically)
#      exists to catch.
#
# Does NOT use tools/lib/mutation-assert.sh's shared `assert_mutation_is_red`:
# that helper greps the lock test's failures as `^FAIL:` anchored at column 0
# (its own header says so) — the convention test-mac-script_test.sh and
# checkpoint-idiom-guard_test.sh use. atomic-promote_test.sh predates this
# fixture and follows the OTHER convention most of tools/lib/*_test.sh uses
# (await-gone_test.sh, gate-budget_test.sh, gosec-report_test.sh, ...): a
# two-space-indented "  FAIL: <label> — expected [...] got [...]" line. This
# file drives tools/mutate.sh directly instead of the shared helper, so it can
# match that format.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this
# file must not re-improvise: the stale-anchor guard, the no-op replacement
# refusal, and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide for this).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../../../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
SUBJECT="tools/onboarding-factory/scripts/lib/atomic-promote.sh"
LOCK_TEST="$DIR/atomic-promote_test.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: atomic-promote-mutations — $1 not found" >&2; exit 1; }; }
need bash
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: atomic-promote-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh needs a clean tree for its post-restore emptiness check to mean
# anything (guard #4 in its own header). A DIRTY TREE MUST NOT SILENTLY PASS —
# see tools/lib/test-mac-script-mutations_test.sh for the full reasoning this
# fixture repeats rather than shares.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "atomic-promote-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/onboarding-factory/scripts/lib/atomic-promote-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running this mutation)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# run_mutation_row <label> <anchor> <replacement> <want-fail-1> [<want-fail-2>]
#   Applies <anchor>-><replacement> via the real tools/mutate.sh, requires
#   LOCK_TEST (atomic-promote_test.sh) to go RED, and requires every
#   <want-fail-*> line to appear among its failures — not just "some
#   assertion went red", which could be red for the wrong reason.
run_mutation_row() {
  local label="$1" anchor="$2" replacement="$3"; shift 3
  local out rc inner_rc want

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$SUBJECT" "$anchor" "$replacement" bash "$LOCK_TEST" 2>&1)"
  rc=$?

  if [[ "$rc" -ne 0 ]]; then
    echo "FAIL: atomic-promote-mutations — $label: mutate.sh itself refused (exit $rc), which is a" >&2
    echo "      fixture bug (a STALE anchor means atomic-promote.sh moved and this fixture needs" >&2
    echo "      updating), NOT evidence about the guard:" >&2
    echo "$out" | sed 's/^/      | /' >&2
    fails=$((fails + 1))
    return
  fi

  # mutate.sh's own trailer is `=== <cmd...> exit=<rc> ===` — LOCK_TEST's own
  # exit code, independent of what mutate.sh itself returned (0 here, because
  # it successfully applied the mutation, ran the test, and restored).
  inner_rc="$(echo "$out" | sed -n 's/^=== .* exit=\([0-9]*\) ===$/\1/p' | tail -n1)"
  if [[ "$inner_rc" == "0" ]]; then
    echo "FAIL: atomic-promote-mutations — $label: atomic-promote_test.sh stayed GREEN (exit 0) under" >&2
    echo "      the mutation, so the check does not reach what it claims to protect:" >&2
    echo "$out" | sed 's/^/      | /' >&2
    fails=$((fails + 1))
    return
  fi
  for want in "$@"; do
    if ! grep -qF "$want" <<<"$out"; then
      echo "FAIL: atomic-promote-mutations — $label: went red, but not with the expected assertion:" >&2
      echo "      wanted: $want" >&2
      echo "$out" | grep '  FAIL:' | sed 's/^/      | /' >&2
      fails=$((fails + 1))
      return
    fi
  done
  echo "ok  $label"
}

echo "== mutation 1: the known_failing check always bypasses (reopens #1333/B2 for EVERY sub-100% candidate) =="
ALWAYS_BYPASS_ANCHOR=$'      if head -n1 "$cell_dir/expected.jsonl" | jq -e \'.known_failing == true\' >/dev/null 2>&1; then\n        promote_rc=2\n      else\n        return 3\n      fi'
ALWAYS_BYPASS_REPLACEMENT=$'      if true; then\n        promote_rc=2\n      else\n        return 3\n      fi'
run_mutation_row \
  "the B2 lock catches the always-bypass mutation" \
  "$ALWAYS_BYPASS_ANCHOR" "$ALWAYS_BYPASS_REPLACEMENT" \
  '  FAIL: returns 3 — expected [3] got [2]' \
  '  FAIL: no recording left behind — expected [absent] got [present]'

echo "== mutation 2: known_failing reverts to a raw-string comparison — a JSON STRING \"true\" bypasses again (#1967 QA) =="
STRING_COERCION_ANCHOR="$ALWAYS_BYPASS_ANCHOR"
STRING_COERCION_REPLACEMENT=$'      if [[ "$(head -n1 "$cell_dir/expected.jsonl" | jq -r \'.known_failing // false\' 2>/dev/null || echo false)" == "true" ]]; then\n        promote_rc=2\n      else\n        return 3\n      fi'
run_mutation_row \
  "the non-boolean known_failing block catches the raw-string-comparison mutation" \
  "$STRING_COERCION_ANCHOR" "$STRING_COERCION_REPLACEMENT" \
  '  FAIL: with-known-failing-string: returns 3 (non-boolean known_failing must not bypass) — expected [3] got [2]' \
  '  FAIL: with-known-failing-string: no recording left behind — expected [absent] got [present]'

if [[ $fails -gt 0 ]]; then
  echo "atomic-promote-mutations: $fails FAILED"
  exit 1
fi
echo "atomic-promote-mutations: ALL PASS"
