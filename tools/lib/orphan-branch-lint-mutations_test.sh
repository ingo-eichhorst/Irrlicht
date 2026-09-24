#!/usr/bin/env bash
# orphan-branch-lint-mutations_test.sh — the committed mutation fixtures for
# tools/lib/orphan-branch-lint_test.sh (#2029).
#
# WHY THIS FILE EXISTS. tools/orphan-branch-lint.sh is a check the #2029
# change ADDS, so it has no "before the fix" to run red; per AGENTS.md it
# earns its place only by being seen to fail when the thing it protects is
# broken. Each row below breaks one rule of the lint and requires its suite
# to go red naming that rule. They are separate rows because one combined
# mutation could go red while one of the rules was actually unguarded.
#
# Every row drives the real tools/mutate.sh (stale-anchor guard, no-op
# refusal, byte-for-byte restore that never touches git state).
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$DIR/../.." && pwd)
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
MUTATE_SH=$REPO_ROOT/tools/mutate.sh
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/orphan-branch-lint_test.sh

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo 'orphan-branch-lint-mutations: CANNOT RUN — worktree is dirty' >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then exit 1; fi
  exit 0
fi

fails=0

# ── 1. A branch it could not ask about is skipped instead of refusing ───────
# The row that matters most: with it, a gh outage reads as a clean day.
assert_mutation_is_red \
  'skipping an unanswerable branch instead of refusing the run is caught' \
  'tools/orphan-branch-lint.sh' \
  $'    *) refuse "could not ask whether \'$short\' has a PR, so no verdict is possible: $out" ;;' \
  $'    *) continue ;;' \
  'gh failing mid-loop must exit 2'

# ── 2. The age threshold stops separating FAIL from INFO ────────────────────
assert_mutation_is_red \
  'treating every orphan as fresh is caught' \
  'tools/orphan-branch-lint.sh' \
  $'  if [ $((now - tip)) -ge "$max_age_s" ]; then' \
  $'  if false; then' \
  'an old orphan must exit 1'

# ── 3. A rotted waiver reads as clean ───────────────────────────────────────
assert_mutation_is_red \
  'dropping the stale-waiver check is caught' \
  'tools/orphan-branch-lint.sh' \
  $'  printf \'%s\' "$live_waivers" | grep -qxF -- "$w" || stale="$stale$w"$\'\\n\'' \
  $'  :' \
  'a stale waiver must exit 1 and be named'

# ── 4. A branch level with main gets asked about (or named) ─────────────────
assert_mutation_is_red \
  'no longer skipping branches that are level with main is caught' \
  'tools/orphan-branch-lint.sh' \
  $'  [ "$ahead" -gt 0 ] || continue' \
  $'  :' \
  'gh was asked about'

if [[ $fails -gt 0 ]]; then
  echo "orphan-branch-lint-mutations: $fails FAILED"
  exit 1
fi
echo 'orphan-branch-lint-mutations: ALL PASS'
