#!/usr/bin/env bash
# preflight-groups-skill-mutations_test.sh — the committed mutation fixtures
# for tools/lib/preflight-groups-skill_test.sh (#1823).
#
# WHY THIS FILE EXISTS. preflight-groups-skill_test.sh is a check the #1823
# fix ADDS: it has no "before the fix" to run red, so per AGENTS.md and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken. Two independent breakages, proven
# separately because a single combined mutation could pass while one of them
# was actually unguarded:
#
#   1. THE SKILL DROPS A GROUP — .claude/skills/ir:exec/SKILL.md's chunk list
#      loses a line (the actual #1823 defect: `swift` and `bash` were both
#      missing). Removing `bash` here stands in for either.
#
#   2. PREFLIGHT GAINS A GROUP the skill never mentions — tools/preflight.sh's
#      VALID_GROUPS array gains a new entry, and the skill's list is never
#      updated to match. This is the direction a hand-corrected list (rather
#      than a derived one) cannot catch: fixing today's omission does
#      nothing for tomorrow's addition.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this
# file must not re-improvise: the stale-anchor guard, the no-op replacement
# refusal, and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).
#
# The `assert_mutation_is_red` mechanics live in tools/lib/mutation-assert.sh,
# shared with tools/lib/simplify-angle-guard-mutations_test.sh (#1823 review:
# the two were originally byte-identical copies in this diff). Convention for
# the rest follows tools/lib/error-retention-mutations_test.sh: plain bash, a
# `fails` counter, "ALL PASS" / "N FAILED" at the end — that file keeps its
# own DIFFERENT 6-arg `go test -run` variant rather than being folded in here,
# since its shape genuinely differs, not just its wording.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
# shellcheck disable=SC2034  # read by assert_mutation_is_red in the sourced mutation-assert.sh, not here
LOCK_TEST="tools/lib/preflight-groups-skill_test.sh"

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: preflight-groups-skill-mutations — $1 not found" >&2; exit 1; }; }
need bash
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: preflight-groups-skill-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS. This suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guard was verified" and "the guard could not
# be checked at all" produce byte-identical results at the gate — exactly the
# shape AGENTS.md forbids, and the same shape as the tripwire blind spot this
# fixture exists to prevent regressing.
#
# So it is a HARD FAILURE wherever the answer is load-bearing (CI, and any
# caller that sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on
# a developer's dirty worktree, where failing would only train people to
# delete the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "preflight-groups-skill-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/preflight-groups-skill-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# ── 1. The skill DROPS a group (the actual #1823 defect) ────────────────────
# Removing `--only bash` from the chunk list must be caught even though every
# other line still matches.
assert_mutation_is_red \
  "lock test catches SKILL.md dropping a group from its chunk list" \
  ".claude/skills/ir:exec/SKILL.md" \
  $'tools/preflight.sh --only posix\ntools/preflight.sh --only bash\n' \
  $'tools/preflight.sh --only posix\n' \
  'only in VALID_GROUPS: bash'

# ── 2. preflight.sh's VALID_GROUPS GAINS a group the skill never mentions ───
# A future new group added to VALID_GROUPS but never added to the skill's
# chunk list is the direction a one-time hand correction cannot catch; only
# a derived comparison does.
assert_mutation_is_red \
  "lock test catches VALID_GROUPS gaining a group the skill omits" \
  "tools/preflight.sh" \
  $'VALID_GROUPS=(go web arch tools skills posix bash security swift linux)' \
  $'VALID_GROUPS=(go web arch tools skills posix bash security swift linux wasm)' \
  'only in VALID_GROUPS: wasm'

if [[ $fails -gt 0 ]]; then
  echo "preflight-groups-skill-mutations: $fails FAILED"
  exit 1
fi
echo "preflight-groups-skill-mutations: ALL PASS"
