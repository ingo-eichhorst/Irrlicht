#!/usr/bin/env bash
# checkpoint-idiom-guard-mutations_test.sh — the committed mutation fixtures
# for tools/lib/checkpoint-idiom-guard_test.sh (#1824).
#
# WHY THIS FILE EXISTS. checkpoint-idiom-guard_test.sh is a check #1824 ADDS:
# it has no "before the fix" to run red, so per AGENTS.md and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken. Two independent breakages, proven
# separately because a single combined mutation could pass while one of them
# was actually unguarded:
#
#   1. THE IDIOM LOSES A PIECE — the verification text drops the
#      never-mutate-a-dirty-tree sentence. An agent reading only this section
#      would no longer be told the one rule that makes a restore
#      unambiguous. Dropping this piece stands in for any listed rule.
#
#   2. THE SECTION HEADING MOVES, so the lock test's anchor no longer matches
#      anything. This is the direction a
#      plain substring search over the whole file cannot catch: the text
#      could still be sitting in the file, correctly worded, just no longer
#      reachable by the anchor the lock test uses to scope its search — and
#      the lock test must refuse loudly ("Cannot verify anything") rather
#      than either silently passing or silently searching the wrong part of
#      the file.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this
# file must not re-improvise: the stale-anchor guard, the no-op replacement
# refusal, and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide for THIS tool's own
# restore — see tools/mutate.sh's header for why it uses a plain filesystem
# copy-back instead).
#
# The `assert_mutation_is_red` mechanics live in tools/lib/mutation-assert.sh,
# shared with tools/lib/preflight-groups-skill-mutations_test.sh and
# tools/lib/simplify-angle-guard-mutations_test.sh — reused here rather than
# copied a third time. Convention for the rest follows tools/lib/mutate_test.sh:
# plain bash, a `fails` counter, "ALL PASS" / "N FAILED" at the end.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
# shellcheck disable=SC2034  # read by assert_mutation_is_red in the sourced mutation-assert.sh, not here
LOCK_TEST="tools/lib/checkpoint-idiom-guard_test.sh"
SKILL_FILE=".claude/skills/ir:exec/SKILL.md"

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: checkpoint-idiom-guard-mutations — $1 not found" >&2; exit 1; }; }
need bash
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: checkpoint-idiom-guard-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS — see
# tools/lib/preflight-groups-skill-mutations_test.sh for the full reasoning;
# this file repeats the same guard rather than sourcing it, matching the
# convention every fixture in this directory uses.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "checkpoint-idiom-guard-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/checkpoint-idiom-guard-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# ── 1. The idiom loses a piece: the never-mutate-a-dirty-tree sentence ──────
assert_mutation_is_red \
  "lock test catches the verification section dropping the never-mutate-a-dirty-tree rule" \
  "$SKILL_FILE" \
  $'`reset --hard`, or any command of the same shape. Never mutate a dirty tree.\nNever keep the checkpoint in `/tmp` or a scratchpad instead of a commit.' \
  $'`reset --hard`, or any command of the same shape. Proceed carefully with a dirty tree.\nNever keep the checkpoint in `/tmp` or a scratchpad instead of a commit.' \
  "no longer bans mutating a dirty tree"

# ── 2. The step heading moves, so the lock test's own anchor goes stale ─────
assert_mutation_is_red \
  "lock test refuses loudly when the verification heading moves" \
  "$SKILL_FILE" \
  $'## 4. Prove and verify' \
  $'## 4. Check results' \
  "Cannot verify anything"

if [[ $fails -gt 0 ]]; then
  echo "checkpoint-idiom-guard-mutations: $fails FAILED"
  exit 1
fi
echo "checkpoint-idiom-guard-mutations: ALL PASS"
