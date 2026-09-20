#!/usr/bin/env bash
# A stand-in for git(1), used by tools/lib/ref-exists_test.sh.
#
# Its ref table carries TWO rows that a search for the digits 2005 matches
# while no branch for ticket 2005 exists. They are different defects and are
# asserted separately in tools/lib/ref-exists_test.sh:
#
#   Row 1 — an object id whose last four characters are "2005". This is the
#   shape that fooled the wave-1 orchestrator on 2026-09-20 through
#   `ls-remote | grep 2005`. Measured while writing this: `grep -w 2005` does
#   NOT match it, because the preceding character is the hex digit "e" and a
#   hex digit is a word character. A four-digit run inside a 40-hex id can
#   never have word boundaries on both sides, so `-w` is safe against object
#   ids specifically — which is why ir:exec section 2's `grep -w "<N>"` needs
#   no repair.
#
#   Row 2 — a branch belonging to ticket 1999 whose NAME carries "2005"
#   between two hyphens. Hyphens are not word characters, so `grep -w 2005`
#   matches this one. It is the case that shows a word boundary is not a
#   substitute for asking about an exact ref.
#
# Set REF_EXISTS_STUB_UNREACHABLE=1 to make it exit 128, the status real git
# returns when it cannot reach the remote at all.

set -uo pipefail

TABLE='4f1c9e2a7b3d5f8e0a1c2d3e4f5a6b7c8d9e2005	refs/heads/feat/1999-other-work
c0ffee11223344556677889900aabbccddeeff01	refs/heads/feat/1999-backport-2005-fix
b71d0c4e5a6f8b9c0d1e2f3a4b5c6d7e8f901234	refs/heads/main'

if [ "${REF_EXISTS_STUB_UNREACHABLE:-}" = "1" ]; then
  echo "fatal: could not read from remote repository" >&2
  exit 128
fi

# Skip leading `-c key=value` configuration options. ref-exists.sh bounds its
# round trip with `git -c http.lowSpeedLimit=... -c http.lowSpeedTime=...`, and
# a stub that refused them reported git exit 128 — "could not look" — for every
# branch, which is the stub inventing a failure rather than modelling one.
while [ "${1:-}" = "-c" ]; do
  shift 2 || break
done

if [ "${1:-}" != "ls-remote" ]; then
  echo "git-stub: unexpected subcommand '${1:-}' — the test only drives ls-remote" >&2
  exit 128
fi
shift

exit_code=0
pattern=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --exit-code) exit_code=1 ;;
    --heads) ;;
    -*) ;;
    *) pattern="$1" ;;
  esac
  shift
done

# The remote name lands in $pattern first and the ref pattern second, so the
# last non-flag argument wins. With no pattern at all, print the whole table —
# that is the form the broken `ls-remote | grep` idiom used.
if [ -z "$pattern" ] || [ "$pattern" = "origin" ]; then
  printf '%s\n' "$TABLE"
  exit 0
fi

hit=$(printf '%s\n' "$TABLE" | awk -v want="$pattern" '$2 == want { print }')
if [ -n "$hit" ]; then
  printf '%s\n' "$hit"
  exit 0
fi

if [ "$exit_code" = "1" ]; then
  exit 2
fi
exit 0
