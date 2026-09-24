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
#   2. For every refs/remotes/<remote>/* except HEAD and main that has at least
#      one commit not in <remote>/main, ask tools/lib/pr-exists.sh whether a
#      PR (any state) ever had that head. One query per branch rather than one
#      listing of all PRs: an unfiltered `gh pr list --limit 1000` already
#      returns 1000 rows in this repo (checked 2026-09-24 during #2029's
#      triage), so a single page is truncated, and a branch whose PR fell off
#      the page would be named falsely — or, worse, a paging bug would hide
#      one. The per-branch query has no cap to get wrong.
#   3. A branch with no PR whose tip commit is older than --max-age-hours
#      (default 24) is a FAIL. A younger one is printed as INFO — it may be a
#      push whose PR is seconds away.
#
# COST. One gh round trip per branch ahead of main. Squash-merged branches
# stay "ahead" forever, so that is most of them: 532 of 533 remote branches on
# 2026-09-24 (counted with the for-each-ref / rev-list loop in #2029). One
# full local run that day took 371s wall clock — minutes, not seconds, which
# is why this is a scheduled job and not a push hook. Deleting merged branches
# shrinks it.
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
#      failed fetch, no <remote>/main, a git error, or ANY pr-exists refusal
#      (gh auth, network, rate limit, unparsable answer). One unanswerable
#      branch aborts the whole run, because "no orphans among the branches I
#      could ask about" is not "no orphans" (AGENTS.md: "A verification
#      mechanism must fail loudly when it cannot run"). The OK line is printed
#      only after every branch was answered.
set -uo pipefail

SELF_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/lib/pr-exists.sh
. "$SELF_DIR/lib/pr-exists.sh" # defines pr_exists

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

is_waived() { printf '%s' "$waived" | grep -qxF -- "$1"; }

# ── Enumerate ───────────────────────────────────────────────────────────────
git fetch --prune --quiet "$REMOTE" || refuse "git fetch --prune $REMOTE failed"
base="refs/remotes/$REMOTE/main"
git rev-parse --verify --quiet "$base" >/dev/null || refuse "$base does not exist; nothing to compare against"

branches=$(git for-each-ref --format='%(refname)' "refs/remotes/$REMOTE/") ||
  refuse "git for-each-ref over refs/remotes/$REMOTE/ failed"

now=$(date +%s)
max_age_s=$((MAX_AGE_HOURS * 3600))
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

  out=$(pr_exists "$short" 2>&1)
  case $? in
    0) checked=$((checked + 1)); continue ;;
    1) checked=$((checked + 1)) ;;
    *) refuse "could not ask whether '$short' has a PR, so no verdict is possible: $out" ;;
  esac

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
  printf '%s' "$live_waivers" | grep -qxF -- "$w" || stale="$stale$w"$'\n'
done <<<"$waived"
if [ -n "$stale" ]; then
  echo "FAIL: orphan-branch-lint — waiver(s) in $WAIVERS name no ahead-of-main branch on $REMOTE any more:" >&2
  printf '%s' "$stale" | sed 's/^/  /' >&2
  echo "  Remove them. A waiver that stopped matching and a clean run must not look the same." >&2
  rc=1
fi

if [ "$rc" -eq 0 ]; then
  echo "OK: orphan-branch-lint — asked about $checked branch(es) ahead of $REMOTE/main; none older than ${MAX_AGE_HOURS}h is without a PR"
fi
exit "$rc"
