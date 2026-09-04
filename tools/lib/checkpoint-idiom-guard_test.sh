#!/usr/bin/env bash
# Keep ir:exec's checkpoint instructions complete and scoped to verification.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || exit 1

SKILL_FILE=.claude/skills/ir:exec/SKILL.md
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

[ -f "$SKILL_FILE" ] || { echo "FAIL: $SKILL_FILE not found" >&2; exit 1; }

proof_section=$(awk '
  /^## 4\. Prove and verify$/ { armed = 1 }
  armed && /^## 5\./ { exit }
  armed { print }
' "$SKILL_FILE")

if [ -z "$proof_section" ]; then
  echo "FAIL: could not find the Prove and verify section. Cannot verify anything." >&2
  exit 1
fi

proof_joined=$(printf '%s' "$proof_section" | tr '\n' ' ')
check() {
  local want="$1" message="$2"
  grep -qF "$want" <<<"$proof_joined" || fail "$message"
}

check 'commit before mutating' 'no longer says to commit before mutating'
check '`git add -A && git commit -m wip`' 'no longer stages the complete checkpoint'
check 'confirm `git status --porcelain` is empty right' 'no longer verifies a clean checkpoint'
check '`git checkout -- <file>`' 'no longer bans checkout-based restore'
check '`git restore --source=HEAD`' 'no longer bans ambient-HEAD restore'
check 'reset --hard' 'no longer bans hard reset'
check 'or any command of the same shape' 'no longer generalizes the restore ban'
check 'Restore *only* by reading' 'no longer requires restore from the captured commit'
check 'Never mutate a dirty tree' 'no longer bans mutating a dirty tree'
check 'in `/tmp` or a scratchpad instead of a commit' 'no longer bans scratch backups'

if [ "$rc" -eq 0 ]; then
  echo "OK: checkpoint-idiom-guard_test — complete checkpoint idiom present"
fi
exit "$rc"
