#!/usr/bin/env bash
# test-mac-script-mutations_test.sh — the committed mutation fixtures for
# tools/lib/test-mac-script_test.sh (#1855).
#
# WHY THIS FILE EXISTS. Every gate in .claude/skills/ir:test-mac/test-mac.sh
# was MOVED there out of SKILL.md's fenced bash blocks, and two more were
# ADDED. Either way there is no "before the fix" to run red: the lock test
# passes the moment it is written. Per AGENTS.md's Testing section and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken — so each gate is broken here, one at a
# time, and the lock test is required to go red naming that specific gate.
#
# Nine mutations, applied and asserted SEPARATELY. A single combined mutation
# could go red on one gate while another was silently unguarded, which is the
# exact shape of defect this whole file exists to rule out. Each one is written
# as the plausible REGRESSION, not as arbitrary damage: the reachability abort
# demoted to a no-op, the backup refresh demoted to "make it once, ever", the
# `swift build` status read back through the pipe the way SKILL.md used to.
#
# Every row drives the real tools/mutate.sh, which owns the mechanics this file
# must not re-improvise: the stale-anchor guard, the no-op replacement refusal,
# and the byte-for-byte restore that never touches git state (worktrees share
# the parent repo's .git dir, so `git checkout --` / `git restore` /
# `git reset --hard` are banned repo-wide).
#
# The `assert_mutation_is_red` mechanics live in tools/lib/mutation-assert.sh,
# shared with tools/lib/preflight-groups-skill-mutations_test.sh and
# tools/lib/checkpoint-idiom-guard-mutations_test.sh.
#
# COST. Each row re-runs the whole lock test (~16s), so this file is the
# slowest thing in the `tools` gate by a wide margin. That is the price of
# nine independently-proven gates and it is paid deliberately; the alternative
# — one combined mutation, or a filtered lock test — buys seconds by giving up
# the thing being bought.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
# shellcheck disable=SC2034  # read by assert_mutation_is_red in the sourced mutation-assert.sh, not here
LOCK_TEST="tools/lib/test-mac-script_test.sh"
SUBJECT=".claude/skills/ir:test-mac/test-mac.sh"

# shellcheck source=tools/lib/mutation-assert.sh
. "$DIR/mutation-assert.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as a
# PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: test-mac-script-mutations — $1 not found" >&2; exit 1; }; }
need bash
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: test-mac-script-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS. This suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the gates were verified" and "the gates could not
# be checked at all" produce byte-identical results — exactly the shape
# AGENTS.md forbids. So it is a HARD FAILURE wherever the answer is
# load-bearing (CI, and any caller that sets MUTATION_FIXTURES_STRICT=1), and a
# loud, non-silent skip on a developer's dirty worktree, where failing would
# only train people to delete the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "test-mac-script-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/test-mac-script-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# ── 1. The daemon-reachability gate is demoted to a no-op ───────────────────
# The failure it prevents is silent: the app finds no daemon on 7837, runs
# `pkill -x irrlichd`, and respawns one WITHOUT --record.
assert_mutation_is_red \
  "reachability gate: the abort never fires" \
  "$SUBJECT" \
  $'  if [[ -z "$READY" ]]; then\n    echo "ABORT: daemon never became reachable on $PORT' \
  $'  if false; then\n    echo "ABORT: daemon never became reachable on $PORT' \
  'GATE daemon-reachability: an unreachable daemon must abort'

# ── 2. The app-exit wait stops hard-aborting ────────────────────────────────
# The bundle is then overwritten while the process that has those files open is
# still alive.
assert_mutation_is_red \
  "app-exit wait: the hard abort never fires" \
  "$SUBJECT" \
  $'  if [[ "$MODE" == "replace" ]] && pgrep -f "$APP_KILL_PATTERN" >/dev/null 2>&1; then' \
  $'  if false; then' \
  'GATE app-exit-wait: a still-running app must abort'

# ── 3. Backup freshness becomes "make it once, ever" ────────────────────────
# The regression that actually threatens this gate is not deleting it, it is
# adding the natural-looking `[[ ! -d "$PROD_BACKUP" ]] &&` — after which a
# backup taken before a newer production release is never refreshed, and
# restore-prod.sh silently reinstalls a stale build.
assert_mutation_is_red \
  "backup freshness: an existing backup is trusted blindly" \
  "$SUBJECT" \
  $'    if codesign -dv --verbose=4 "$PROD_APP" 2>&1 | grep -q "^Authority=Developer ID Application"; then' \
  $'    if [[ ! -d "$PROD_BACKUP" ]] && codesign -dv --verbose=4 "$PROD_APP" 2>&1 | grep -q "^Authority=Developer ID Application"; then' \
  'GATE backup-freshness: the stale backup was NOT refreshed'

# ── 4. The no-safety-net refusal stops refusing ─────────────────────────────
assert_mutation_is_red \
  "backup refusal: a dev-signed app with no backup is overwritten anyway" \
  "$SUBJECT" \
  $'    elif [[ ! -d "$PROD_BACKUP" ]]; then' \
  $'    elif false; then' \
  'GATE backup-refusal: dev-signed app + no backup must refuse'

# ── 5. The build-output existence check stops checking ──────────────────────
assert_mutation_is_red \
  "build-output existence: a missing product no longer stops the install" \
  "$SUBJECT" \
  $'  if [[ ! -x "$DEBUG_BIN" || ! -d "$DEBUG_SPARKLE" ]]; then' \
  $'  if false; then' \
  'GATE build-output-exists:'

# ── 6. The enum gains the default branch it was written without ─────────────
# A typo like `seperate` then silently runs the DESTRUCTIVE default instead of
# erroring — the exact hazard SKILL.md's step 0 named.
assert_mutation_is_red \
  "enum validation: an unrecognised axis value is silently ignored" \
  "$SUBJECT" \
  $'    *)\n      echo "ERROR: unrecognised argument \'$arg\'." >&2' \
  $'    *)\n      : ;;\n    unreachable)\n      echo "ERROR: unrecognised argument \'$arg\'." >&2' \
  'would have silently run in replace mode'

# ── 7. The daemon is killed before the app ─────────────────────────────────
# A still-alive app then observes a daemon-less gap and spawns its own
# replacement, which can win the port race against the daemon started next.
assert_mutation_is_red \
  "kill order: the daemon is killed first" \
  "$SUBJECT" \
  $'if want_app; then\n  echo "Stopping the app ($APP_KILL_PATTERN) …"' \
  $'if want_daemon && [[ "$MODE" == "replace" ]]; then\n  pkill -x "irrlichd" 2>/dev/null || true\nfi\nif want_app; then\n  echo "Stopping the app ($APP_KILL_PATTERN) …"' \
  'GATE kill-order: the daemon was killed at line'

# ── 8. Build freshness (ADDED by #1855) stops firing ───────────────────────
assert_mutation_is_red \
  "build freshness: a stale build product is installed anyway" \
  "$SUBJECT" \
  $'  if [[ -n "$STALE_SRC" ]]; then' \
  $'  if false; then' \
  'GATE build-freshness: a source newer than the build product must abort'

# ── 8b. ...and its own cannot-run refusal stops refusing ───────────────────
# A freshness check with nothing to compare against would otherwise report the
# same green as one that looked and found nothing.
assert_mutation_is_red \
  "build freshness: an empty source set passes vacuously" \
  "$SUBJECT" \
  $'  if [[ "$SWIFT_SRC_COUNT" -eq 0 ]]; then' \
  $'  if false; then' \
  'a vacuous pass'

# ── 9. `swift build`'s status is read back through the pipe ────────────────
# This is SKILL.md's own pre-#1855 spelling (`swift build 2>&1 | tail -5`),
# whose exit status is tail's. A failed build reported success and the run
# walked into the install with whatever binary was lying around.
assert_mutation_is_red \
  "swift build status: a failed build is reported as success by the pipe" \
  "$SUBJECT" \
  $'  if ! swift build --package-path "$REPO_ROOT/platforms/macos" 2>&1 | tail -5; then\n    echo "ERROR: swift build failed — not touching $APP_TARGET." >&2\n    exit 1\n  fi' \
  $'  swift build --package-path "$REPO_ROOT/platforms/macos" 2>&1 | tail -5 || true' \
  'GATE swift-build-status: a failed swift build must abort'

# ── 10. The `tools` gate stops firing on this script's own directory ───────
# Not a gate inside the script, but the gate that RUNS the two files above.
# Without the trigger alternative added in #1855, a push that changes only
# test-mac.sh skips the whole `tools` group under `--changed`, and a gate that
# never ran is indistinguishable from one that found nothing.
assert_mutation_is_red \
  "preflight trigger: the tools gate stops covering .claude/skills/ir:test-mac/" \
  "tools/preflight.sh" \
  $'|^\\.claude/skills/ir:test-mac/|^\\.github/workflows/(ars|codescene-badge' \
  $'|^\\.github/workflows/(ars|codescene-badge' \
  'the tools gate does NOT fire on a diff touching .claude/skills/ir:test-mac/test-mac.sh'

if [[ $fails -gt 0 ]]; then
  echo "test-mac-script-mutations: $fails FAILED"
  exit 1
fi
echo "test-mac-script-mutations: ALL PASS"
