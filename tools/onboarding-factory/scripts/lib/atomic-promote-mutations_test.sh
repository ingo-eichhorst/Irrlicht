#!/usr/bin/env bash
# atomic-promote-mutations_test.sh — the committed mutation fixture for
# atomic-promote.sh's known_failing exception (#1967).
#
# WHY THIS FILE EXISTS. The known_failing bypass added to atomic-promote.sh
# IS protected by a defect test — atomic-promote_test.sh's "validation fails
# but the cell declares known_failing:true" case was seen RED against the
# pre-#1967 gate (returns 3 / nothing written) before the fix existed; see
# the PR description for that captured output. But the ADJACENT guarantee —
# that a cell NOT marked known_failing still gets refused outright — has no
# equivalent "before" to run red here: it is the unchanged #1333/B2
# behaviour, so atomic-promote_test.sh's "validation fails: NOTHING is
# written (this is B2)" case passes by construction (a LOCK, not evidence).
# Per AGENTS.md, a check like that earns its place only by being seen to fail
# when the thing it protects is actually broken — so this file breaks it. The
# plausible regression is the known_failing check silently degrading to
# "bypass always", which would quietly reopen the exact hole #1333 closed,
# for EVERY sub-100% candidate rather than only known_failing ones.
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

ANCHOR=$'      if [[ "$(head -n1 "$cell_dir/expected.jsonl" | jq -r \'.known_failing // false\' 2>/dev/null || echo false)" == "true" ]]; then\n        promote_rc=2\n      else\n        return 3\n      fi'
REPLACEMENT=$'      if true; then\n        promote_rc=2\n      else\n        return 3\n      fi'

echo "== mutation: the known_failing check always bypasses (reopens #1333/B2 for EVERY sub-100% candidate) =="
OUT="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$SUBJECT" "$ANCHOR" "$REPLACEMENT" bash "$LOCK_TEST" 2>&1)"
RC=$?

if [[ "$RC" -ne 0 ]]; then
  echo "FAIL: atomic-promote-mutations — mutate.sh itself refused (exit $RC), which is a fixture bug" >&2
  echo "      (a STALE anchor means atomic-promote.sh moved and this fixture needs updating), NOT" >&2
  echo "      evidence about the guard:" >&2
  echo "$OUT" | sed 's/^/      | /' >&2
  fails=$((fails + 1))
else
  # mutate.sh's own trailer is `=== <cmd...> exit=<rc> ===` — LOCK_TEST's own
  # exit code, independent of what mutate.sh itself returned (0 here, because
  # it successfully applied the mutation, ran the test, and restored).
  INNER_RC="$(echo "$OUT" | sed -n 's/^=== .* exit=\([0-9]*\) ===$/\1/p' | tail -n1)"
  if [[ "$INNER_RC" == "0" ]]; then
    echo "FAIL: atomic-promote-mutations — atomic-promote_test.sh stayed GREEN (exit 0) under the" >&2
    echo "      mutation, so the known_failing check does not reach what it claims to protect:" >&2
    echo "$OUT" | sed 's/^/      | /' >&2
    fails=$((fails + 1))
  elif ! grep -qF '  FAIL: returns 3 — expected [3] got [2]' <<<"$OUT"; then
    echo "FAIL: atomic-promote-mutations — went red, but not with the expected B2 rc assertion:" >&2
    echo "$OUT" | grep '  FAIL:' | sed 's/^/      | /' >&2
    fails=$((fails + 1))
  elif ! grep -qF '  FAIL: no recording left behind — expected [absent] got [present]' <<<"$OUT"; then
    echo "FAIL: atomic-promote-mutations — the rc assertion caught it, but the B2 guarantee itself" >&2
    echo "      (no recording left behind) did NOT go red — a partial catch is not proof the hole" >&2
    echo "      is closed:" >&2
    echo "$OUT" | grep '  FAIL:' | sed 's/^/      | /' >&2
    fails=$((fails + 1))
  else
    echo "ok  the B2 lock caught the always-bypass mutation on both assertions"
  fi
fi

if [[ $fails -gt 0 ]]; then
  echo "atomic-promote-mutations: $fails FAILED"
  exit 1
fi
echo "atomic-promote-mutations: ALL PASS"
