#!/usr/bin/env bash
# orphan-branch-lint.sh — name every branch on the remote that carries work
# not in main and has never had a pull request (#2029).
#
# WHY THIS EXISTS. A pushed branch with no PR is invisible: the work is on the
# server, it is not in main, and nothing lists it. On 2026-09-22 two were
# found by hand — `feat/1887-desktop-driver-stability` (43 commits ahead, 16
# days old, its issue already closed) and `fix/dsh-watch-deadline-reconcile`
# (the only fix for a red main, unseen for about nine hours). ir:exec now
# asserts a PR exists before it hands back (tools/lib/pr-exists.sh), but a
# session that follows no skill is not covered by that. This lint is the
# backstop: .github/workflows/orphan-branches.yml runs it daily, and a red run
# in the Actions tab is the signal.
#
# WHAT IT DOES
#   1. `git fetch --prune <remote>`, so a branch deleted upstream is not named.
#   2. List every PR once — `gh pr list --state all --limit $LIST_LIMIT
#      --json headRefName,isCrossRepository` — and keep the heads of PRs whose
#      head lives in this repository. gh pages through the whole list up to
#      --limit: on 2026-09-24 `--limit 5000` returned 1217 rows, equal to the
#      GraphQL `pullRequests.totalCount` (1217), in about 8s. An answer with
#      as many rows as the limit may have been cut off, so it is REFUSED
#      rather than read as complete. Fork PRs are dropped because gh matches a
#      head by name only, and a fork's same-named branch says nothing about
#      this repository's (live: `--head fix/ghostty-tab-focus` returns fork
#      PR #1470, and origin has no such branch).
#   3. For every refs/remotes/<remote>/* except HEAD and main that has at least
#      one commit not in <remote>/main, look its name up in that set.
#   4. A branch with no PR whose tip commit is older than --max-age-hours
#      (default 24) is a FAIL. A younger one is printed as INFO — it may be a
#      push whose PR is seconds away.
#
# COST. One paginated listing (about 13 GraphQL pages of 100 for 1217 PRs),
# plus one local `git rev-list` per remote branch. The first version asked
# tools/lib/pr-exists.sh once per ahead-of-main branch instead — 532 of 533
# remote branches on 2026-09-24, since squash-merged branches stay "ahead" —
# and took 371s; review of #2029 showed the premise behind that choice (a
# truncated listing) was a misread `--limit`. This version's full local run
# on 2026-09-24 took 28s and named the same single branch.
#
# WAIVERS. tools/orphan-branch-lint.waivers lists branches that are knowingly
# PR-less, one `<branch> <reason>` per line. A waiver is REFUSED without a
# reason, and a waiver naming a branch that is no longer an ahead-of-main
# branch on the remote FAILS: a rotted waiver and a clean run must not read
# the same. The lint names branches; it never decides what happens to them.
#
# Usage:
#   tools/orphan-branch-lint.sh [--max-age-hours N] [--remote R] [--waivers F]
#
# Exit codes:
#   0  every ahead-of-main branch older than the threshold has a PR or a live
#      waiver, and every waiver is live
#   1  at least one orphan older than the threshold, or a stale waiver
#   2  REFUSAL — could not look: bad arguments, an unreadable waiver file, a
#      failed fetch, no <remote>/main, a git error, gh failing (auth, network,
#      rate limit), an answer that is not a JSON array, or a listing that may
#      have been truncated. "No orphans among the PRs I could list" is not "no
#      orphans" (AGENTS.md: "A verification mechanism must fail loudly when it
#      cannot run"), so the OK line prints only after a complete listing.
set -uo pipefail

SELF_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# The listing's own cap. An answer this long may have been cut off and is
# refused. Overridable so the test can drive the truncation refusal.
LIST_LIMIT=${ORPHAN_BRANCH_LINT_LIST_LIMIT:-100000}
MAX_AGE_HOURS=24
REMOTE=origin
WAIVERS="$SELF_DIR/orphan-branch-lint.waivers"

refuse() {
  echo "REFUSE: orphan-branch-lint — $*" >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --max-age-hours) MAX_AGE_HOURS="${2:-}"; shift 2 || refuse "--max-age-hours needs a value" ;;
    --remote) REMOTE="${2:-}"; shift 2 || refuse "--remote needs a value" ;;
    --waivers) WAIVERS="${2:-}"; shift 2 || refuse "--waivers needs a value" ;;
    -h | --help) sed -n '2,/^set -uo pipefail$/p' "${BASH_SOURCE[0]}" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) refuse "unknown argument '$1'" ;;
  esac
done

case "$LIST_LIMIT" in
  '' | *[!0-9]* | 0*) refuse "ORPHAN_BRANCH_LINT_LIST_LIMIT must be a positive whole number, got '$LIST_LIMIT'" ;;
esac
case "$MAX_AGE_HOURS" in
  '' | *[!0-9]*) refuse "--max-age-hours must be a whole number of hours, got '$MAX_AGE_HOURS'" ;;
esac
[ -n "$REMOTE" ] || refuse "--remote is empty"
[ -r "$WAIVERS" ] || refuse "cannot read the waiver file $WAIVERS"

# ── Waivers ─────────────────────────────────────────────────────────────────
# Newline-separated branch names; a name with no reason is refused rather than
# silently accepted, because a waiver nobody explained cannot be re-judged.
waived=""
while IFS= read -r line || [ -n "$line" ]; do
  line=${line%%#*}
  # Deliberate word split into name + reason, with globbing off so a `*` in a
  # reason cannot expand into file names.
  set -f
  # shellcheck disable=SC2086
  set -- $line
  set +f
  [ "$#" -eq 0 ] && continue
  [ "$#" -ge 2 ] || refuse "waiver '$1' in $WAIVERS has no reason"
  waived="$waived$1"$'\n'
done <"$WAIVERS"

# Every set lookup reads a here-string, never `printf | grep -q`: under
# pipefail, grep -q exiting on an early match SIGPIPEs the printf and the
# pipeline reports 141 — a "no". Measured in #2029: with the 1218-PR listing
# the piped form named 10 branches that all have PRs (feat/1798-error-state,
# PR #1809, among them); tools/lib/orphan-branch-lint_test.sh row 1b repeats
# it with 20001 heads.
is_waived() { grep -qxF -- "$1" <<<"$waived"; }

# ── Enumerate ───────────────────────────────────────────────────────────────
git fetch --prune --quiet "$REMOTE" || refuse "git fetch --prune $REMOTE failed"
base="refs/remotes/$REMOTE/main"
git rev-parse --verify --quiet "$base" >/dev/null || refuse "$base does not exist; nothing to compare against"

branches=$(git for-each-ref --format='%(refname)' "refs/remotes/$REMOTE/") ||
  refuse "git for-each-ref over refs/remotes/$REMOTE/ failed"

# ── Every PR head in this repository, once ──────────────────────────────────
listing=$(gh pr list --state all --limit "$LIST_LIMIT" --json headRefName,isCrossRepository 2>&1) ||
  refuse "gh pr list failed, so no branch can be judged: $listing"
rows=$(printf '%s' "$listing" | jq -e 'if type == "array" then length else error("not an array") end' 2>/dev/null) ||
  refuse "gh pr list answered with something that is not a JSON array: $(printf '%s' "$listing" | head -c 300)"
[ "$rows" -lt "$LIST_LIMIT" ] ||
  refuse "gh pr list returned $rows rows, the whole --limit $LIST_LIMIT; the listing may be truncated, so a missing head proves nothing"
pr_heads=$(printf '%s' "$listing" | jq -r '.[] | select(.isCrossRepository == false) | .headRefName') ||
  refuse "could not extract PR heads from the listing"

has_pr() { grep -qxF -- "$1" <<<"$pr_heads"; }

now=$(date +%s)
# 10#: a leading zero is decimal, not octal ("08" would otherwise abort the
# arithmetic with exit 1, and "010" would mean 8 hours).
max_age_s=$((10#$MAX_AGE_HOURS * 3600))
checked=0
live_waivers=""
orphans=""
fresh=""

while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  short=${ref#"refs/remotes/$REMOTE/"}
  case "$short" in HEAD | main) continue ;; esac

  ahead=$(git rev-list --count "$base..$ref") || refuse "git rev-list $base..$ref failed"
  [ "$ahead" -gt 0 ] || continue

  if is_waived "$short"; then
    live_waivers="$live_waivers$short"$'\n'
    continue
  fi

  checked=$((checked + 1))
  has_pr "$short" && continue

  tip=$(git log -1 --format='%ct' "$ref") || refuse "git log $ref failed"
  day=$(git log -1 --format='%cs' "$ref")
  row="$day  ahead=$ahead  $short"
  if [ $((now - tip)) -ge "$max_age_s" ]; then
    orphans="$orphans$row"$'\n'
  else
    fresh="$fresh$row"$'\n'
  fi
done <<<"$branches"

# ── Verdict ─────────────────────────────────────────────────────────────────
rc=0
if [ -n "$fresh" ]; then
  echo "INFO: orphan-branch-lint — no PR yet, but younger than ${MAX_AGE_HOURS}h:"
  printf '%s' "$fresh" | sed 's/^/  /'
fi

if [ -n "$orphans" ]; then
  echo "FAIL: orphan-branch-lint — $(printf '%s' "$orphans" | grep -c .) branch(es) on $REMOTE carry work not in main and have never had a PR (older than ${MAX_AGE_HOURS}h):" >&2
  printf '%s' "$orphans" | sed 's/^/  /' >&2
  echo "  Open a PR, delete the branch, or record it in $WAIVERS with a reason." >&2
  rc=1
fi

stale=""
while IFS= read -r w; do
  [ -n "$w" ] || continue
  grep -qxF -- "$w" <<<"$live_waivers" || stale="$stale$w"$'\n'
done <<<"$waived"
if [ -n "$stale" ]; then
  echo "FAIL: orphan-branch-lint — waiver(s) in $WAIVERS name no ahead-of-main branch on $REMOTE any more:" >&2
  printf '%s' "$stale" | sed 's/^/  /' >&2
  echo "  Remove them. A waiver that stopped matching and a clean run must not look the same." >&2
  rc=1
fi

if [ "$rc" -eq 0 ]; then
  echo "OK: orphan-branch-lint — checked $checked branch(es) ahead of $REMOTE/main against $rows PR(s); none older than ${MAX_AGE_HOURS}h is without a PR"
fi
exit "$rc"
