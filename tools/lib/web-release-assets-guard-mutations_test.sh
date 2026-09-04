#!/usr/bin/env bash
# web-release-assets-guard-mutations_test.sh — the committed mutation fixtures
# for tools/lib/web-release-assets-guard_test.sh (#1900).
#
# WHY THIS FILE EXISTS. The #1900 fix ADDS a guard, and per AGENTS.md and
# docs/testing-philosophy.md a guard earns its place by being seen to fail when
# the thing it protects is broken. This one is unusual in also having a real
# "before the fix" — the defect was live on main, and the guard's closure run
# against origin/main's own `cp` line names all ten missing modules — but that
# red is a one-off nobody re-runs. These six mutations are the part that keeps
# re-running, each breaking ONE property, because a single combined mutation
# could go red while one of the properties was in fact unguarded:
#
#   1. THE WALK IS TRANSITIVE. collapsedSet.js is imported by
#      collapsedGroups.js and collapsedSummaries.js and by nothing else, so a
#      scan of irrlicht.js's DIRECT imports finds 9 of the 10 modules and
#      reports completeness. Dropping collapsedSet.js from the staged set is
#      the mutation that a direct-only scan would sail straight past.
#   2. ...AND IT COVERS THE DIRECT EDGES TOO. Dropping formatters.js, a
#      first-hop import, so 1's pass cannot be an accident of depth.
#   3. THE DEV-ONLY HALF IS REAL. Without it, `cp -R platforms/web/` —
#      node_modules and all — satisfies every "nothing is missing" assertion.
#   4. THE SCAN CAN ACTUALLY READ THE TREE. irrlicht.js and sessionIdentity.js
#      carry literal NUL bytes; a grep without `-a` answers "Binary file …
#      matches" and yields no edges. This is the exact shape of vacuity the
#      guard exists to refuse, so it is mutated rather than trusted.
#   5. ...AND SAYS SO WHEN IT CANNOT. Disabling the zero-edge refusal, so an
#      unreadable tree would report success instead.
#   6. THE SHIPPING SCRIPT STAYS ATTACHED TO THE RULE. 1-5 all grade
#      tools/lib/stage-web.sh; this one puts a hand-written `cp` back into
#      tools/build-release.sh, which is what actually shipped the bug.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this file
# must not re-improvise: the stale-anchor guard, the no-op replacement refusal,
# and the byte-for-byte restore that never touches git state (worktrees share
# the parent repo's .git dir, so `git checkout --` / `git restore` /
# `git reset --hard` are banned repo-wide). `assert_mutation_is_red` lives in
# tools/lib/mutation-assert.sh. Convention follows
# tools/lib/simplify-angle-guard-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
# shellcheck disable=SC2034  # read by assert_mutation_is_red in the sourced mutation-assert.sh, not here
LOCK_TEST="tools/lib/web-release-assets-guard_test.sh"
STAGE_LIB="tools/lib/stage-web.sh"
GUARD="tools/web-release-assets-guard.sh"
BUILD_RELEASE="tools/build-release.sh"
PREFLIGHT="tools/preflight.sh"
RELEASE_SKILL=".claude/skills/ir:release/SKILL.md"

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: web-release-assets-guard-mutations — $1 not found" >&2; exit 1; }; }
need bash
need git
need grep

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: web-release-assets-guard-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then. A dirty tree must not
# silently pass — same guard, same wording, as every other fixture here.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "web-release-assets-guard-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/web-release-assets-guard-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

DEV_ONLY_ANCHOR='        *.test.js|vitest.config.js|vitest.setup.js) return 0 ;;'

# ── 1. The walk is transitive: collapsedSet.js specifically ─────────────────
assert_mutation_is_red \
  "lock test catches the staging rule dropping collapsedSet.js — the module NO file imports directly" \
  "$STAGE_LIB" \
  "$DEV_ONLY_ANCHOR" \
  '        *.test.js|vitest.config.js|vitest.setup.js|collapsedSet.js) return 0 ;;' \
  "'collapsedSet.js' is reachable from index.html but tools/build-release.sh would not stage it"

# ── 2. ...and the direct edges are covered too ───────────────────────────────
assert_mutation_is_red \
  "lock test catches the staging rule dropping formatters.js — a first-hop import" \
  "$STAGE_LIB" \
  "$DEV_ONLY_ANCHOR" \
  '        *.test.js|vitest.config.js|vitest.setup.js|formatters.js) return 0 ;;' \
  "'formatters.js' is reachable from index.html but tools/build-release.sh would not stage it"

# ── 3. The dev-only half is real ────────────────────────────────────────────
assert_mutation_is_red \
  "lock test catches the staging rule shipping *.test.js into the release tree" \
  "$STAGE_LIB" \
  "$DEV_ONLY_ANCHOR" \
  '        vitest.config.js|vitest.setup.js) return 0 ;;' \
  "is dev-only tooling and must not ship in a release artifact"

# ── 4. The scan can actually read a NUL-containing module ────────────────────
#
# Precondition, asserted rather than assumed: this machine's grep must treat a
# NUL-containing file as binary, or removing `-a` changes nothing here and the
# row below would report a false GREEN. Reported as a failure, not a skip —
# "the mutation did not go red" and "this machine cannot judge the mutation"
# must not print the same thing.
NUL_PROBE=$(mktemp -t wrag-nul-probe) || exit 1
printf 'a\000\nfrom\n' >"$NUL_PROBE"
probe_out=$(LC_ALL=C command grep -oE 'from' "$NUL_PROBE" 2>&1)
rm -f "$NUL_PROBE"
if [[ "$probe_out" == "from" ]]; then
  echo "FAIL: the NUL mutation cannot be judged on this machine — $(command -v grep) does not treat a"
  echo "      NUL-containing file as binary, so dropping \`-a\` from the guard's scan is a no-op here."
  echo "      The guard still needs \`-a\` (GNU grep in CI does suppress the matches); this fixture"
  echo "      simply cannot prove it from this host."
  fails=$((fails + 1))
else
  assert_mutation_is_red \
    "lock test catches the import scan losing \`grep -a\` and reading a NUL-containing module as binary" \
    "$GUARD" \
    '_wrag_text_grep() { LC_ALL=C command grep -a "$@"; }' \
    '_wrag_text_grep() { LC_ALL=C command grep "$@"; }' \
    "the walk refused on a module containing a NUL byte"
fi

# ── 5. ...and refuses out loud when it finds nothing ─────────────────────────
assert_mutation_is_red \
  "lock test catches the zero-import-edge refusal being disarmed, so an unreadable tree would pass" \
  "$GUARD" \
  '    if [[ "$edges" -eq 0 ]]; then' \
  '    if [[ "$edges" -lt 0 ]]; then' \
  "a module graph with not one import edge — expected REFUSE (2)"

# ── 6. build-release.sh stays attached to the rule ──────────────────────────
assert_mutation_is_red \
  "lock test catches tools/build-release.sh staging the web tree by hand again" \
  "$BUILD_RELEASE" \
  'stage_web platforms/web "$APP_CONTENTS/Resources/web"' \
  'cp platforms/web/index.html platforms/web/irrlicht.css platforms/web/irrlicht.js "$APP_CONTENTS/Resources/web/"' \
  "touches platforms/web outside stage_web"

# ── 7. The gates keep firing on a platforms/web-only diff ───────────────────
#
# The failure this pair pins is silence, not a wrong answer: drop
# `^platforms/web/` from a trigger and preflight --changed reports SKIP —
# exit-code-neutral, indistinguishable from "this diff cannot break it" — for
# exactly the commit that extracts a new module.
assert_mutation_is_red \
  "lock test catches ^platforms/web/ leaving the packaging guard's own trigger" \
  "$PREFLIGHT" \
  "  run_gate_scoped '^platforms/web/|^tools/lib/stage-web\\.sh\$|^tools/web-release-assets-guard\\.sh\$|^tools/build-release\\.sh\$' \\" \
  "  run_gate_scoped '^tools/lib/stage-web\\.sh\$|^tools/web-release-assets-guard\\.sh\$|^tools/build-release\\.sh\$' \\" \
  "'web release assets' would be SKIPped by preflight --changed"

assert_mutation_is_red \
  "lock test catches ^platforms/web/ leaving the shell-lib tests trigger" \
  "$PREFLIGHT" \
  '^AGENTS\.md$|^platforms/web/|^\.claude/skills/ir:test-mac/' \
  '^AGENTS\.md$|^\.claude/skills/ir:test-mac/' \
  "'tools/lib shell-lib tests' would be SKIPped by preflight --changed"

# ── 9. The release PROCEDURE stays attached to the rule too ─────────────────
#
# tools/build-release.sh is not the only thing that assembles a release: the
# manual per-artifact blocks in .claude/skills/ir:release/SKILL.md are the
# documented fallback, and every one of them used to `cp` the web tree by hand
# — two of them copying index.html alone, which is this bug with one file
# instead of three. Guarding only the script would leave the defect sitting in
# the document a human reads.
assert_mutation_is_red \
  "lock test catches the release skill copying the web tree by hand again" \
  "$RELEASE_SKILL" \
  '  stage_web platforms/web /tmp/irrlichd-tarball/web )' \
  '  cp platforms/web/index.html /tmp/irrlichd-tarball/web/ )' \
  "copies platforms/web by hand"

# ── 10. The NUL fixture's own vacuity guard fires ───────────────────────────
#
# Row 4 above is only worth anything while its fixture really carries a NUL.
# The first spelling of this guard used `grep -q $'\000'`, which bash expands
# to the empty string — it matched every file and could never fire. So the
# guard on the fixture is itself mutated: take the NUL out of the fixture and
# the lock test must say so.
assert_mutation_is_red \
  "lock test notices its own NUL fixture losing its NUL byte" \
  "$LOCK_TEST" \
  $'printf \'const SEP = "\\000";\\nimport { x } from \'\\\'\'./dep.js\'\\\'\';\\n\' >"$NUL/app.js"' \
  $'printf \'const SEP = "";\\nimport { x } from \'\\\'\'./dep.js\'\\\'\';\\n\' >"$NUL/app.js"' \
  "the NUL fixture carries no NUL byte"

# ── 11. A CSS reference the walker cannot follow is refused ─────────────────
#
# The walk follows HTML and JavaScript references, not CSS ones. That is safe
# only while the stylesheet has none — so the stylesheet is checked, and this
# row proves the check is real rather than decorative.
assert_mutation_is_red \
  "lock test catches a CSS @import the walker does not follow" \
  "platforms/web/irrlicht.css" \
  '    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }' \
  $'@import url(\'./theme/dark.css\');\n    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }' \
  "carries a CSS reference this walker does not follow"

if [[ $fails -gt 0 ]]; then
  echo "web-release-assets-guard-mutations: $fails FAILED"
  exit 1
fi
echo "web-release-assets-guard-mutations: ALL PASS"
