#!/usr/bin/env bash
# A stand-in for gh(1), used by tools/lib/pr-exists_test.sh and
# tools/lib/orphan-branch-lint_test.sh. It answers only
# `gh pr list --state all [--head <branch>] [--limit N] --json <fields>`.
#
# Its PR table is one PR per line: `<head>` for a PR whose head lives in this
# repository, `<head> fork` for one opened from a fork (isCrossRepository
# true). `--head` matches the head name only, whoever owns it — which is what
# real gh does (#2029 review: `--head fix/ghostty-tab-focus` returns a fork's
# PR on this repo). `--limit N` truncates the answer to N rows, as real gh
# does. The default table carries the
# prefix pair the #2029 triage asked for: `fix/x-2` has a PR and `fix/x` does
# not. Any check that matches a head by substring or prefix — the unfiltered
# `gh pr list | grep fix/x` idiom — reads `fix/x` as covered. The exact-head
# answer must say it is not.
#
# Knobs, all environment variables:
#   PR_EXISTS_STUB_HEADS=<file>    use this table instead of the default one
#   PR_EXISTS_STUB_FAIL=1          exit 1 with an error, as gh does on an auth
#                                  or network failure
#   PR_EXISTS_STUB_FAIL_ON=<head>  fail like PR_EXISTS_STUB_FAIL, but only for
#                                  this one head — a failure in the middle of a
#                                  loop over many branches
#   PR_EXISTS_STUB_GARBAGE=1       exit 0 but print something that is not JSON
#   PR_EXISTS_STUB_LOG=<file>      append each queried head to this file, so a
#                                  test can prove the stub was actually asked

set -uo pipefail

DEFAULT_TABLE='fix/x-2
feat/1999-other-work
fix/forked fork'

if [ "${1:-}" != "pr" ] || [ "${2:-}" != "list" ]; then
  echo "gh-stub: unexpected command '$*' — the tests only drive 'gh pr list'" >&2
  exit 64
fi
shift 2

head=""
have_head=0
limit=30 # gh's own default
while [ "$#" -gt 0 ]; do
  case "$1" in
    --head) head="${2:-}"; have_head=1; shift 2 ;;
    --limit) limit="${2:-}"; shift 2 ;;
    --state | --json | --repo) shift 2 ;;
    *) shift ;;
  esac
done

if [ -n "${PR_EXISTS_STUB_LOG:-}" ]; then
  printf '%s\n' "$head" >>"$PR_EXISTS_STUB_LOG"
fi

if [ "${PR_EXISTS_STUB_FAIL:-}" = "1" ] ||
  { [ -n "${PR_EXISTS_STUB_FAIL_ON:-}" ] && [ "$head" = "$PR_EXISTS_STUB_FAIL_ON" ]; }; then
  echo "error connecting to api.github.com" >&2
  exit 1
fi

if [ "${PR_EXISTS_STUB_GARBAGE:-}" = "1" ]; then
  echo "<html><body>502 Bad Gateway</body></html>"
  exit 0
fi

if [ -n "${PR_EXISTS_STUB_HEADS:-}" ]; then
  table=$(cat "$PR_EXISTS_STUB_HEADS") || { echo "gh-stub: cannot read $PR_EXISTS_STUB_HEADS" >&2; exit 64; }
else
  table=$DEFAULT_TABLE
fi

# Emit a JSON array of {number, headRefName, isCrossRepository}; numbers are
# line numbers.
printf '%s\n' "$table" | awk -v want="$head" -v filter="$have_head" -v limit="$limit" '
  BEGIN { printf "["; n = 0 }
  NF == 0 { next }
  filter == 1 && $1 != want { next }
  n >= limit + 0 { next }
  {
    cross = ($2 == "fork") ? "true" : "false"
    if (n++) printf ","
    printf "{\"number\":%d,\"headRefName\":\"%s\",\"isCrossRepository\":%s}", NR, $1, cross
  }
  END { print "]" }
'
