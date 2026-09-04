#!/usr/bin/env bash
# Keep ir:exec's simplify report complete and loud on missing results.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || exit 1

SKILL_FILE=.claude/skills/ir:exec/SKILL.md
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

[ -f "$SKILL_FILE" ] || { echo "FAIL: $SKILL_FILE not found" >&2; exit 1; }

review_section=$(awk '
  /^## 6\. Open and review the PR$/ { armed = 1 }
  armed && /^## 7\./ { exit }
  armed { print }
' "$SKILL_FILE")

if [ -z "$review_section" ]; then
  echo "FAIL: could not find the Open and review the PR section. Cannot verify anything." >&2
  exit 1
fi

review_joined=$(printf '%s' "$review_section" | tr '\n' ' ')
grep -qF 'reuse, simplification, efficiency, altitude' <<<"$review_joined" ||
  fail 'review section no longer enumerates all four angles'
grep -qF 'surface it and pause' <<<"$review_joined" ||
  fail "review section no longer pairs the four-angle check with a 'surface it and pause' instruction"

if [ "$rc" -eq 0 ]; then
  echo "OK: simplify-angle-guard_test — all angles and loud failure present"
fi
exit "$rc"
