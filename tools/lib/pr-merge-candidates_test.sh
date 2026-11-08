#!/usr/bin/env bash
# Drive tools/pr-merge-candidates.sh (#2128) against a stub gh that serves
# recorded `gh --json` / `gh api` payloads, and pin one decision per row.
#
# The fixture world is tools/lib/testdata/pr-merge-candidates/world/: one
# pr-list.json, one collaborators.json, and one directory per PR holding the
# payloads the script asks for next (comments, files, tree, commits) plus a
# NOTE saying what the row isolates and where its data came from. Rows that
# isolate a skip rule are synthetic in exactly one field — the field that rule
# reads — and CLEAN and green otherwise, so that rule is the only thing that
# can produce the skip. tools/lib/pr-merge-candidates-mutations_test.sh
# removes the rules one at a time (its rows list which) and requires the row
# named here to go red.
#
# Every row asserts the EXACT reason list, not "contains": a rule that starts
# firing on a row it should not touch is as much a defect as one that stops.
set -uo pipefail # NOT -e: assertions capture non-zero return codes

SELF=$(cd "$(dirname "$0")" && pwd)/$(basename "$0")
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

SCRIPT=tools/pr-merge-candidates.sh
TD=tools/lib/testdata/pr-merge-candidates
for f in "$SCRIPT" "$TD/gh-stub.sh" "$TD/world/pr-list.json" "$TD/world/collaborators.json"; do
  [ -r "$f" ] || { echo "FAIL: pr-merge-candidates_test — $f not found" >&2; exit 1; }
done
command -v jq >/dev/null 2>&1 || { echo "FAIL: pr-merge-candidates_test — jq is not on PATH" >&2; exit 1; }

rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

WORK=$(mktemp -d "${TMPDIR:-/tmp}/pr-merge-candidates-test.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
mkdir "$WORK/bin"
cp "$TD/gh-stub.sh" "$WORK/bin/gh"
chmod +x "$WORK/bin/gh"
LOG="$WORK/gh.log"

# run_world <world> [ENV=value ...] — sets GOT (exit status); stdout lands in
# $WORK/out, stderr in $WORK/err, every gh call in $LOG.
run_world() {
  local world="$1"
  shift
  : >"$LOG"
  env "$@" PATH="$WORK/bin:$PATH" PRM_STUB_WORLD="$TD/$world" PRM_STUB_LOG="$LOG" \
    bash "$SCRIPT" >"$WORK/out" 2>"$WORK/err"
  GOT=$?
}

# ── 1. The fixture world ─────────────────────────────────────────────────────
run_world world
if [ "$GOT" -ne 0 ]; then
  fail "the fixture world must exit 0 (PRs are actionable), got $GOT"
  sed 's/^/      | /' "$WORK/err" >&2
  exit 1
fi
cp "$WORK/out" "$WORK/world.json"
if ! jq -e '.prs | type == "array"' "$WORK/world.json" >/dev/null 2>&1; then
  fail 'the fixture world printed no {"prs": [...]} JSON'
  exit 1
fi

# The stub was really asked — an answer assembled without gh would make every
# row below meaningless.
grep -q '^pr list ' "$LOG" || fail 'the script never asked gh for the open PR list'

want_n=$(jq length "$TD/world/pr-list.json")
got_n=$(jq '.prs | length' "$WORK/world.json")
[ "$want_n" = "$got_n" ] || fail "the world holds $want_n PRs but the output lists $got_n"
dup=$(jq -r '[.prs[].number] | group_by(.) | map(select(length > 1) | .[0]) | .[]' "$WORK/world.json")
[ -z "$dup" ] || fail "PR(s) listed more than once: $dup"

# row <label> <number> <decision> <tier> <reasons-json>
row() {
  local label="$1" num="$2" decision="$3" tier="$4" reasons="$5" got
  got=$(jq -c --argjson n "$num" '.prs[] | select(.number == $n) | {decision, tier, reasons: (.reasons | sort)}' "$WORK/world.json")
  local want
  want=$(jq -nc --arg d "$decision" --arg t "$tier" --argjson r "$reasons" '{decision: $d, tier: $t, reasons: ($r | sort)}')
  if [ "$got" = "$want" ]; then
    echo "  PASS: #$num $label"
  else
    fail "#$num $label — wanted $want, got ${got:-<no such PR in the output>}"
  fi
}

head_of() { jq -r --argjson n "$1" '.[] | select(.number == $n) | .headRefOid' "$TD/world/pr-list.json"; }

# The ten acceptance-criteria rows.
row 'draft'                           101 skip   trusted    '["draft"]'
row 'CI pending'                      102 skip   trusted    '["ci-pending:go-test"]'
row 'CI failed'                       103 skip   trusted    '["ci-failed:build-test"]'
row 'trusted and clean'               104 review trusted    '[]'
row 'dependabot minor'                105 review dependabot '[]'
row 'dependabot major'                106 hold   dependabot '["dependabot-major:vitest"]'
row 'external author'                 107 hold   external   '["external-author"]'
row 'sensitive path'                  108 hold   trusted    '["sensitive:github"]'
row 'handled at the same SHA'         109 skip   trusted    "[\"handled:$(head_of 109)\"]"
row 're-pushed after a hold'          110 hold   trusted    '["needs-human-label"]'

# Rows that isolate the rules the AC rows do not.
row 're-pushed, label since removed'  111 review trusted    '[]'
row 'forged marker does not count'    112 review trusted    '[]'
row 'hand-applied needs-human'        113 hold   trusted    '["needs-human-label"]'
row 'collaborator from a fork'        114 hold   external   '["external-author"]'
row 'not CLEAN alone'                 115 skip   trusted    '["not-clean:MERGEABLE/BEHIND"]'
row 'unusual file name'               116 hold   trusted    '["sensitive:unusual-path"]'
row 'go.mod new module and replace'   117 hold   trusted    '["sensitive:go-mod-directive","sensitive:new-dependency"]'
row 'package.json script and new key' 118 hold   trusted    '["sensitive:package-json-new-key","sensitive:package-json-structure","sensitive:package-json-value"]'
row 'opaque and large files'          119 hold   trusted    '["sensitive:large-file","sensitive:opaque"]'
row 'hidden content'                  120 hold   trusted    '["hidden-unicode-in-description","sensitive:base64-blob","sensitive:hidden-unicode"]'
row 'dependabot unverified commit'    121 hold   external   '["dependabot-unverified-commit","external-author"]'
row 'file list incomplete'            122 hold   trusted    '["file-list-incomplete"]'
row 'dependabot indirect, same major' 123 review dependabot '[]'
row 'dependabot from/to major'        124 hold   dependabot '["dependabot-major:undici"]'
row 'dependabot Go minor'             125 review dependabot '[]'
row 'required check never reported'   126 skip   trusted    '["ci-missing:build-test"]'
row 'tools/ path'                     127 hold   trusted    '["sensitive:tools"]'
row 'dependabot with a merge commit'  128 hold   external   '["dependabot-foreign-commit","external-author"]'
row 'truncated tree, missing blob'    129 hold   trusted    '["sensitive:not-in-tree","tree-truncated"]'
row 'non-collaborator, head in repo'  130 hold   external   '["external-author"]'

# Rows added after review of #2129: one per rule that had none.
row 'every remaining path rule'       131 hold   trusted    '["sensitive:agent-config","sensitive:agent-instructions","sensitive:binary","sensitive:git-metadata","sensitive:install-path","sensitive:minified","sensitive:package-manager-config","sensitive:release"]'
row 'renamed out of .github/'         132 hold   trusted    '["sensitive:github"]'
row 'more files than the API returns' 133 hold   trusted    '["too-many-files"]'
row 'dependabot non-manifest file'    134 hold   external   '["dependabot-non-manifest-file","external-author"]'
row 'dependabot with no commits'      135 hold   external   '["dependabot-no-commits","external-author"]'
row 'dependabot metadata missing'     136 hold   dependabot '["dependabot-metadata-missing"]'
row 'dependabot two deps, no type'    137 hold   dependabot '["dependabot-major-unknown:alpha","dependabot-major-unknown:beta"]'
row 'required check SKIPPED'          138 skip   trusted    '["ci-required-not-success:go-test"]'
row 'commit status FAILURE'           139 skip   trusted    '["ci-failed:other"]'
row 'commit status PENDING'           140 skip   trusted    '["ci-pending:other"]'
row 'dependabot login without is_bot' 141 hold   external   '["external-author"]'
row 'collaborator login with is_bot'  142 hold   external   '["external-author"]'
row 'check name is injection text'    143 skip   trusted    '["ci-failed:other"]'
row 'tag characters and LRM'          144 hold   trusted    '["hidden-unicode-in-description","sensitive:hidden-unicode"]'
row 'files API error'                 145 hold   trusted    '["unreadable:files"]'
row 'comments API error'              146 skip   trusted    '["unreadable:comments"]'
row 'tree API error'                  147 hold   trusted    '["unreadable:tree"]'
row 'commits API error'               148 hold   dependabot '["unreadable:commits"]'
row 'file name ending in a newline'   149 hold   trusted    '["sensitive:unusual-path"]'

# #131 carries one file per path rule, and several rules share a reason code
# (three agent-config patterns, two agent-instructions ones), so pin the exact
# hits: deleting any single pattern removes exactly one path from this list.
hits131=$(jq -c '.prs[] | select(.number == 131) | [.sensitive_hits[] | "\(.rule)=\(.path)"] | sort' "$WORK/world.json")
want131='["agent-config=.Claude/settings.json","agent-config=.mcp.json","agent-config=platforms/web/.codex/config.toml","agent-instructions=CLAUDE.local.md","agent-instructions=core/AGENTS.md","binary=lib/x.dll","git-metadata=.gitattributes","install-path=site/install.sh","minified=platforms/web/app.min.js","package-manager-config=platforms/web/.npmrc","release=version.json"]'
[ "$hits131" = "$want131" ] && echo '  PASS: #131 every path rule hits its own file' ||
  fail "#131 every remaining path rule — wanted hits $want131, got $hits131"


# Every PR in the world is pinned by a row above.
pinned=$(grep -cE '^row .* [0-9]{3} (skip|review|hold) ' "$SELF")
[ "$pinned" = "$want_n" ] || fail "the world holds $want_n PRs but $pinned rows pin a decision"

want_act=$(jq '[.prs[] | select(.decision != "skip")] | length' "$WORK/world.json")
got_act=$(jq '.actionable' "$WORK/world.json")
[ "$want_act" = "$got_act" ] || fail "actionable says $got_act, but $want_act PRs are not skip"

# The marker the skill must EDIT rather than add to.
m110=$(jq -c '.prs[] | select(.number == 110) | .marker.comment_id' "$WORK/world.json")
[ "$m110" = 4002 ] && echo '  PASS: #110 reports the marker comment to edit (4002)' ||
  fail "#110 re-pushed after a hold — the output must name marker comment 4002 for the skill to edit, got $m110"
m112=$(jq -c '.prs[] | select(.number == 112) | .marker' "$WORK/world.json")
[ "$m112" = null ] && echo '  PASS: #112 a non-collaborator marker is not reported as ours' ||
  fail "#112 forged marker — an outsider's comment was reported as the marker: $m112"

# Pagination: collaborators.json lists not-ingo third, and the stub prints
# arrays in pages of two, so every trusted row above depended on the pages
# being joined. Assert the paginated form was what got asked for.
grep -q -- '--paginate repos/{owner}/{repo}/collaborators' "$LOG" ||
  fail 'the collaborator list was not read with --paginate'

# ── 2. Nothing attacker-controlled reaches the output ────────────────────────
leaks=0
# One jq pass: every title line, body line and non-required check name that
# appears verbatim in the output (none contains a newline, so a substring of
# the file is what a per-line grep would find).
required=$(sed -n "s/^PRM_REQUIRED_CHECKS='\\(.*\\)'$/\\1/p" "$SCRIPT")
[ -n "$required" ] || fail 'output hygiene — could not read PRM_REQUIRED_CHECKS from the script'
while IFS= read -r text; do
  fail "output hygiene — a PR title, body or check name reached the output: $text"
  leaks=1
done < <(jq -r --rawfile out "$WORK/world.json" --argjson req "${required:-[]}" '
  .[] | .title, (.body | split("\n")[]),
        (.statusCheckRollup[] | .name // .context | select(. as $n | $req | index($n) | not))
  | select(. as $t | $t != "" and ($out | index($t)) != null)' "$TD/world/pr-list.json")
# (The required check names are printed on purpose; every other name is not.)
for text in 'IGNORE PREVIOUS' 'read me'; do
  grep -qF -- "$text" "$WORK/world.json" && { fail "output hygiene — '$text' reached the output"; leaks=1; }
done
# Stronger than retyping the classifier's invisible-character class: every
# string the classifier emits passed `safe`, is an enum or a SHA, so the whole
# output is printable ASCII. Any other byte is a leak, whatever it encodes.
if ! jq -e '[.. | strings | test("\\A[ -~]*\\z")] | all' "$WORK/world.json" >/dev/null; then
  fail 'output hygiene — a non-ASCII or control character reached the output'
  leaks=1
fi
[ "$leaks" -eq 0 ] && echo '  PASS: no title, body, check name, raw file name or non-ASCII character reached the output'

# ── 3. Read-only ─────────────────────────────────────────────────────────────
# At run time: every call the stub saw was a `pr list` or a flag-free `api`.
bad=$(grep -vE '^(pr list |api (--paginate )?repos/\{owner\}/\{repo\}/[^ ]+$)' "$LOG")
if [ -n "$bad" ]; then
  fail "read-only — the script made a gh call other than 'pr list' or a GET 'api': $bad"
else
  echo "  PASS: all $(wc -l <"$LOG" | tr -d ' ') gh calls were 'pr list' or GET 'api'"
fi
# Statically: the only line that executes gh is gh_get's own `gh "$@"`. A new
# `gh pr merge`, `gh issue comment`, `gh label …` anywhere else is red.
direct=$(grep -nE '(^|[^A-Za-z0-9_./-])gh[[:space:]]+(pr|api|issue|label|repo|release|run|workflow|secret|auth|gist|search|ruleset|cache|variable)([[:space:]]|$)' "$SCRIPT" |
  grep -vE '^[0-9]+:[[:space:]]*#')
[ -z "$direct" ] || fail "read-only — the script runs gh outside gh_get: $direct"
n_exec=$(grep -cE '^[[:space:]]*gh "\$@"' "$SCRIPT")
[ "$n_exec" = 1 ] || fail "read-only — expected exactly one 'gh \"\$@\"' (inside gh_get), found $n_exec"
# gh_get itself refuses a write before gh sees it. Load just that function.
fn=$(sed -n '/^gh_get() {$/,/^}$/p' "$SCRIPT")
if [ -z "$fn" ]; then
  fail 'read-only — could not extract gh_get from the script; its write guard is untested'
else
  for args in 'api -X POST repos/{owner}/{repo}/issues/1/comments' \
    'api repos/{owner}/{repo}/issues/1/labels -f labels[]=x' \
    'api --method=PATCH repos/{owner}/{repo}/issues/comments/1' \
    'pr merge 1 --squash' 'issue comment 1 --body x' 'label create needs-human'; do
    : >"$LOG"
    # shellcheck disable=SC2086,SC2034  # $args is split on purpose (an argv); TMP is read by the eval'd gh_get
    ( eval "$fn"; TMP=$WORK; PATH="$WORK/bin:$PATH" PRM_STUB_WORLD="$TD/world" PRM_STUB_LOG="$LOG" \
      gh_get "$WORK/w.json" array 'a write' $args ) >/dev/null 2>"$WORK/err"
    got=$?
    if [ "$got" -ne 2 ] || [ -s "$LOG" ]; then
      fail "read-only — gh_get did not refuse 'gh $args' before calling gh (exit $got, stub calls: $(wc -l <"$LOG" | tr -d ' '))"
    fi
  done
  echo '  PASS: gh_get refuses -X, -f, --method, pr merge, issue comment and label create without calling gh'
fi

# ── 4. Exit status: nothing actionable vs could not look ─────────────────────
run_world quiet
if [ "$GOT" -eq 1 ] && [ "$(jq '.actionable' "$WORK/out" 2>/dev/null)" = 0 ]; then
  echo '  PASS: quiet world — nothing actionable exits 1 and still prints the JSON'
else
  fail "quiet world — nothing actionable must exit 1 with actionable 0, got exit $GOT, out: $(head -c 200 "$WORK/out")"
fi

refuse() { # refuse <label> <world> [ENV=value ...]
  local label="$1" world="$2"
  shift 2
  run_world "$world" "$@"
  if [ "$GOT" -eq 2 ] && [ ! -s "$WORK/out" ] && grep -q '^REFUSE:' "$WORK/err"; then
    echo "  PASS: $label refuses with exit 2 and prints nothing on stdout"
  else
    fail "$label — wanted exit 2, an empty stdout and a REFUSE line; got exit $GOT, stdout $(wc -c <"$WORK/out" | tr -d ' ') bytes"
  fi
}
# Only the two global reads refuse. A per-PR read failure is a hold (rows
# #145-#148); #147 is the case that makes gh's exit status load-bearing, since
# gh's HTTP-error body is a valid JSON object, the type the tree call asks for.
refuse 'gh failing'                    world PRM_STUB_FAIL=1
refuse 'an HTTP error on the collaborator list' world PRM_STUB_FAIL_ON=collaborators
refuse 'non-JSON from pr list'         world 'PRM_STUB_GARBAGE_ON=pr list'
refuse 'an empty collaborator list'    nocollab

# Non-JSON on one PR's read holds that PR, never the run. One PR of the same
# world is enough: rows #145-#148 already show the rest of a run continuing.
run_world world PRM_STUB_ONLY_PR=104 PRM_STUB_GARBAGE_ON=pulls/104/files
r104=$(jq -c 'if (.prs | length) == 1 then .prs[0] | [.decision, .reasons] else "wrong PR count" end' "$WORK/out" 2>/dev/null)
if [ "$GOT" -eq 0 ] && [ "$r104" = '["hold",["unreadable:files"]]' ]; then
  echo '  PASS: non-JSON on one PR'"'"'s files holds that PR and the run continues'
else
  fail "non-JSON on one PR's files — wanted exit 0 and #104 held unreadable:files, got exit $GOT and $r104"
fi

# shellcheck source=tools/lib/checker-assert.sh
. tools/lib/checker-assert.sh
run_script() { PATH="$WORK/bin:$PATH" PRM_STUB_WORLD="$TD/world" bash "$SCRIPT" "$@"; }
assert_checker_rc run_script 'an unknown argument refuses' 2 'unknown argument' --frobnicate

# ── 5. Vacuity guard — the real repository, through the real gh ─────────────
# Every case above passes against payloads shaped like gh's. This one checks
# that the real gh still answers in those shapes. Read-only, like the script.
# The mutation fixture sets PRM_TEST_OFFLINE=1: it re-runs this file once per
# mutation, and the live check says nothing about a mutated rule.
if [ -n "${PRM_TEST_OFFLINE:-}" ]; then
  echo '  SKIP: PRM_TEST_OFFLINE is set; the stub cases still ran'
elif gh api 'repos/{owner}/{repo}' --jq .full_name >/dev/null 2>&1; then
  bash "$SCRIPT" >"$WORK/real.json" 2>"$WORK/real.err"
  got=$?
  if { [ "$got" -eq 0 ] || [ "$got" -eq 1 ]; } && jq -e '.prs | type == "array"' "$WORK/real.json" >/dev/null 2>&1; then
    echo "  PASS: the real repository classifies cleanly (exit $got, $(jq '.prs | length' "$WORK/real.json") open PRs)"
  else
    fail "the real repository did not classify (exit $got): $(head -c 300 "$WORK/real.err")"
  fi
else
  echo '  SKIP: gh cannot reach this repository (no auth or no network); the stub cases still ran'
fi

if [ "$rc" -eq 0 ]; then
  echo 'OK: pr-merge-candidates_test — every row decides as pinned, nothing leaks, nothing writes, and could-not-look is exit 2'
fi
exit "$rc"
