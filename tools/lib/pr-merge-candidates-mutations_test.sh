#!/usr/bin/env bash
# pr-merge-candidates-mutations_test.sh — the committed mutation fixtures for
# tools/pr-merge-candidates.sh (#2128).
#
# WHY THIS FILE EXISTS. The issue's acceptance criterion is that each check is
# SEEN red: mutate it out and its fixture must fail. A row in
# tools/lib/pr-merge-candidates_test.sh that stays green when the rule it names
# is deleted proves nothing about that rule — the other rules, or a skip
# upstream of it, would be what kept it green. Each row below deletes or
# neutralises ONE rule through tools/mutate.sh and requires the lock test to
# fail with the message of the fixture row built to isolate that rule.
#
# Every row drives the real tools/mutate.sh (stale-anchor and ambiguous-anchor
# refusal, byte-for-byte restore, never touching git state) through
# tools/lib/mutation-assert.sh's assert_mutation_is_red.
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$DIR/../.." && pwd)
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
MUTATE_SH=$REPO_ROOT/tools/mutate.sh
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/pr-merge-candidates_test.sh
# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo 'pr-merge-candidates-mutations: CANNOT RUN — worktree is dirty' >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then exit 1; fi
  exit 0
fi

# The lock test's live-repository check says nothing about a mutated rule and
# costs API calls on every row; the stub cases are what these rows grade.
export PRM_TEST_OFFLINE=1

S=tools/pr-merge-candidates.sh
fails=0

# ── Skip rules (draft, CI, mergeability, already handled) ───────────────────
assert_mutation_is_red 'dropping the draft rule is caught' "$S" \
  '(if .isDraft == false then [] else ["draft"] end)' \
  '([])' \
  '#101 draft'

assert_mutation_is_red 'dropping the CI-pending rule is caught' "$S" \
  '  + [ $checks[] | select(check_state == "pending") | "ci-pending:\(check_name | safe_check)" ]' \
  '  + []' \
  '#102 CI pending'

assert_mutation_is_red 'dropping the CI-failed rule is caught' "$S" \
  '  + [ $checks[] | select(check_state == "failed") | "ci-failed:\(check_name | safe_check)" ];' \
  '  + [];' \
  '#103 CI failed'

assert_mutation_is_red 'accepting a required check that never reported is caught' "$S" \
  '      | if ($m | length) == 0 then "ci-missing:\($r)"' \
  '      | if ($m | length) == 0 then empty' \
  '#126 required check never reported'

assert_mutation_is_red 'dropping the CLEAN requirement is caught' "$S" \
  '  if .mergeable == "MERGEABLE" and .mergeStateStatus == "CLEAN" then []' \
  '  if true then []' \
  '#115 not CLEAN alone'

assert_mutation_is_red 'dropping the handled-at-this-SHA skip is caught' "$S" \
  $'  if [ "$(jq -r \'.sha // ""\' "$TMP/marker.json")" = "$head_sha" ]; then' \
  '  if false; then' \
  '#109 handled at the same SHA'

assert_mutation_is_red 'counting a marker from anyone, not only collaborators, is caught' "$S" \
  '  [ .[] | select((.user.login // "") as $u | $collab | any(. == $u))' \
  '  [ .[]' \
  '#112 forged marker'

# ── Tiers ───────────────────────────────────────────────────────────────────
assert_mutation_is_red 'trusting a non-collaborator is caught' "$S" \
  '    elif .author.is_bot != true and .isCrossRepository == false and ($collab | any(. == $u)) then "trusted"' \
  '    elif .author.is_bot != true and .isCrossRepository == false then "trusted"' \
  '#130 non-collaborator, head in repo'

assert_mutation_is_red 'trusting a collaborator whose head is in a fork is caught' "$S" \
  '    elif .author.is_bot != true and .isCrossRepository == false and ($collab | any(. == $u)) then "trusted"' \
  '    elif .author.is_bot != true and ($collab | any(. == $u)) then "trusted"' \
  '#114 collaborator from a fork'

assert_mutation_is_red 'letting an external PR through without the external-author hold is caught' "$S" \
  '  | ( (if $tier == "external" then ["external-author"] else [] end)' \
  '  | ( ([])' \
  '#107 external author'

assert_mutation_is_red 'accepting an unverified dependabot commit is caught' "$S" \
  '   + [ $commits[] | select(.commit.verification.verified != true) | "dependabot-unverified-commit" ]' \
  '   + []' \
  '#121 dependabot unverified commit'

assert_mutation_is_red 'accepting a non-dependabot commit on a dependabot PR is caught' "$S" \
  $'   + [ $commits[] | select((.author.login // "") != "dependabot[bot]") | "dependabot-foreign-commit" ]' \
  '   + []' \
  '#128 dependabot with a merge commit'

assert_mutation_is_red 'ignoring update-type semver-major is caught' "$S" \
  '        | if $d.update == "version-update:semver-major" then "dependabot-major:\($d.name | safe)"' \
  '        | if false then "dependabot-major:\($d.name | safe)"' \
  '#106 dependabot major'

assert_mutation_is_red 'ignoring a major change in the from/to fallback is caught' "$S" \
  '              elif $changed then "dependabot-major:\($d.name | safe)"' \
  '              elif false then "dependabot-major:\($d.name | safe)"' \
  '#124 dependabot from/to major'

# ── Holds ───────────────────────────────────────────────────────────────────
assert_mutation_is_red 'dropping the .github/ path rule is caught' "$S" \
  '  {rule: "github",             re: "^\\.github/"},' \
  '' \
  '#108 sensitive path'

assert_mutation_is_red 'dropping the tools/ path rule is caught' "$S" \
  '  {rule: "tools",              re: "^tools/"},' \
  '' \
  '#127 tools/ path'

assert_mutation_is_red 'dropping the unusual-path rule is caught' "$S" \
  '  if ($p | safe) == "<unusual>" then [{rule: "unusual-path", path: "<unusual>"}]' \
  '  if false then [{rule: "unusual-path", path: "<unusual>"}]' \
  '#116 unusual file name'

assert_mutation_is_red 'dropping the opaque-file rule is caught' "$S" \
  '       then [{rule: "opaque", path: ($f | safe)}] else [] end)' \
  '       then [] else [] end)' \
  '#119 opaque and large files'

assert_mutation_is_red 'dropping the large-file rule is caught' "$S" \
  '         elif ($blob.size // 0) > $max_bytes then [{rule: "large-file", path: ($f | safe)}]' \
  '         elif false then [{rule: "large-file", path: ($f | safe)}]' \
  '#119 opaque and large files'

assert_mutation_is_red 'dropping the not-in-tree rule is caught' "$S" \
  '       | if $blob == null then [{rule: "not-in-tree", path: ($f | safe)}]' \
  '       | if $blob == null then []' \
  '#129 truncated tree, missing blob'

assert_mutation_is_red 'dropping the truncated-tree rule is caught' "$S" \
  '      + (if $tree.truncated != false then ["tree-truncated"] else [] end)' \
  '      + []' \
  '#129 truncated tree, missing blob'

assert_mutation_is_red 'dropping the base64-blob rule is caught' "$S" \
  '  + (if any(lines("+")[]; test("[A-Za-z0-9+/_-]{200,}")) then [{rule: "base64-blob", path: ($f | safe)}] else [] end)' \
  '  + []' \
  '#120 hidden content'

assert_mutation_is_red 'dropping hidden-unicode in added lines is caught' "$S" \
  '  + (if any(lines("+")[]; hidden_unicode) then [{rule: "hidden-unicode", path: ($f | safe)}] else [] end)' \
  '  + []' \
  '#120 hidden content'

assert_mutation_is_red 'dropping hidden-unicode in the title/body is caught' "$S" \
  '      + (if (($pr.title // "") + "\n" + ($pr.body // "")) | hidden_unicode then ["hidden-unicode-in-description"] else [] end)' \
  '      + []' \
  '#120 hidden content'

assert_mutation_is_red 'dropping the go.mod new-module rule is caught' "$S" \
  '      | {rule: "new-dependency", path: "\($f | safe):\(. | safe)"} ]' \
  '      | empty ]' \
  '#117 go.mod new module and replace'

assert_mutation_is_red 'dropping the go.mod directive rule is caught' "$S" \
  '  + (if any($add[]; .kind == "other") then [{rule: "go-mod-directive", path: ($f | safe)}] else [] end);' \
  '  + [];' \
  '#117 go.mod new module and replace'

assert_mutation_is_red 'dropping the package.json new-key rule is caught' "$S" \
  '      | if (.key as $k | $removed_keys | any(. == $k) | not) then {rule: "package-json-new-key", path: "\($f | safe):\(.key | safe)"}' \
  '      | if false then {rule: "package-json-new-key", path: "\($f | safe):\(.key | safe)"}' \
  '#118 package.json script and new key'

assert_mutation_is_red 'dropping the package.json value rule is caught' "$S" \
  '        elif (.val | test("^[~^]?(>=|<=|>|<|=)?v?[0-9][0-9A-Za-z.+-]*$") | not) then {rule: "package-json-value", path: "\($f | safe):\(.key | safe)"}' \
  '        elif false then {rule: "package-json-value", path: "\($f | safe):\(.key | safe)"}' \
  '#118 package.json script and new key'

assert_mutation_is_red 'dropping the package.json structure rule is caught' "$S" \
  '  + (if any($add[]; .kind == "other") then [{rule: "package-json-structure", path: ($f | safe)}] else [] end);' \
  '  + [];' \
  '#118 package.json script and new key'

assert_mutation_is_red 'dropping the needs-human label cap is caught' "$S" \
  '      + (if ($pr.labels // []) | any(.name == $hold_label) then ["needs-human-label"] else [] end)' \
  '      + []' \
  '#110 re-pushed after a hold'

assert_mutation_is_red 'dropping the incomplete-file-list hold is caught' "$S" \
  '         elif ($files | length) != ($pr.changedFiles // -1) then ["file-list-incomplete"] else [] end)' \
  '         else [] end)' \
  '#122 file list incomplete'

# ── Plumbing: pagination, exit status, refusals, read-only ──────────────────
assert_mutation_is_red 'reading only the first page of a paginated answer is caught' "$S" \
  $'    jq -e -s \'if length > 0 and all(.[]; type == "array") then add else error("not a JSON array") end\' \\' \
  $'    jq -e -s \'if length > 0 and all(.[]; type == "array") then .[0] else error("not a JSON array") end\' \\' \
  '#104 trusted and clean'

assert_mutation_is_red 'exiting 0 when nothing is actionable is caught' "$S" \
  $'  exit 0\nfi\nexit 1' \
  $'  exit 0\nfi\nexit 0' \
  'quiet world'

assert_mutation_is_red 'ignoring a failed gh call is caught' "$S" \
  $'  if [ "$rc" -ne 0 ]; then\n    echo "REFUSE: pr-merge-candidates — could not read' \
  $'  if false; then\n    echo "REFUSE: pr-merge-candidates — could not read' \
  'an HTTP error from the tree API'

assert_mutation_is_red 'accepting non-JSON from gh is caught' "$S" \
  $'  if [ "$?" -ne 0 ]; then\n    echo "REFUSE: pr-merge-candidates — gh answered' \
  $'  if false; then\n    echo "REFUSE: pr-merge-candidates — gh answered' \
  'non-JSON from'

assert_mutation_is_red 'a gh write outside gh_get is caught' "$S" \
  ': >"$TMP/results.jsonl"' \
  $': >"$TMP/results.jsonl"\ngh pr merge 1 --squash >/dev/null 2>&1 || true' \
  'runs gh outside gh_get'

assert_mutation_is_red 'dropping the write-flag guard in gh_get is caught' "$S" \
  '      -X | --method | --method=* | -X* | -f | -F | --field | --field=* | --raw-field | --raw-field=* | --input | --input=*)' \
  '      --never-matches-anything)' \
  'gh_get did not refuse'

assert_mutation_is_red 'letting gh_get run any subcommand is caught' "$S" \
  $'    \'pr list\' | \'api \'*) ;;' \
  '    *) ;;' \
  'gh_get did not refuse'

if [[ $fails -gt 0 ]]; then
  echo "pr-merge-candidates-mutations: $fails FAILED"
  exit 1
fi
echo 'pr-merge-candidates-mutations: ALL PASS'
