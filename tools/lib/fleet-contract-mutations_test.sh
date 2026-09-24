#!/usr/bin/env bash
# fleet-contract-mutations_test.sh — the committed mutation fixtures for
# tools/lib/fleet-contract_test.sh (#2022).
#
# WHY THIS FILE EXISTS. fleet-contract_test.sh is a check the #2022 change
# ADDS: it has no "before the fix" to run red, so per AGENTS.md and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken. Nine breakages, proven separately
# because a single combined mutation could pass while one of them was
# actually unguarded.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this
# file must not re-improvise: the stale-anchor guard, the no-op replacement
# refusal, and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).
#
# The `assert_mutation_is_red` mechanics live in tools/lib/mutation-assert.sh.
# This file uses the minimal preamble dialect of
# tools/lib/triage-exec-contract-mutations_test.sh, which is the newest of the
# two dialects in this directory.
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$DIR/../.." && pwd)
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
MUTATE_SH=$REPO_ROOT/tools/mutate.sh
# shellcheck disable=SC2034  # read by assert_mutation_is_red in mutation-assert.sh
LOCK_TEST=tools/lib/fleet-contract_test.sh

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo 'fleet-contract-mutations: CANNOT RUN — worktree is dirty' >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then exit 1; fi
  exit 0
fi

fails=0

# ── 1. A review gate that cannot be shown to have run reads as clean ────────
assert_mutation_is_red \
  'guard catches the silent-review rule being softened' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'A review that reported nothing is not a clean gate until there is evidence it\nran.' \
  $'A review that reported nothing is a clean gate once the agent says so.' \
  'exec section 6 no longer says a silent review is not a clean gate'

# ── 2. An undeclared scope gets co-dispatched ───────────────────────────────
assert_mutation_is_red \
  'guard catches an undeclared scope becoming co-dispatchable' \
  '.claude/skills/ir:fleet/SKILL.md' \
  $'an unknown scope is never co-dispatched' \
  $'an unknown scope is treated as disjoint' \
  'ir:fleet no longer keeps an undeclared scope out of a shared wave'

# ── 3. Gate selection goes back to a hand-picked list ───────────────────────
assert_mutation_is_red \
  'guard catches --changed being replaced by a hand-picked gate list' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'tools/preflight.sh --changed --only <group>' \
  $'tools/preflight.sh --only <group>' \
  'exec section 4 no longer names --changed'

# ── 4. A wait keys on a process name again ──────────────────────────────────
assert_mutation_is_red \
  'guard catches the self-owned-marker wait rule being dropped' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'Key every wait on a marker in a log this run owns, never on a process name.' \
  $'Key every wait on whichever signal is convenient, including a process name.' \
  'exec section 4 no longer bans keying a wait on a process name'

# ── 5. The ref check goes back to a substring search ────────────────────────
# This is the one to get right: it is the check that fooled the orchestrator
# itself on 2026-09-20. The mutation reintroduces the exact broken idiom.
assert_mutation_is_red \
  'guard catches a ref check rewritten as a substring grep' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'tools/lib/ref-exists.sh origin feat/<N>-<slug>\ngh pr create --base main --draft \\' \
  $'git ls-remote --heads origin | grep feat/<N>-<slug>\ngh pr create --base main --draft \\' \
  'pipes git ls-remote into a grep'

# ── 6. A resume loses its entry point ───────────────────────────────────────
assert_mutation_is_red \
  'guard catches the resume entry point being removed' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'when the caller names an existing worktree for `<N>` and asks to continue it,' \
  $'when the caller asks to continue an interrupted run,' \
  'exec section 2 no longer lets a caller resume an existing worktree'

# ── 7. The repo root goes back to answering relative ────────────────────────
assert_mutation_is_red \
  'guard catches the repo root losing --path-format=absolute' \
  '.claude/skills/ir:fleet/SKILL.md' \
  $'REPO_ROOT=$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")' \
  $'REPO_ROOT=$(dirname "$(git rev-parse --git-common-dir)")' \
  'ir:fleet computes the repo root without --path-format=absolute'

# ── 8. The hand-back stops asserting a PR exists (#2029) ────────────────────
# Deleting the line is the exact shape of the gap: the ref check stays, the
# push "lands", and a run that stops before `gh pr create` leaves a branch
# nobody can see.
assert_mutation_is_red \
  'guard catches the pr-exists hand-back assertion being deleted' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'  --title "WIP: <type>(<scope>): <change>" --body "..."\ntools/lib/pr-exists.sh feat/<N>-<slug>\n' \
  $'  --title "WIP: <type>(<scope>): <change>" --body "..."\n' \
  'exec section 6 no longer asserts a PR exists for the pushed branch'

assert_mutation_is_red \
  'guard catches the no-turn-end-before-a-PR rule being softened' \
  '.claude/skills/ir:exec/SKILL.md' \
  $'Do not end a turn — not for a\nquestion' \
  $'Try not to end a turn — not for a\nquestion' \
  'exec section 6 no longer forbids ending a turn between the push and a PR'

if [[ $fails -gt 0 ]]; then
  echo "fleet-contract-mutations: $fails FAILED"
  exit 1
fi
echo 'fleet-contract-mutations: ALL PASS'
