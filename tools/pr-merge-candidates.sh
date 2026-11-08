#!/usr/bin/env bash
# pr-merge-candidates.sh — classify every open pull request as `review`,
# `hold` or `skip` for the ir:pr-merge skill (#2128), deterministically and
# without an LLM.
#
# WHY THIS EXISTS. The repository is public, so everything inside a PR is
# attacker-controlled: title, body, comments, commit messages, branch name,
# diff and file names. The 2026 "Comment and Control" attacks made merge and
# review agents leak their credentials through nothing more than a PR title.
# ir:pr-merge therefore splits the decision in two. This script decides
# whether a PR MAY merge, from fields an attacker cannot talk their way
# past: draft flag, mergeability, check results, who wrote it, which paths it
# touches. The skill's LLM review can only veto a `review` PR, never promote a
# `hold` or a `skip`. Two callers use this one script: the skill's first step
# and Factory's daily gate, which starts the agent only when this exits 0.
#
# READ-ONLY BY CONSTRUCTION. Every gh call goes through gh_get, which refuses
# anything but `gh pr list` and `gh api` GETs (no -X/--method, no -f/-F/
# --field/--raw-field/--input, which would turn `gh api` into a POST). The
# script never checks out, fetches or runs PR code, and never prints a PR's
# title, body or branch name: every string it emits is an enum, a SHA, a
# number, or a path/name that passed the `safe` character filter below.
# tools/lib/pr-merge-candidates_test.sh asserts all three against a stub gh
# that logs every call.
#
# Usage:
#   tools/pr-merge-candidates.sh            # JSON on stdout
#   GH_REPO=owner/name tools/pr-merge-candidates.sh
#
# Output (stdout, only when the exit status is 0 or 1):
#   {"actionable": N,
#    "prs": [{"number", "head_sha", "author", "tier", "decision",
#             "reasons": [...], "sensitive_hits": [{"rule","path"}],
#             "marker": {"comment_id","sha","verdict"} | null}]}
#
#   tier      trusted | dependabot | external
#   decision  review  eligible for the LLM review, and may merge if it is clean
#             hold    needs a human by a deterministic rule; never merged
#             skip    nothing to do now (draft, CI not green, not CLEAN,
#                     already decided at this head SHA)
#   actionable = the number of PRs whose decision is `review` or `hold`.
#
# Exit codes:
#   0  at least one PR is actionable
#   1  nothing is actionable (the JSON is still printed)
#   2  REFUSAL — could not look: gh failed (auth, network, rate limit), gh
#      answered with something that is not the JSON shape asked for, a write
#      verb reached gh_get, or the open-PR list may be truncated. Nothing is
#      printed on stdout, so "could not look" never reads as "nothing to do"
#      (AGENTS.md: "A verification mechanism must fail loudly when it cannot
#      run").
#
# The Factory gate maps these as: 0 → `factory task run <task>`, 1 → exit 0
# without starting an agent, 2 → fail the gate so the owner sees it.
#
# One PR whose file list cannot be read in full (more files than the API
# returns, a truncated tree) is NOT a refusal: that PR alone is held, because
# one oversized PR must not stop the pass for every other PR, and holding it
# still fails closed.
#
# Requires bash (3.2 is enough: no associative arrays, no mapfile) and jq.
set -uo pipefail

# ── Rule tables ──────────────────────────────────────────────────────────────
# Named once here. tools/lib/pr-merge-candidates-mutations_test.sh anchors on
# several of these lines, so a change to one is a change to a tested rule.

# The ruleset "Protect Main" (id 15993081) requires exactly these two checks,
# read with `gh api repos/ingo-eichhorst/Irrlicht/rulesets/15993081` on
# 2026-11-08. Every OTHER check on the head SHA must also be green, but these
# two must be present at all; a PR whose required check never reported is not
# green, it is unknown.
PRM_REQUIRED_CHECKS='["go-test","build-test"]'
PRM_HOLD_LABEL='needs-human'
PRM_LIST_LIMIT=500
# GitHub's pulls/N/files API returns at most 3000 files.
PRM_MAX_FILES=3000
PRM_MAX_FILE_BYTES=1048576
PRM_PR_FIELDS='number,isDraft,headRefOid,author,isCrossRepository,mergeable,mergeStateStatus,labels,changedFiles,statusCheckRollup,title,body'

usage() {
  sed -n '2,/^set -uo pipefail$/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
}

case "${1:-}" in
  -h | --help) usage; exit 0 ;;
  '') ;;
  *) echo "REFUSE: pr-merge-candidates — unknown argument '$1' (try --help)" >&2; exit 2 ;;
esac

command -v jq >/dev/null 2>&1 || { echo "REFUSE: pr-merge-candidates — jq is not on PATH" >&2; exit 2; }
command -v gh >/dev/null 2>&1 || { echo "REFUSE: pr-merge-candidates — gh is not on PATH" >&2; exit 2; }

TMP=$(mktemp -d "${TMPDIR:-/tmp}/pr-merge-candidates.XXXXXX") ||
  { echo "REFUSE: pr-merge-candidates — cannot create a temp dir" >&2; exit 2; }
trap 'rm -rf "$TMP"' EXIT

# gh_get <outfile> <array|object> <label> <gh args...>
#
# The only place this script runs gh. It refuses a write before gh sees it,
# requires a zero exit, and requires the answer to be the JSON type asked for.
# Both checks are needed: on an HTTP error `gh api` prints the JSON error body
# on STDOUT and exits 1. Measured on gh 2.97.0 on 2026-11-08, a tree request
# for an unknown SHA printed {"message":"Invalid object requested. …",
# "status":"422"}. That is a valid JSON object, the very type the tree call
# asks for, so only the exit status tells the error apart from an answer.
# `gh api --paginate` prints one JSON array per page back to back, so an
# `array` answer is read with `jq -s 'add'`, which joins the pages; one page
# is the same as no pagination.
gh_get() {
  local outfile="$1" want="$2" label="$3" arg rc
  shift 3
  case "$1 ${2:-}" in
    'pr list' | 'api '*) ;;
    *) echo "REFUSE: pr-merge-candidates — refusing gh '$1 ${2:-}': only 'pr list' and 'api' GETs are allowed" >&2; return 2 ;;
  esac
  for arg in "$@"; do
    case "$arg" in
      -X | --method | --method=* | -X* | -f | -F | --field | --field=* | --raw-field | --raw-field=* | --input | --input=*)
        echo "REFUSE: pr-merge-candidates — refusing gh argument '$arg' while reading $label: this script never writes" >&2
        return 2
        ;;
    esac
  done
  gh "$@" >"$TMP/gh.out" 2>"$TMP/gh.err"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "REFUSE: pr-merge-candidates — could not read $label (gh exit $rc): $(head -c 400 "$TMP/gh.err")" >&2
    return 2
  fi
  if [ "$want" = array ]; then
    jq -e -s 'if length > 0 and all(.[]; type == "array") then add else error("not a JSON array") end' \
      "$TMP/gh.out" >"$outfile" 2>/dev/null
  else
    jq -e 'if type == "object" then . else error("not a JSON object") end' \
      "$TMP/gh.out" >"$outfile" 2>/dev/null
  fi
  if [ "$?" -ne 0 ]; then
    echo "REFUSE: pr-merge-candidates — gh answered with something that is not a JSON $want for $label: $(head -c 200 "$TMP/gh.out")" >&2
    return 2
  fi
  return 0
}

# ── Classification, in jq ────────────────────────────────────────────────────
# Every rule lives in this one program so the bash above stays plumbing. The
# entry points at the bottom are `cheap` (fields from `gh pr list` only),
# `marker` (the latest trusted marker comment) and `full` (everything else).
# shellcheck disable=SC2016  # $vars below are jq variables, not shell ones
JQ_LIB='
# A string we may print: a path, login, dependency or check name made only of
# these characters. Anything else (spaces in a path, bidi or zero-width code
# points, quotes, markup) prints as a placeholder, so a file name cannot carry
# text into the skill'"'"'s PR comment.
def safe: if type == "string" and test("^[A-Za-z0-9._/@+-]{1,200}$") then . else "<unusual>" end;
def safe_check: if type == "string" and test("^[A-Za-z0-9 ._/()@+:-]{1,120}$") then . else "<unusual>" end;

# Bidi controls and zero-width characters: the "Trojan Source" and hidden-
# instruction families. Matched in the title, body and every added line.
def hidden_unicode: test("[​-‍⁠﻿‪-‮⁦-⁩]");

def check_name: .name // .context // "";
# One check-run or status context → pass | pending | failed. Anything this
# does not recognise is failed, never pass.
def check_state:
  if .__typename == "StatusContext" then
    (if .state == "SUCCESS" then "pass"
     elif .state == "PENDING" or .state == "EXPECTED" then "pending"
     else "failed" end)
  else
    (if .status != "COMPLETED" then "pending"
     elif .conclusion == "SUCCESS" or .conclusion == "NEUTRAL" or .conclusion == "SKIPPED" then "pass"
     else "failed" end)
  end;
def check_success:
  (.__typename == "StatusContext" and .state == "SUCCESS")
  or (.__typename != "StatusContext" and .status == "COMPLETED" and .conclusion == "SUCCESS");

def ci_reasons($required):
  (.statusCheckRollup // []) as $checks
  | [ $required[] as $r
      | ($checks | map(select(check_name == $r))) as $m
      | if ($m | length) == 0 then "ci-missing:\($r)"
        elif ($m | all(check_state != "pass")) then empty
        elif ($m | all(check_success)) then empty
        else "ci-required-not-success:\($r)" end ]
  + [ $checks[] | select(check_state == "pending") | "ci-pending:\(check_name | safe_check)" ]
  + [ $checks[] | select(check_state == "failed") | "ci-failed:\(check_name | safe_check)" ];

def mergeable_reasons:
  if .mergeable == "MERGEABLE" and .mergeStateStatus == "CLEAN" then []
  else ["not-clean:\(.mergeable // "null" | safe)/\(.mergeStateStatus // "null" | safe)"] end;

# The tier by login alone. A dependabot CLAIM is confirmed or demoted in
# `full`, from its commits and files.
def login_tier($collab):
  (.author.login // "") as $u
  | if $u == "app/dependabot" and .author.is_bot == true then "dependabot"
    elif .author.is_bot != true and .isCrossRepository == false and ($collab | any(. == $u)) then "trusted"
    else "external" end;

def cheap($required; $collab):
  { number, head_sha: .headRefOid, author: (.author.login // "" | safe),
    tier: login_tier($collab),
    reasons: ((if .isDraft == false then [] else ["draft"] end)
              + mergeable_reasons + ci_reasons($required)) };

# The marker the skill writes. Only a push collaborator'"'"'s comment counts, so
# an outsider cannot plant a "handled" marker on their own PR.
def marker_re: "<!-- ir:pr-merge sha=(?<sha>[0-9a-f]{40}) verdict=(?<verdict>merge|needs-human) -->";
def latest_marker($collab):
  [ .[] | select((.user.login // "") as $u | $collab | any(. == $u))
        | select((.body // "") | test(marker_re))
        | ((.body | capture(marker_re)) as $c
           | {comment_id: .id, updated_at: (.updated_at // ""), sha: $c.sha, verdict: $c.verdict}) ]
  | sort_by(.updated_at) | last
  | if . == null then null else del(.updated_at) end;

# ── Sensitive paths ──
# Matched case-insensitively: on a case-insensitive checkout `.Claude/` IS
# `.claude/`. Both a file'"'"'s new and previous name are matched, so renaming a
# workflow out of `.github/` is still a hit.
def path_rules: [
  {rule: "github",             re: "^\\.github/"},
  {rule: "agent-config",       re: "^\\.(claude|pi)/"},
  {rule: "agent-instructions", re: "(^|/)(AGENTS|CLAUDE)\\.md$"},
  {rule: "tools",              re: "^tools/"},
  {rule: "install-path",       re: "^site/(install\\.sh|relay\\.sh|appcast\\.xml)$"},
  {rule: "release",            re: "^version\\.json$"},
  {rule: "git-metadata",       re: "(^|/)\\.git(attributes|modules)$"},
  {rule: "binary",             re: "\\.(exe|dll|so|dylib|a|o|bin|jar|war|class|pyc|wasm|node|zip|tar|gz|tgz|bz2|xz|7z|rar|dmg|pkg|deb|rpm|whl|apk|ipa)$"},
  {rule: "minified",           re: "\\.min\\.(js|mjs|cjs|css)$"}
];

def path_hits($p):
  if ($p | safe) == "<unusual>" then [{rule: "unusual-path", path: "<unusual>"}]
  else [ path_rules[] | select(.re as $re | $p | test($re; "i")) | {rule, path: $p} ] end;

def lines($c): [ .patch // "" | split("\n")[] | select(startswith($c)) | .[1:] ];

# go.mod / go.work: the only accepted change is a version bump of a module
# that the same patch also removes, plus `go`/`toolchain` directives. A new
# module path, a `replace`, a `use`, or any other directive is a hit.
def gomod_line:
  gsub("^\\s+|\\s+$"; "") as $s
  | if $s == "" or ($s | startswith("//")) or $s == ")" or ($s | test("^require\\s*\\($")) then {kind: "skip"}
    elif ($s | test("^(go|toolchain)\\s")) then {kind: "skip"}
    elif ($s | test("^(require\\s+)?[^\\s=]+\\s+v[0-9][^\\s]*(\\s+//\\s*indirect)?$")) then
      {kind: "dep", mod: ($s | sub("^require\\s+"; "") | split(" ")[0] | split("\t")[0])}
    else {kind: "other"} end;

def gomod_hits:
  .filename as $f
  | [ lines("+")[] | gomod_line ] as $add
  | ([ lines("-")[] | gomod_line | select(.kind == "dep") | .mod ]) as $removed
  | [ $add[] | select(.kind == "dep") | .mod | select(. as $m | $removed | any(. == $m) | not)
      | {rule: "new-dependency", path: "\($f | safe):\(. | safe)"} ]
  + (if any($add[]; .kind == "other") then [{rule: "go-mod-directive", path: ($f | safe)}] else [] end);

# package.json: the only accepted change is a key whose line the same patch
# also removes, with a version-shaped new value. A new key (a new package, a
# new script), a script whose command changed, or a structural line is a hit.
def pkg_line:
  gsub("^\\s+|\\s*,?\\s*$"; "") as $s
  | if $s == "" then {kind: "skip"}
    elif ($s | test("^\"[^\"]+\"\\s*:\\s*\"[^\"]*\"$")) then
      ($s | capture("^\"(?<k>[^\"]+)\"\\s*:\\s*\"(?<v>[^\"]*)\"$")) as $c | {kind: "kv", key: $c.k, val: $c.v}
    else {kind: "other"} end;

def pkg_hits:
  .filename as $f
  | [ lines("+")[] | pkg_line ] as $add
  | ([ lines("-")[] | pkg_line | select(.kind == "kv") | .key ]) as $removed_keys
  | [ $add[] | select(.kind == "kv")
      | if (.key as $k | $removed_keys | any(. == $k) | not) then {rule: "package-json-new-key", path: "\($f | safe):\(.key | safe)"}
        elif (.val | test("^[~^]?(>=|<=|>|<|=)?v?[0-9][0-9A-Za-z.+-]*$") | not) then {rule: "package-json-value", path: "\($f | safe):\(.key | safe)"}
        else empty end ]
  + (if any($add[]; .kind == "other") then [{rule: "package-json-structure", path: ($f | safe)}] else [] end);

def content_hits($tree; $max_bytes):
  .filename as $f
  | (if (.patch == null) and .status != "removed" and ((.status == "renamed" and .changes == 0) | not)
       then [{rule: "opaque", path: ($f | safe)}] else [] end)
  + (if .status == "removed" then []
     else ([ $tree.tree[]? | select(.path == $f) ] | first) as $blob
       | if $blob == null then [{rule: "not-in-tree", path: ($f | safe)}]
         elif ($blob.size // 0) > $max_bytes then [{rule: "large-file", path: ($f | safe)}]
         else [] end end)
  + (if any(lines("+")[]; test("[A-Za-z0-9+/_-]{200,}")) then [{rule: "base64-blob", path: ($f | safe)}] else [] end)
  + (if any(lines("+")[]; hidden_unicode) then [{rule: "hidden-unicode", path: ($f | safe)}] else [] end)
  + (if ($f | test("(^|/)go\\.(mod|work)$")) then gomod_hits else [] end)
  + (if ($f | test("(^|/)package\\.json$")) then pkg_hits else [] end);

# ── dependabot ──
def manifest_re: "(^|/)(go\\.mod|go\\.sum|go\\.work\\.sum|package\\.json|package-lock\\.json)$";

# dependabot'"'"'s own metadata block in each commit message:
#   updated-dependencies:
#   - dependency-name: X
#     update-type: version-update:semver-minor
#   ...
def updated_deps:
  reduce (split("\n")[]) as $line ({in: false, cur: null, out: []};
    if (.in | not) then (if $line == "updated-dependencies:" then .in = true else . end)
    elif $line == "..." then .in = false
    elif ($line | startswith("- dependency-name: ")) then
      (if .cur != null then .out += [.cur] else . end)
      | .cur = {name: ($line | ltrimstr("- dependency-name: "))}
    elif ($line | startswith("  update-type: ")) and .cur != null then
      .cur.update = ($line | ltrimstr("  update-type: "))
    else . end)
  | .out + (if .cur != null then [.cur] else [] end);

def lead_major: ltrimstr("v") | capture("^(?<m>[0-9]+)").m | tonumber;
def first_from_to:
  [ match("from (?<a>v?[0-9]+(\\.[0-9A-Za-z+-]+)*) to (?<b>v?[0-9]+(\\.[0-9A-Za-z+-]+)*)"; "g")
    | {a: .captures[0].string, b: .captures[2].string} ] | first;

# A major bump, or a bump this cannot classify, is a hold. update-type is the
# primary signal; an entry without one (dependabot omits it for indirect
# bumps, measured on #2121) falls back to the "from A to B" line, but only in
# a single-dependency commit, where that line cannot belong to another entry.
def major_reasons:
  [ .[] | (.commit.message // "") as $msg | ($msg | updated_deps) as $deps
    | if ($deps | length) == 0 then "dependabot-metadata-missing"
      else $deps[] as $d
        | if $d.update == "version-update:semver-major" then "dependabot-major:\($d.name | safe)"
          elif (($d.update // "") | test("^version-update:semver-(minor|patch)$")) then empty
          elif ($deps | length) == 1 then
            ($msg | first_from_to) as $ft
            | (if $ft == null then null else (try (($ft.a | lead_major) != ($ft.b | lead_major)) catch null) end) as $changed
            | if $changed == null then "dependabot-major-unknown:\($d.name | safe)"
              elif $changed then "dependabot-major:\($d.name | safe)"
              else empty end
          else "dependabot-major-unknown:\($d.name | safe)" end
      end ]
  | unique;

def dependabot_reasons($commits; $files):
  ((if ($commits | length) == 0 then ["dependabot-no-commits"] else [] end)
   + [ $commits[] | select((.author.login // "") != "dependabot[bot]") | "dependabot-foreign-commit" ]
   + [ $commits[] | select(.commit.verification.verified != true) | "dependabot-unverified-commit" ]
   + [ $files[] | select(.filename | test(manifest_re) | not) | "dependabot-non-manifest-file" ])
  | unique;

def full($base; $collab; $hold_label; $commits; $files; $tree; $max_files; $max_bytes):
  . as $pr
  | ($base.tier) as $claimed
  | (if $claimed == "dependabot" then dependabot_reasons($commits; $files) else [] end) as $dep_problems
  | (if $claimed == "dependabot" and ($dep_problems | length) == 0 then "dependabot"
     elif $claimed == "dependabot" then "external"
     else $claimed end) as $tier
  | ([ $files[] | path_hits(.filename), (if .previous_filename then path_hits(.previous_filename) else [] end), content_hits($tree; $max_bytes) ]
     | add // [] | unique) as $hits
  | ( (if $tier == "external" then ["external-author"] else [] end)
      + $dep_problems
      + (if $tier == "dependabot" then ($commits | major_reasons) else [] end)
      + (if ($pr.labels // []) | any(.name == $hold_label) then ["needs-human-label"] else [] end)
      + (if ($pr.changedFiles // 0) > $max_files then ["too-many-files"]
         elif ($files | length) != ($pr.changedFiles // -1) then ["file-list-incomplete"] else [] end)
      # `//` would read `false` as missing, so compare to false explicitly:
      # only a tree that says truncated == false is a complete one.
      + (if $tree.truncated != false then ["tree-truncated"] else [] end)
      + (if (($pr.title // "") + "\n" + ($pr.body // "")) | hidden_unicode then ["hidden-unicode-in-description"] else [] end)
      + [ $hits[] | "sensitive:\(.rule)" ] | unique ) as $hold
  | $base + {tier: $tier,
             decision: (if ($hold | length) > 0 then "hold" else "review" end),
             reasons: $hold,
             sensitive_hits: $hits};
'

jq -n "$JQ_LIB"' 1' >/dev/null 2>&1 || {
  echo "REFUSE: pr-merge-candidates — the embedded jq program does not compile ($(jq -n "$JQ_LIB"' 1' 2>&1 | head -c 300))" >&2
  exit 2
}

# ── Gather and classify ──────────────────────────────────────────────────────
# `{owner}/{repo}` is resolved by gh from the checkout or GH_REPO.
gh_get "$TMP/collab.raw" array "push collaborators" \
  api --paginate 'repos/{owner}/{repo}/collaborators?permission=push&per_page=100' || exit 2
jq -c '[.[] | .login // empty]' "$TMP/collab.raw" >"$TMP/collab.json" || exit 2
if [ "$(jq length "$TMP/collab.json")" -eq 0 ]; then
  # Every PR would read as external, which fails closed but would also make a
  # silent API change look like "nobody is trusted". Say it instead.
  echo "REFUSE: pr-merge-candidates — the push-collaborator list came back empty" >&2
  exit 2
fi

gh_get "$TMP/prs.json" array "open pull requests" \
  pr list --state open --limit "$PRM_LIST_LIMIT" --json "$PRM_PR_FIELDS" || exit 2
if [ "$(jq length "$TMP/prs.json")" -ge "$PRM_LIST_LIMIT" ]; then
  echo "REFUSE: pr-merge-candidates — $PRM_LIST_LIMIT or more open PRs; the list may be truncated" >&2
  exit 2
fi

: >"$TMP/results.jsonl"
for num in $(jq -r '.[].number' "$TMP/prs.json"); do
  jq --argjson n "$num" '.[] | select(.number == $n)' "$TMP/prs.json" >"$TMP/pr.json"

  jq -c --argjson required "$PRM_REQUIRED_CHECKS" --slurpfile collab "$TMP/collab.json" \
    "$JQ_LIB"' cheap($required; $collab[0])' "$TMP/pr.json" >"$TMP/base.json" || exit 2

  if [ "$(jq '.reasons | length' "$TMP/base.json")" -gt 0 ]; then
    jq -c '. + {decision: "skip", sensitive_hits: [], marker: null}' "$TMP/base.json" >>"$TMP/results.jsonl"
    continue
  fi

  gh_get "$TMP/comments.json" array "comments on #$num" \
    api --paginate "repos/{owner}/{repo}/issues/$num/comments?per_page=100" || exit 2
  jq -c --slurpfile collab "$TMP/collab.json" \
    "$JQ_LIB"' latest_marker($collab[0])' "$TMP/comments.json" >"$TMP/marker.json" || exit 2

  head_sha=$(jq -r '.head_sha' "$TMP/base.json")
  if [ "$(jq -r '.sha // ""' "$TMP/marker.json")" = "$head_sha" ]; then
    jq -c --slurpfile m "$TMP/marker.json" \
      '. + {decision: "skip", reasons: ["handled:\(.head_sha)"], sensitive_hits: [], marker: $m[0]}' \
      "$TMP/base.json" >>"$TMP/results.jsonl"
    continue
  fi

  gh_get "$TMP/files.json" array "files of #$num" \
    api --paginate "repos/{owner}/{repo}/pulls/$num/files?per_page=100" || exit 2
  gh_get "$TMP/tree.json" object "tree of #$num at $head_sha" \
    api "repos/{owner}/{repo}/git/trees/$head_sha?recursive=1" || exit 2
  if [ "$(jq -r '.tier' "$TMP/base.json")" = dependabot ]; then
    gh_get "$TMP/commits.json" array "commits of #$num" \
      api --paginate "repos/{owner}/{repo}/pulls/$num/commits?per_page=100" || exit 2
  else
    echo '[]' >"$TMP/commits.json"
  fi

  jq -c --slurpfile base "$TMP/base.json" --slurpfile collab "$TMP/collab.json" \
    --slurpfile commits "$TMP/commits.json" --slurpfile files "$TMP/files.json" \
    --slurpfile tree "$TMP/tree.json" --slurpfile marker "$TMP/marker.json" \
    --arg hold_label "$PRM_HOLD_LABEL" \
    --argjson max_files "$PRM_MAX_FILES" --argjson max_bytes "$PRM_MAX_FILE_BYTES" \
    "$JQ_LIB"' full($base[0]; $collab[0]; $hold_label; $commits[0]; $files[0]; $tree[0]; $max_files; $max_bytes) + {marker: $marker[0]}' \
    "$TMP/pr.json" >>"$TMP/results.jsonl" || exit 2
done

jq -s '{actionable: (map(select(.decision != "skip")) | length), prs: .}' "$TMP/results.jsonl" >"$TMP/out.json" || exit 2
cat "$TMP/out.json"
if [ "$(jq '.actionable' "$TMP/out.json")" -gt 0 ]; then
  exit 0
fi
exit 1
