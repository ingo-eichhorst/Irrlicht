#!/usr/bin/env bash
# Keep the triage output and exec input contracts aligned.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || exit 1

TRIAGE=.claude/skills/ir:triage/SKILL.md
EXEC=.claude/skills/ir:exec/SKILL.md
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

for file in "$TRIAGE" "$EXEC"; do
  [ -f "$file" ] || { echo "FAIL: $file not found" >&2; exit 1; }
done

triage_template=$(awk '
  /^## Assessment format$/ { armed = 1 }
  armed && /^Apply labels and a milestone/ { exit }
  armed { print }
' "$TRIAGE")
exec_validation=$(awk '
  /^## 1\. Validate the triage contract$/ { armed = 1 }
  armed && /^## 2\./ { exit }
  armed { print }
' "$EXEC")

[ -n "$triage_template" ] || fail 'cannot read the triage assessment template'
[ -n "$exec_validation" ] || fail 'cannot read the exec validation section'

for field in '## High-level design' '## Testing strategy' '**Process:**' '**Estimate:**' '**Verdict:**'; do
  grep -qF "$field" <<<"$triage_template" ||
    fail "triage assessment template missing required field: $field"
  grep -qF "$field" <<<"$exec_validation" ||
    fail "exec validation missing required field: $field"
done

grep -qF '/ir:exec <N>' "$EXEC" || fail 'exec no longer documents /ir:exec <N>'
for mode in auto plan full close; do
  if grep -qF "/ir:exec $mode" "$EXEC"; then
    fail "exec documents unsupported mode: /ir:exec $mode"
  fi
done
grep -qF 'templates/plan.html' "$EXEC" && fail 'exec still references the removed HTML planner'
grep -qF 'gh pr merge' "$EXEC" && fail 'exec still performs a merge'

if [ "$rc" -eq 0 ]; then
  echo 'OK: triage-exec-contract_test — contracts align and exec has one mode'
fi
exit "$rc"
