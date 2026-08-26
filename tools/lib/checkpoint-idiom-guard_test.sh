#!/usr/bin/env bash
# checkpoint-idiom-guard_test.sh — hold ir:exec SKILL.md's step 11a to a
# mechanical requirement: it must state the checkpoint-and-restore idiom
# explicitly — commit before mutating, restore only against that captured
# commit, never against a dirty tree, never via a command that targets
# ambient HEAD, and never via a `/tmp`/scratchpad backup instead of a commit
# (#1824).
#
# WHY THIS EXISTS. Three separate implementing agents (#1799, #1801, #1817)
# and a fourth incident (#1800) each independently discarded uncommitted work
# because step 11a said "checkpoint it ... then restore" without ever saying
# HOW to restore safely. Each agent invented its own answer, and the ones
# that used `git checkout -- <file>` or `git restore --source=HEAD` against a
# dirty tree — two different command names, the same ambient-HEAD shape —
# silently reverted work that was never part of the mutation being undone. A
# prose fix on its own is exactly the failure this issue is about: none of
# those four instances were caught by review, because a step that quietly
# stopped saying enough reads identically to one that never needed to. So the
# requirement itself needs a mechanical check that can go stale-and-caught
# rather than stale-and-silent — this is that check. It is a LOCK (passes
# today by construction, since the fix above just added the text) proven
# capable of failing by
# tools/lib/checkpoint-idiom-guard-mutations_test.sh, which deletes pieces of
# the requirement with tools/mutate.sh and confirms this goes red, then
# restores.
#
# What is (and isn't) checked. This cannot verify that a running agent
# actually commits before mutating or actually restores against the right
# SHA — no static check can enforce runtime behavior. What it CAN verify, and
# does: the instruction to do so is present and names, individually, each
# piece of the idiom (stage-everything-before-checkpointing plus the
# post-commit porcelain check — added after this repo's own review
# subagent caught a bare `git commit -m wip` silently skipping untracked
# files, #1824 — restore-only-against-the-captured-SHA, the ban on both
# named ambient-HEAD restore commands plus an explicit generalization to
# "any command of the same shape," the never-mutate-a-dirty-tree rule, and
# the extended `/tmp`/scratchpad ban) —
# the same class of guarantee tools/skill-lint.sh and
# tools/lib/simplify-angle-guard_test.sh already give this repo's other skill
# prose: not proof of compliance, but proof the instruction cannot silently
# erode out of the file the way the underlying defect did.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

SKILL_FILE=".claude/skills/ir:exec/SKILL.md"
rc=0
fail() { echo "FAIL: $1" >&2; rc=1; }

[ -f "$SKILL_FILE" ] || { echo "FAIL: checkpoint-idiom-guard_test — $SKILL_FILE not found" >&2; exit 1; }

# Anchored on step 11a's own heading text through the next numbered step
# (11b) — not a line range, which drifts on every unrelated edit above this
# point.
step11a=$(awk '
  /^11a\. \*\*Prove red-first\.\*\*/ { armed = 1 }
  armed && /^11b\./ { exit }
  armed { print }
' "$SKILL_FILE")

if [ -z "$step11a" ]; then
  echo "FAIL: checkpoint-idiom-guard_test — could not find step 11a (the red-first / checkpoint step) in $SKILL_FILE; its heading text may have moved. Cannot verify anything." >&2
  exit 1
fi

# Whitespace-joined, not $step11a directly: markdown's own line wrap can
# legitimately split a phrase across two source lines without changing what a
# reader sees rendered, and a literal multi-line grep would then report the
# phrase missing when it plainly isn't — the exact "absence of a finding and
# inability to look produce the same output" shape this whole issue is about,
# one layer down in this test itself (same reasoning as
# tools/lib/simplify-angle-guard_test.sh). Every phrase checked below is
# picked to sit on one physical source line as of this writing, so the join
# is a robustness margin against future reflow, not a requirement for today's
# text to pass.
step11a_joined=$(printf '%s' "$step11a" | tr '\n' ' ')

check() {
  local want="$1" msg="$2"
  if ! grep -qF "$want" <<<"$step11a_joined"; then
    fail "$SKILL_FILE step 11a $msg"
  fi
}

check 'commit before mutating' \
  "no longer says to commit a checkpoint before mutating — an agent has nothing telling it when to capture one"

check '`git add -A && git commit -m wip`' \
  "no longer stages everything before checkpointing — a bare \`git commit -m wip\` silently commits only what was already staged, so a new untracked file never enters the checkpoint at all (review finding on this PR itself)"

check 'confirm `git status --porcelain` is empty right' \
  "no longer confirms the checkpoint commit left the tree empty — the one check that catches an incomplete checkpoint loudly instead of silently"

check '`git checkout -- <file>`' \
  "no longer bans \`git checkout -- <file>\` as a restore — the exact command that silently discarded uncommitted work in #1799 and #1801"

check '`git restore --source=HEAD`' \
  "no longer bans \`git restore --source=HEAD\` as a restore — the exact command that silently discarded uncommitted review fixes in #1817"

check 'reset --hard' \
  "no longer names \`git reset --hard\` among the banned restores — this repo forbids it everywhere for the same ambient-HEAD reason (tools/mutate.sh's header), and it would otherwise read as a permitted third spelling"

check 'or any command of the same shape' \
  "no longer generalizes the ban beyond the named commands — a fourth ambient-HEAD restore spelling would then read as permitted"

check 'Restore *only* by reading the checkpoint back' \
  "no longer says to restore only by reading the checkpoint commit back — the one instruction that distinguishes a safe restore from an ambient-HEAD one"

check 'Never mutate a dirty tree' \
  "no longer bans mutating a dirty tree — without it, a restore has no way to know what it's safe to discard"

check 'in `/tmp` or a scratchpad instead of a commit' \
  "no longer extends the /tmp ban to mutation/checkpoint backups — the exact gap that let a stale /tmp backup silently revert a change in #1800"

if [ "$rc" -eq 0 ]; then
  echo "OK: checkpoint-idiom-guard_test — $SKILL_FILE step 11a states the checkpoint idiom (commit before mutating, restore only against the captured SHA, never checkout/restore against ambient HEAD in any shape, never a dirty-tree mutation, never a /tmp backup)"
fi
exit "$rc"
