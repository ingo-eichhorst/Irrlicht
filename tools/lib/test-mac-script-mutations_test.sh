#!/usr/bin/env bash
# test-mac-script-mutations_test.sh — the committed mutation fixtures for
# tools/lib/test-mac-script_test.sh (#1855).
#
# WHY THIS FILE EXISTS. Six gates in .claude/skills/ir:test-mac/test-mac.sh
# were MOVED there out of SKILL.md's fenced bash blocks; four more were ADDED,
# and the review of this same PR added a further eleven CALL-SITE rows.
# Either way there is no "before the fix" to run red: the lock test passes the
# moment it is written. Per AGENTS.md's Testing section and
# docs/testing-philosophy.md it earns its place only by being seen to fail
# when the thing it protects is broken — so each gate is broken here, one at a
# time, and the lock test is required to go red naming that specific gate.
#
# Twenty-three mutations, applied and asserted SEPARATELY. A single combined mutation
# could go red on one gate while another was silently unguarded, which is the
# exact shape of defect this whole file exists to rule out. Each one is written
# as the plausible REGRESSION, not as arbitrary damage: the reachability abort
# demoted to a no-op, the backup refresh demoted to "make it once, ever", the
# `swift build` status read back through the pipe the way SKILL.md used to.
#
# The last row is not a gate inside the script at all — it is the `tools` gate
# that RUNS these two files, whose --changed trigger regex must cover the
# skill directory or a push touching only test-mac.sh skips both of them.
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
# COST. Each row runs the lock test scoped to the ONE case it is about (see
# red_case below). Running every case per row instead cost 194s here and took
# the whole `tools` gate from 116s to 327s — against a 540s budget the pre-push
# hook shares with every other gate, which is a regression for every push in
# the repo, not just this one. Measured with:
#     S=$(date +%s); MUTATION_FIXTURES_STRICT=1 bash tools/lib/test-mac-script-mutations_test.sh >/dev/null 2>&1; echo $(( $(date +%s) - S ))
#     S=$(date +%s); tools/preflight.sh --only tools >/dev/null 2>&1; echo $(( $(date +%s) - S ))
# Re-measure rather than trusting these numbers after adding rows.

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

# red_case <case> <label> <file> <anchor> <replacement> <want_match>
#
# Scopes the lock test to the ONE case a mutation is about. Without this every
# row re-ran every case: 194s for this file and 327s for the whole `tools`
# gate, against a 540s pre-push budget shared with every other gate (see COST
# above for the commands). $TESTMAC_CASE is the spelling used because
# mutation-assert.sh runs the lock test as a bare `bash <path>` and cannot
# append an argument.
#
# A case name that matches nothing makes the lock test REFUSE (exit 2) rather
# than pass over zero assertions, so a typo here cannot turn into a mutation
# that "went red" against a test that checked nothing.
red_case() {
  local c="$1"; shift
  export TESTMAC_CASE="$c"
  assert_mutation_is_red "$@"
  unset TESTMAC_CASE
}

# ── 1. The daemon-reachability gate is demoted to a no-op ───────────────────
# The failure it prevents is silent: the app finds no daemon on 7837, runs
# `pkill -x irrlichd`, and respawns one WITHOUT --record.
red_case "reachability" \
  "reachability gate: the abort never fires" \
  "$SUBJECT" \
  $'  if [[ -z "$READY" ]]; then\n    echo "ABORT: daemon never became reachable on $PORT' \
  $'  if false; then\n    echo "ABORT: daemon never became reachable on $PORT' \
  'GATE daemon-reachability: an unreachable daemon must abort'

# ── 2. The app-exit wait stops hard-aborting ────────────────────────────────
# The bundle is then overwritten while the process that has those files open is
# still alive.
red_case "app-exit" \
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
red_case "backup-refresh" \
  "backup freshness: an existing backup is trusted blindly" \
  "$SUBJECT" \
  $'    if printf \'%s\\n\' "$CODESIGN_INFO" | grep -q "^Authority=Developer ID Application"; then' \
  $'    if [[ ! -d "$PROD_BACKUP" ]] && printf \'%s\\n\' "$CODESIGN_INFO" | grep -q "^Authority=Developer ID Application"; then' \
  'GATE backup-freshness: the stale backup was NOT refreshed'

# ── 4. The no-safety-net refusal stops refusing ─────────────────────────────
red_case "backup-refuse" \
  "backup refusal: a dev-signed app with no backup is overwritten anyway" \
  "$SUBJECT" \
  $'    elif [[ ! -d "$PROD_BACKUP" ]]; then' \
  $'    elif false; then' \
  'GATE backup-refusal: dev-signed app + no backup must refuse'

# ── 5. The build-output existence check stops checking ──────────────────────
red_case "build-output" \
  "build-output existence: a missing product no longer stops the install" \
  "$SUBJECT" \
  $'  if [[ ! -x "$DEBUG_BIN" || ! -d "$DEBUG_SPARKLE" ]]; then' \
  $'  if false; then' \
  'GATE build-output-exists:'

# ── 6. The enum gains the default branch it was written without ─────────────
# A typo like `seperate` then silently runs the DESTRUCTIVE default instead of
# erroring — the exact hazard SKILL.md's step 0 named.
red_case "enum" \
  "enum validation: an unrecognised axis value is silently ignored" \
  "$SUBJECT" \
  $'    *)\n      echo "ERROR: unrecognised argument \'$arg\'." >&2' \
  $'    *)\n      : ;;\n    unreachable)\n      echo "ERROR: unrecognised argument \'$arg\'." >&2' \
  'would have silently run in replace mode'

# ── 7. The daemon is killed before the app ─────────────────────────────────
# A still-alive app then observes a daemon-less gap and spawns its own
# replacement, which can win the port race against the daemon started next.
red_case "kill-order" \
  "kill order: the daemon is killed first" \
  "$SUBJECT" \
  $'if want_app; then\n  echo "Stopping the app ($APP_KILL_PATTERN) …"' \
  $'if want_daemon && [[ "$MODE" == "replace" ]]; then\n  pkill -x "irrlichd" 2>/dev/null || true\nfi\nif want_app; then\n  echo "Stopping the app ($APP_KILL_PATTERN) …"' \
  'GATE kill-order: the daemon was killed at line'

# ── 8. Build freshness (ADDED by #1855) stops firing ───────────────────────
red_case "freshness" \
  "build freshness: a stale build product is installed anyway" \
  "$SUBJECT" \
  $'  if [[ -n "$STALE_SRC" ]]; then' \
  $'  if false; then' \
  'GATE build-freshness: a source newer than the build product must abort'

# ── 8b. ...and its own cannot-run refusal stops refusing ───────────────────
# A freshness check with nothing to compare against would otherwise report the
# same green as one that looked and found nothing.
red_case "freshness-vacuous" \
  "build freshness: an empty source set passes vacuously" \
  "$SUBJECT" \
  $'  if [[ "$SWIFT_SRC_COUNT" -eq 0 ]]; then' \
  $'  if false; then' \
  'a vacuous pass'

# ── 9. `swift build`'s status is read back through the pipe ────────────────
# This is SKILL.md's own pre-#1855 spelling (`swift build 2>&1 | tail -5`),
# whose exit status is tail's. A failed build reported success and the run
# walked into the install with whatever binary was lying around.
red_case "swift-status" \
  "swift build status: a failed build is reported as success by the pipe" \
  "$SUBJECT" \
  $'  if ! swift build --package-path "$REPO_ROOT/platforms/macos" 2>&1 | tail -5; then\n    echo "ERROR: swift build failed — not touching $APP_TARGET." >&2\n    exit 1\n  fi' \
  $'  swift build --package-path "$REPO_ROOT/platforms/macos" 2>&1 | tail -5 || true' \
  'GATE swift-build-status: a failed swift build must abort'

# ── 10. F1: the Developer-ID check goes back through a pipe ────────────────
# The regression this PR's own review found: `codesign … | grep -q` under
# `set -o pipefail` returns 141 (SIGPIPE — grep -q exits at its first match
# while the 28-line codesign output is still being written), so the branch is
# skipped EXACTLY WHEN THE APP IS GENUINELY SIGNED. Measured 5/5 against a real
# Developer-ID bundle. This row is why the codesign stub emits a realistic line
# count: a one-line stub cannot reproduce the SIGPIPE and passed regardless.
red_case "backup-refresh" \
  "F1a: the Developer-ID check goes straight back through a pipe" \
  "$SUBJECT" \
  $'    CODESIGN_INFO="$(codesign -dv --verbose=4 "$PROD_APP" 2>&1 || true)"\n    if [[ "$NL$CODESIGN_INFO" == *"${NL}Authority=Developer ID Application"* ]]; then' \
  $'    if codesign -dv --verbose=4 "$PROD_APP" 2>&1 | grep -q "^Authority=Developer ID Application"; then' \
  'GATE backup-freshness: the stale backup was NOT refreshed'

# ── 10b. ...and the INTERMEDIATE wrong fix, which is the subtle one ────────
# "Capture first, then pipe the capture into grep -q" looks like it fixes F1
# and does not: the producer is now `printf` instead of `codesign`, but it is
# still a producer racing a `grep -q` that exits early. It survives only while
# the captured text fits one pipe buffer, i.e. by headroom rather than by
# construction. This row is what forces the match to happen IN-SHELL.
red_case "backup-refresh" \
  "F1b: the captured output is piped into grep -q (fix by headroom, not construction)" \
  "$SUBJECT" \
  $'    if [[ "$NL$CODESIGN_INFO" == *"${NL}Authority=Developer ID Application"* ]]; then' \
  $'    if printf \'%s\\n\' "$CODESIGN_INFO" | grep -q "^Authority=Developer ID Application"; then' \
  'GATE backup-freshness: the stale backup was NOT refreshed'

# ── 11. ...and the same hazard in the teardown half ───────────────────────
red_case "restore-signed" \
  "F1: restore-prod.sh's Developer-ID check is piped into grep -q again" \
  ".claude/skills/ir:test-mac/restore-prod.sh" \
  $'elif { CODESIGN_INFO="$(codesign -dv --verbose=4 "$PROD_APP" 2>&1 || true)"\n       [[ "$NL$CODESIGN_INFO" == *"${NL}Authority=Developer ID Application"* ]]; }; then' \
  $'elif codesign -dv --verbose=4 "$PROD_APP" 2>&1 | grep -q "^Authority=Developer ID Application"; then' \
  'restore-prod: a genuine Developer-ID app with no backup'

# ── 12-14. CALL SITES: the daemon launch ──────────────────────────────────
# `--record` is the single value the reachability gate exists to protect, and
# the port is the value the whole fresh-shell bug was about. Both survived a
# mutation battery before the daemon-launch case existed (#1855 review F3).
red_case "daemon-launch" \
  "call site: --record is dropped from the daemon launch" \
  "$SUBJECT" \
  $'    IRRLICHT_BIND_ADDR="127.0.0.1:$PORT" \\\n      nohup "$IRRLICHTD_BIN" --record > "$LOG_DIR/irrlichd-dev.log" 2>&1 &' \
  $'    IRRLICHT_BIND_ADDR="127.0.0.1:$PORT" \\\n      nohup "$IRRLICHTD_BIN" > "$LOG_DIR/irrlichd-dev.log" 2>&1 &' \
  'started WITHOUT --record'

red_case "daemon-launch" \
  "call site: replace mode binds the wrong port" \
  "$SUBJECT" \
  $'  PORT=7837                                          # production port' \
  $'  PORT=7838                                          # production port' \
  'did not bind 127.0.0.1:7837'

# ── 15-16. CALL SITES: signing ────────────────────────────────────────────
red_case "sign-order" \
  "call site: the rpath fix-up is deleted" \
  "$SUBJECT" \
  $'  install_name_tool -add_rpath @executable_path/../Frameworks "$APP_TARGET/Contents/MacOS/Irrlicht"' \
  $'  : "rpath fix-up removed"' \
  'COULD NOT LOOK'

red_case "sign-order" \
  "call site: codesign loses --entitlements" \
  "$SUBJECT" \
  $'    codesign --force --deep --sign - --entitlements "$ENTITLEMENTS" "$APP_TARGET" 2>&1' \
  $'    codesign --force --deep --sign - "$APP_TARGET" 2>&1' \
  'codesign was called without --entitlements'

# ── 17-18. CALL SITES: separate mode's bundle assembly ────────────────────
# Half the MODE axis had no case at all before separate-full.
red_case "separate-full" \
  "call site: separate mode never copies the built binary in" \
  "$SUBJECT" \
  $'    cp "$DEBUG_BIN" "$APP_TARGET/Contents/MacOS/Irrlicht"\n    cp "$SWIFT_SRC_DIR/Resources/AppIcon.icns" "$APP_TARGET/Contents/Resources/AppIcon.icns"\n    # Embed Sparkle.framework' \
  $'    cp "$SWIFT_SRC_DIR/Resources/AppIcon.icns" "$APP_TARGET/Contents/Resources/AppIcon.icns"\n    # Embed Sparkle.framework' \
  'is not the built binary'

red_case "separate-full" \
  "call site: the dev app is launched without its port override" \
  "$SUBJECT" \
  $'    open --env IRRLICHT_DAEMON_PORT="$PORT" --env IRRLICHT_HOME="$DEV_HOME" \\' \
  $'    open \\' \
  'it would talk to PRODUCTION on 7837'

# ── 19-20. CALL SITES: the two quiet install writes ───────────────────────
red_case "install-details" \
  "call site: the icon refresh is dropped" \
  "$SUBJECT" \
  $'    cp "$SWIFT_SRC_DIR/Resources/AppIcon.icns" "$APP_TARGET/Contents/Resources/AppIcon.icns"\n    # Stamp the version string too' \
  $'    # Stamp the version string too' \
  'still holds the production copy'

red_case "install-details" \
  "call site: the stale-socket cleanup is dropped" \
  "$SUBJECT" \
  $'  rm -f "$SOCK"' \
  $'  : "socket cleanup removed"' \
  'the stale socket survived'

# ── 21. F5: the abort safety net stops firing ─────────────────────────────
red_case "abort-hint" \
  "F5: a failure after the bundle is overwritten says nothing" \
  "$SUBJECT" \
  $'trap on_exit EXIT' \
  $': "trap not installed"' \
  'never said so, nor pointed at restore-prod.sh'

# ── 22. The `tools` gate stops firing on this script's own directory ───────
# Not a gate inside the script, but the gate that RUNS the two files above.
# Without the trigger alternative added in #1855, a push that changes only
# test-mac.sh skips the whole `tools` group under `--changed`, and a gate that
# never ran is indistinguishable from one that found nothing.
red_case "preflight-trigger" \
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
