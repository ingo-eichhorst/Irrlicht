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

# ── 1. A failed PR listing reads as "no PRs" ────────────────────────────────
# With it, a gh outage names every branch instead of refusing — or, with the
# threshold, reads as a verdict the lint never had the data for.
assert_mutation_is_red \
  'reading a failed gh listing as an empty one is caught' \
  'tools/orphan-branch-lint.sh' \
  $'listing=$(gh pr list --state all --limit "$LIST_LIMIT" --json headRefName,isCrossRepository 2>&1) ||\n  refuse "gh pr list failed, so no branch can be judged: $listing"' \
  $'listing=$(gh pr list --state all --limit "$LIST_LIMIT" --json headRefName,isCrossRepository 2>/dev/null || echo \'[]\')' \
  'gh failing must exit 2'

# ── 2. A listing that filled its --limit is read as complete ───────────────
assert_mutation_is_red \
  'dropping the truncation refusal is caught' \
  'tools/orphan-branch-lint.sh' \
  $'[ "$rows" -lt "$LIST_LIMIT" ] ||\n  refuse' \
  $'true ||\n  refuse' \
  'a listing that fills its --limit must exit 2 as truncated'

# ── 3. A fork PR with the same head name hides an orphan ────────────────────
assert_mutation_is_red \
  'counting fork PRs as this repository'"'"'s is caught' \
  'tools/orphan-branch-lint.sh' \
  $'jq -r \'.[] | select(.isCrossRepository == false) | .headRefName\'' \
  $'jq -r \'.[] | .headRefName\'' \
  'an old orphan must exit 1'

# ── 4. The age threshold stops separating FAIL from INFO ────────────────────
assert_mutation_is_red \
  'treating every orphan as fresh is caught' \
  'tools/orphan-branch-lint.sh' \
  $'  if [ $((now - tip)) -ge "$max_age_s" ]; then' \
  $'  if false; then' \
  'an old orphan must exit 1'

# ── 5. A rotted waiver reads as clean ───────────────────────────────────────
assert_mutation_is_red \
  'dropping the stale-waiver check is caught' \
  'tools/orphan-branch-lint.sh' \
  $'  grep -qxF -- "$w" <<<"$live_waivers" || stale="$stale$w"$\'\\n\'' \
  $'  :' \
  'a stale waiver must exit 1 and be named'

# ── 6. A branch level with main gets named ──────────────────────────────────
assert_mutation_is_red \
  'no longer skipping branches that are level with main is caught' \
  'tools/orphan-branch-lint.sh' \
  $'  [ "$ahead" -gt 0 ] || continue' \
  $'  :' \
  'a branch level with main was named'

# ── 8. The PR lookup goes back to a pipe ────────────────────────────────────
# The defect the first live run hit: printf | grep -q under pipefail SIGPIPEs
# on a large listing and reads an early match as "no PR".
assert_mutation_is_red \
  'a piped PR lookup that SIGPIPEs on a large listing is caught' \
  'tools/orphan-branch-lint.sh' \
  $'has_pr() { grep -qxF -- "$1" <<<"$pr_heads"; }' \
  $'has_pr() { printf \'%s\\n\' "$pr_heads" | grep -qxF -- "$1"; }' \
  'a branch whose PR is listed FIRST was named as an orphan'

# ── 7. A leading-zero threshold is read as octal ────────────────────────────
assert_mutation_is_red \
  'reading --max-age-hours as octal is caught' \
  'tools/orphan-branch-lint.sh' \
  $'max_age_s=$((10#$MAX_AGE_HOURS * 3600))' \
  $'max_age_s=$((MAX_AGE_HOURS * 3600))' \
  'max-age-hours 08 must be read as 8 hours'

if [[ $fails -gt 0 ]]; then
  echo "orphan-branch-lint-mutations: $fails FAILED"
  exit 1
fi
echo 'orphan-branch-lint-mutations: ALL PASS'
