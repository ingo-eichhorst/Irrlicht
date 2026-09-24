#!/usr/bin/env bash
# fleet-checkers-mutations_test.sh — the committed mutation fixtures for the
# fleet checkers and their own suites: the three from #2022, plus
# tools/lib/pr-exists.sh from #2029.
#
# WHY THIS FILE EXISTS. Review of #2022 ran one mutation this repo's corpus
# could not catch: widening `fleet_scope_overlap`'s containment test from
# tree keys to EVERY key left `fleet-scope-overlap_test.sh` printing 13 of 13
# PASS, because no fixture pair had a file-derived key that was a strict
# prefix of another. A restriction the file's own header calls load-bearing
# had no test that could go red. That is the gap this file closes, and the
# other rows are here so the same thing cannot happen to the rules beside it.
#
# It covers FOUR lock tests rather than one, so `LOCK_TEST` is reassigned
# between groups. The house convention is one `<lock>-mutations_test.sh` per
# lock; four near-empty files for four checkers that are only ever changed
# together would be worse, so the grouping is deliberate and named here.
# tools/lib/fleet-contract-mutations_test.sh stays separate: it mutates SKILL
# prose, not shell.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this
# file must not re-improvise: the stale-anchor guard, the no-op replacement
# refusal, and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$DIR/../.." && pwd)
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
MUTATE_SH=$REPO_ROOT/tools/mutate.sh
# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo 'fleet-checkers-mutations: CANNOT RUN — worktree is dirty' >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then exit 1; fi
  exit 0
fi

fails=0

# ── Group 1: tools/lib/fleet-scope-overlap.sh ───────────────────────────────
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/fleet-scope-overlap_test.sh

# The row review of #2022 asked for by name. Before adapters-root-file.md
# existed, this mutation left the whole suite green.
assert_mutation_is_red \
  'widening containment to every key is caught' \
  'tools/lib/fleet-scope-overlap.sh' \
  $'*/) [ "${kb_base#"$ka_base"/}" != "$kb_base" ] && shared="$shared $ka_base/>$kb_base" ;;' \
  $'*) [ "${kb_base#"$ka_base"/}" != "$kb_base" ] && shared="$shared $ka_base/>$kb_base" ;;' \
  'a file-derived parent key does not swallow a child package'

assert_mutation_is_red \
  'running dirname over a tree declaration is caught' \
  'tools/lib/fleet-scope-overlap.sh' \
  $'          */)\n            key="${token%/}/"\n            ;;' \
  $'          */)\n            key=$(dirname "$token")\n            ;;' \
  'a declared tree contains a file beneath it'

assert_mutation_is_red \
  'keying a bare directory to its parent is caught' \
  'tools/lib/fleet-scope-overlap.sh' \
  $'              # A path with no known extension names a directory.\n              key="$token/"' \
  $'              # A path with no known extension names a directory.\n              key=$(dirname "$token")' \
  'a bare directory collides with a file inside it'

assert_mutation_is_red \
  'dropping the extensionless root files is caught' \
  'tools/lib/fleet-scope-overlap.sh' \
  $"FLEET_SCOPE_BARE_FILES='go.work go.work.sum go.mod go.sum Makefile Dockerfile LICENSE CODEOWNERS'" \
  $"FLEET_SCOPE_BARE_FILES=''" \
  'two tickets both touching go.work overlap'

# The extension vocabulary is named once so the with-slash and no-slash arms
# cannot drift apart. Emptying it must reach both.
assert_mutation_is_red \
  'emptying the file-extension vocabulary is caught' \
  'tools/lib/fleet-scope-overlap.sh' \
  $"FLEET_SCOPE_FILE_EXTS='md json sh go yml yaml swift js ts html css rs py txt'" \
  $"FLEET_SCOPE_FILE_EXTS=''" \
  'two files in the same package overlap'

# A disclaimer bullet carrying a second path must refuse, not drop the line.
assert_mutation_is_red \
  'dropping a mixed disclaim-and-declare bullet in silence is caught' \
  'tools/lib/fleet-scope-overlap.sh' \
  $'            echo "AMBIGUOUS: $label — a disclaiming bullet also names another path, so its scope cannot be read: $line" >&2\n            refused=1' \
  $'            :' \
  'a bullet that disclaims AND declares refuses'

# ── Group 2: tools/lib/fleet-review-evidence.sh ─────────────────────────────
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/fleet-review-evidence_test.sh

assert_mutation_is_red \
  'accepting a hand-back with no header line is caught' \
  'tools/lib/fleet-review-evidence.sh' \
  $'        reason="no \'review: effort=<tier> findings=<N>\' line; prose alone cannot show that a review ran"' \
  $'        reason=""' \
  '"conventional commit" does not satisfy the category test'

assert_mutation_is_red \
  'reading the header count case-sensitively again is caught' \
  'tools/lib/fleet-review-evidence.sh' \
  $'      header=$(grep -iEm1 "$FLEET_REVIEW_HEADER_PATTERN" <<<"$text" |\n        tr \'[:upper:]\' \'[:lower:]\')' \
  $'      header=$(grep -iEm1 "$FLEET_REVIEW_HEADER_PATTERN" <<<"$text" |)' \
  'an upper-case header line still parses'

assert_mutation_is_red \
  'dropping the explicit no-findings requirement is caught' \
  'tools/lib/fleet-review-evidence.sh' \
  $'          grep -qi \'no findings\' <<<"$text" ||\n            reason="the line declares findings=0, but the hand-back never says \'no findings\'"' \
  $'          :' \
  'findings=0 without the words "no findings" is unproven'

# ── Group 3: tools/lib/ref-exists.sh ────────────────────────────────────────
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/ref-exists_test.sh

assert_mutation_is_red \
  'dropping the refs/heads/ prefix is caught' \
  'tools/lib/ref-exists.sh' \
  $'  out=$(git -c http.lowSpeedLimit=1000 -c http.lowSpeedTime=10 \\\n    ls-remote --exit-code --heads "$remote" "refs/heads/$branch" 2>&1)' \
  $'  out=$(git -c http.lowSpeedLimit=1000 -c http.lowSpeedTime=10 \\\n    ls-remote --exit-code --heads "$remote" "$branch" 2>&1)' \
  'a branch that exists reports 0'

assert_mutation_is_red \
  'letting a glob through instead of refusing it is caught' \
  'tools/lib/ref-exists.sh' \
  $'      echo "REFUSE: ref-exists — \'$branch\' carries a glob character; this check answers one exact ref, never a pattern" >&2\n      return 2' \
  $'      : # glob no longer refused' \
  'a glob in the branch name is refused'

# ── Group 4: tools/lib/pr-exists.sh (#2029) ─────────────────────────────────
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/pr-exists_test.sh

# The row the #2029 triage asked for by name: a check that lists every PR and
# searches the heads by substring reads `fix/x` as covered by `fix/x-2`.
assert_mutation_is_red \
  'replacing the exact-head query with a substring search is caught' \
  'tools/lib/pr-exists.sh' \
  $'  out=$(gh pr list --state all --head "$branch" --json number,isCrossRepository 2>&1)' \
  $'  out=$(gh pr list --state all --limit 100 --json headRefName 2>&1 | grep -qF "$branch" && echo \'[{"isCrossRepository":false}]\' || echo \'[]\')' \
  'a head that is only a prefix of another PR head reports 1'

assert_mutation_is_red \
  'reporting a gh failure as "no PR" is caught' \
  'tools/lib/pr-exists.sh' \
  $'    echo "REFUSE: pr-exists — could not ask gh about head \'$branch\' (gh exit $status): $out" >&2\n    return 2' \
  $'    return 1' \
  'gh failing is refused, not reported as absent'

# Counting "number" keys instead of parsing turns a non-JSON answer into a
# count of zero, i.e. "no PR" — the silent shape the jq array check exists
# to refuse.
assert_mutation_is_red \
  'reading a non-JSON answer as zero PRs is caught' \
  'tools/lib/pr-exists.sh' \
  $'  count=$(printf \'%s\' "$out" | jq -e \'if type == "array" then map(select(.isCrossRepository == false)) | length else error("not an array") end\' 2>/dev/null)' \
  $'  count=$(printf \'%s\' "$out" | awk \'/"number"/ { n++ } END { print n + 0 }\')' \
  'gh printing non-JSON is refused, not reported as absent'

# #2029 review: --head matches a fork's same-named head too.
assert_mutation_is_red \
  'counting a fork PR as this repository'"'"'s is caught' \
  'tools/lib/pr-exists.sh' \
  $'then map(select(.isCrossRepository == false)) | length else' \
  $'then length else' \
  'a head whose only PR is from a fork reports 1'

assert_mutation_is_red \
  'letting a glob through instead of refusing it is caught' \
  'tools/lib/pr-exists.sh' \
  $'      echo "REFUSE: pr-exists — \'$branch\' carries a glob character; this check answers one exact head, never a pattern" >&2\n      return 2' \
  $'      : # glob no longer refused' \
  'a glob in the branch name is refused'

if [[ $fails -gt 0 ]]; then
  echo "fleet-checkers-mutations: $fails FAILED"
  exit 1
fi
echo 'fleet-checkers-mutations: ALL PASS'
