#!/usr/bin/env bash
# codex-tui-launcher-mutations_test.sh — committed mutation fixture for issue
# #2083: a codex root hosted by the shared app-server daemon takes its launcher
# from the one codex TUI in its cwd, and keeps the daemon's otherwise.
#
# WHY THIS FILE EXISTS. The red-first tests for #2083 were seen red against a
# seam that was declared but not consulted: the hosted root kept the daemon's
# launcher, and the startup backfill read the daemon. What they cannot show is
# that each guard the fix ADDS reaches what it protects: the locks pass by
# construction, and Process.LauncherPID, codex.LauncherPID and
# processlifecycle.PIDsByCWDExcludingArgv are new. Each mutation below breaks
# one guard and requires the named test to go red with the named message:
#
#   1. "exactly one TUI" relaxed to "the first TUI" → the codex table's
#      two-TUI row goes red: an ambiguous cwd guesses.
#   2. a failed TUI scan read as its partial answer → the scan-failed row goes
#      red.
#   3. the bound process need not be a listening app-server → the row for a
#      root bound to its own TUI goes red: a --no-daemon root takes a
#      neighbour's launcher.
#   4. any app-server counts as listening → the stdio app-server row goes red:
#      a VS Code extension thread takes a terminal TUI's launcher.
#   5. `--listen stdio://` and `--listen off` count as listening → the
#      `--listen stdio` row goes red.
#   6. a lone candidate with an unreadable argv is taken → its row goes red.
#   7. launcher capture never consults the hook → the PID manager's red-first
#      test goes red.
#   8. the hook runs without observe consent → the consent lock goes red.
#   9. the startup backfill treats a hosted root like any other → the
#      ambiguous-cwd row goes red: the daemon's fields are merged into the
#      last unique attribution.
#  10. an undecided hook falls through to a read of the daemon → the same row
#      goes red.
#  11. a read from a different terminal is merged instead of replacing → the
#      lone-TUI row goes red: the daemon's terminal keeps the TUI's tty.
#  12. the hosted check reads argv without observe consent → the backfill's
#      consent row goes red.
#  13. agents.LauncherPIDs projects no adapter → the opt-in pin goes red.
#  14. codex declares no LauncherPID → the declaration test goes red.
#  15. PIDsByCWDExcludingArgv skips a candidate whose cwd it cannot read →
#      its unreadable-cwd row goes red: a skipped TUI reads as one fewer.
#  16. PIDsByCWDExcludingArgv keeps the daemon's own PID → its every-match row
#      goes red.
#  17. PIDsByCWDExcludingArgv compares the cwd as given → its every-match row
#      goes red: a symlinked spelling of the cwd matches nothing.
#
# Not covered: the startup.go line that passes agents.LauncherPIDs to
# SetLauncherPIDs is reached by no test, like its SetSessionHosts and
# SetInfraReaper neighbours. Which adapter's hook applies is a map index
# (pm.launcherPIDs[state.Adapter]), not a guard; the PID manager's
# other-adapters lock pins it.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on codex-appserver-placeholder-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: codex-tui-launcher-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: codex-tui-launcher-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "codex-tui-launcher-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/codex-tui-launcher-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running this mutation)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -race -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating — it does NOT mean the"
    echo "      guard is fine."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard. A fixture that"
    echo "      cannot compile proves nothing about the behavior it is meant to exercise."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

SERVICES="./core/application/services/"
CODEX="./core/adapters/inbound/agents/codex/"
PROCLIFE="./core/adapters/inbound/agents/processlifecycle/"
PM_FILE="core/application/services/pid_manager.go"
LAUNCHER_FILE="core/adapters/inbound/agents/codex/launcher.go"
DISCOVERY_FILE="core/adapters/inbound/agents/processlifecycle/discovery.go"
RULE='^TestLauncherPID$'
SEED='^TestSeedPIDs_HostedCodexRootLauncherReevaluation$'
SCAN='^TestPIDsByCWDExcludingArgv$'
UNIQUE_LINE='if err != nil || len(tuis) != 1 {'

# ── 1. exactly one → first match ──
assert_go_test_goes_red \
  "codex.LauncherPID taking the first of several TUIs" \
  "$LAUNCHER_FILE" \
  "$UNIQUE_LINE" \
  'if err != nil || len(tuis) == 0 {' \
  "$CODEX" \
  "$RULE/^two_TUIs_in_the_cwd\$" \
  "--- FAIL: TestLauncherPID/two_TUIs_in_the_cwd"

# ── 2. a failed scan read as its answer ──
assert_go_test_goes_red \
  "codex.LauncherPID trusting a TUI scan that failed" \
  "$LAUNCHER_FILE" \
  "$UNIQUE_LINE" \
  'if _ = err; len(tuis) != 1 {' \
  "$CODEX" \
  "$RULE/^TUI_scan_failed\$" \
  "--- FAIL: TestLauncherPID/TUI_scan_failed"

# ── 3. the bound process need not be a listening app-server ──
assert_go_test_goes_red \
  "codex.LauncherPID for a root not bound to an app-server" \
  "$LAUNCHER_FILE" \
  'if pid <= 0 || cwd == "" || !listensForClients(readArgv(pid)) {' \
  'if pid <= 0 || cwd == "" {' \
  "$CODEX" \
  "$RULE/^root_bound_to_its_own_TUI\$" \
  "scanned for TUIs although the bound process is not a listening app-server"

# ── 4. any app-server counts as listening ──
assert_go_test_goes_red \
  "codex.LauncherPID treating a stdio app-server as a TUI host" \
  "$LAUNCHER_FILE" \
  $'\t}\n\treturn false\n}\n' \
  $'\t}\n\treturn true\n}\n' \
  "$CODEX" \
  "$RULE/^root_bound_to_a_stdio_app-server\$" \
  "--- FAIL: TestLauncherPID/root_bound_to_a_stdio_app-server"

# ── 5. stdio:// and off count as listening ──
assert_go_test_goes_red \
  "codex.LauncherPID treating --listen stdio:// as a socket" \
  "$LAUNCHER_FILE" \
  'return url != "" && url != "stdio://" && url != "off"' \
  'return url != ""' \
  "$CODEX" \
  "$RULE/^--listen_stdio\$" \
  "--- FAIL: TestLauncherPID/--listen_stdio"

# ── 6. a lone candidate nothing is known about ──
assert_go_test_goes_red \
  "codex.LauncherPID taking a lone candidate with an unreadable argv" \
  "$LAUNCHER_FILE" \
  'if len(readArgv(tuis[0])) == 0 {' \
  'if false {' \
  "$CODEX" \
  "$RULE/^lone_candidate_argv_unreadable\$" \
  "--- FAIL: TestLauncherPID/lone_candidate_argv_unreadable"

# ── 7. capture never consults the hook ──
assert_go_test_goes_red \
  "launcher capture reading the bound PID only" \
  "$PM_FILE" \
  'if l, _ := pm.launcherEnv(pm.launcherSourcePID(state, pid)); l != nil {' \
  'if l, _ := pm.launcherEnv(pid); l != nil {' \
  "$SERVICES" \
  '^TestHandlePIDAssigned_HostedCodexRootTakesItsTUIsLauncher$' \
  "want the TUI's"

# ── 8. the hook without observe consent ──
assert_go_test_goes_red \
  "the LauncherPID hook ignoring the consent gate" \
  "$PM_FILE" \
  'if hook == nil || !pm.observeAllowed(state.Adapter) {' \
  'if hook == nil {' \
  "$SERVICES" \
  '^TestHandlePIDAssigned_LauncherPIDHookIsConsentGated$' \
  "hook ran 1 times with observe consent withheld"

# ── 9. the backfill treats a hosted root like any other ──
assert_go_test_goes_red \
  "the startup backfill without the hosted-root re-evaluation" \
  "$PM_FILE" \
  'if pm.boundToSessionHost(state) {' \
  'if false && pm.boundToSessionHost(state) {' \
  "$SERVICES" \
  "$SEED/^TUI_launcher,_ambiguous_cwd\$" \
  "want no read"

# ── 10. an undecided hook reads the daemon ──
assert_go_test_goes_red \
  "the hosted re-evaluation reading the daemon when undecided" \
  "$PM_FILE" \
  $'\tsrc := pm.launcherSourcePID(state, state.PID)\n\tif src == state.PID {\n\t\treturn\n\t}\n' \
  $'\tsrc := pm.launcherSourcePID(state, state.PID)\n' \
  "$SERVICES" \
  "$SEED/^TUI_launcher,_ambiguous_cwd\$" \
  "want no read"

# ── 11. a different terminal merged instead of replacing ──
assert_go_test_goes_red \
  "the hosted re-evaluation merging a different terminal's read" \
  "$PM_FILE" \
  'if fresh.TTY != state.Launcher.TTY {' \
  'if false {' \
  "$SERVICES" \
  "$SEED/^daemon_launcher,_lone_TUI\$" \
  "--- FAIL: TestSeedPIDs_HostedCodexRootLauncherReevaluation/daemon_launcher,_lone_TUI"

# ── 12. the hosted check without observe consent ──
assert_go_test_goes_red \
  "the hosted-root check reading argv without observe consent" \
  "$PM_FILE" \
  'return pm.observeAllowed(state.Adapter) && isHost(pm.hostReadArgv(state.PID))' \
  'return isHost(pm.hostReadArgv(state.PID))' \
  "$SERVICES" \
  "$SEED/^observe_consent_withheld\$" \
  "--- FAIL: TestSeedPIDs_HostedCodexRootLauncherReevaluation/observe_consent_withheld"

# ── 13. the projection drops every adapter ──
assert_go_test_goes_red \
  "agents.LauncherPIDs projecting no adapter" \
  "core/adapters/inbound/agents/maps.go" \
  'if a.Process.LauncherPID != nil {' \
  'if false && a.Process.LauncherPID != nil {' \
  "./core/adapters/inbound/agents/" \
  '^TestLauncherPIDs_OptInSet$' \
  "launcher PIDs = [], want [codex]"

# ── 14. codex declares no LauncherPID ──
assert_go_test_goes_red \
  "codex without a LauncherPID declaration" \
  "core/adapters/inbound/agents/codex/agent.go" \
  $'\t\t\tLauncherPID: LauncherPID,\n' \
  '' \
  "$CODEX" \
  '^TestAgentDeclaresLauncherPID$' \
  "codex declares no Process.LauncherPID"

# ── 15. an unreadable cwd skipped ──
assert_go_test_goes_red \
  "PIDsByCWDExcludingArgv skipping an unreadable cwd" \
  "$DISCOVERY_FILE" \
  'return nil, fmt.Errorf("cwd of %s pid %d: %w", processName, pid, err)' \
  'continue' \
  "$PROCLIFE" \
  "$SCAN/^unreadable_cwd\$" \
  "--- FAIL: TestPIDsByCWDExcludingArgv/unreadable_cwd"

# ── 16. the daemon's own PID kept ──
assert_go_test_goes_red \
  "PIDsByCWDExcludingArgv keeping the daemon's own PID" \
  "$DISCOVERY_FILE" \
  $'\tfor _, pid := range withoutExcludedArgv(pids, excludeArgv) {\n\t\tif pid == myPID {\n\t\t\tcontinue\n\t\t}\n' \
  $'\tfor _, pid := range withoutExcludedArgv(pids, excludeArgv) {\n\t\t_ = myPID\n' \
  "$PROCLIFE" \
  "$SCAN/^every_match\$" \
  "--- FAIL: TestPIDsByCWDExcludingArgv/every_match"

# ── 17. the cwd compared as given ──
assert_go_test_goes_red \
  "PIDsByCWDExcludingArgv comparing a symlinked cwd as given" \
  "$DISCOVERY_FILE" \
  $'\tcwd = canonicalCWD(cwd)\n' \
  '' \
  "$PROCLIFE" \
  "$SCAN/^every_match\$" \
  "--- FAIL: TestPIDsByCWDExcludingArgv/every_match"

if [[ $fails -gt 0 ]]; then
  echo "codex-tui-launcher-mutations: $fails FAILED"
  exit 1
fi
echo "codex-tui-launcher-mutations: ALL PASS"
