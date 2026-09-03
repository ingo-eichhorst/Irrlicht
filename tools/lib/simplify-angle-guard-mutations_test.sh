#!/usr/bin/env bash
# simplify-angle-guard-mutations_test.sh — the committed mutation fixtures for
# tools/lib/simplify-angle-guard_test.sh (#1823).
#
# WHY THIS FILE EXISTS. simplify-angle-guard_test.sh is a check the #1823 fix
# ADDS: it has no "before the fix" to run red, so per AGENTS.md and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken. Two independent breakages, proven
# separately because a single combined mutation could pass while one of them
# was actually unguarded:
#
#   1. AN ANGLE NAME IS DROPPED from the review text — an agent reading
#      only this section would no longer know that angle exists to check for.
#      Dropping `altitude` stands in for any of the four.
#
#   2. THE LOUD-FAILURE INSTRUCTION IS SOFTENED — "surface it and pause"
#      (this skill's own established stop-the-run idiom) replaced with a
#      "be careful" style caveat that nothing enforces. This is the exact
#      shape AGENTS.md calls out by name: "do not paper over it by simply
#      telling agents to be careful" — a softened instruction still LOOKS
#      like guidance and would pass a casual re-read.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this
# file must not re-improvise: the stale-anchor guard, the no-op replacement
# refusal, and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).
#
# The `assert_mutation_is_red` mechanics live in tools/lib/mutation-assert.sh,
# shared with tools/lib/preflight-groups-skill-mutations_test.sh (#1823
# review: the two were originally byte-identical copies in this diff).
# Convention for the rest follows tools/lib/error-retention-mutations_test.sh:
# plain bash, a `fails` counter, "ALL PASS" / "N FAILED" at the end.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
# shellcheck disable=SC2034  # read by assert_mutation_is_red in the sourced mutation-assert.sh, not here
LOCK_TEST="tools/lib/simplify-angle-guard_test.sh"
SKILL_FILE=".claude/skills/ir:exec/SKILL.md"

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: simplify-angle-guard-mutations — $1 not found" >&2; exit 1; }; }
need bash
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: simplify-angle-guard-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS — see error-retention-mutations_test.sh
# for the full reasoning; this file repeats the same guard rather than
# sourcing it, matching the convention every fixture in this directory uses.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "simplify-angle-guard-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/simplify-angle-guard-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# ── 1. An angle name is dropped from the review text ────────────────────────
assert_mutation_is_red \
  "lock test catches the review section dropping the 'altitude' angle" \
  "$SKILL_FILE" \
  $'each by name: reuse, simplification, efficiency, altitude. If one angle is' \
  $'each by name: reuse, simplification, efficiency. If one angle is' \
  "no longer enumerates all four angles"

# ── 2. The loud-failure instruction is softened ──────────────────────────────
assert_mutation_is_red \
  "lock test catches the review section softening 'surface it and pause' into a caveat" \
  "$SKILL_FILE" \
  $'If one angle is\nsilent, surface it and pause. Commit and push any cleanup.' \
  $'If one angle is\nsilent, proceed carefully. Commit and push any cleanup.' \
  "no longer pairs the four-angle check with a 'surface it and pause' instruction"

if [[ $fails -gt 0 ]]; then
  echo "simplify-angle-guard-mutations: $fails FAILED"
  exit 1
fi
echo "simplify-angle-guard-mutations: ALL PASS"
