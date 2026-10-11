#!/usr/bin/env bash
# codex-appserver-placeholder-mutations_test.sh — committed mutation fixture for
# issue #2082: codex app-server processes get no placeholder row, and a TUI's
# placeholder goes once a newer codex root in its cwd is bound to an app-server.
#
# WHY THIS FILE EXISTS. The red-first tests for #2082 were seen red on the
# base: the codex scanner had no argv filter, and a TUI's placeholder stayed
# beside a root bound to the app-server. What they cannot show is that each
# guard the fix ADDS reaches what it protects: the locks for those guards pass
# on the base by construction, and SessionHostArgv, SkipsPreSession and
# IsAppServerArgv are new. Each mutation below breaks one guard and requires
# the named test to go red with the named message:
#
#   0. the sweep never runs the hosted pairing → the red-first test goes red:
#      the placeholder survives beside its root.
#   1. a placeholder first seen well after the root is paired → the scope
#      lock's "placeholder newer than the root" row goes red: a TUI opened
#      with no thread yet loses its row.
#   2. a root's pairing is forgotten between sweeps → the two-TUI lock goes red
#      on sweep 2: one root retires a second placeholder.
#   3. a root pairs with every eligible placeholder → the two-TUI lock goes red
#      on sweep 1.
#   4. the root's bound PID need not be a session host → the scope lock's
#      "root bound to a TUI" row goes red.
#   5. the observe-consent gate is dropped → the scope lock's consent row goes
#      red.
#   7. a placeholder on the root's own PID is paired → the own-PID lock goes
#      red: the TUI placeholder keeps its row.
#   8. codex also names the app-server in ExcludeArgv → the idle-root lock
#      goes red: the #727 infra reaper ends an idle hosted root.
#   9. the predicate reads argv[1] only → the codex table's VS Code row goes
#      red.
#  10. the predicate matches a short or unreadable argv → the codex table's
#      unreadable-argv row goes red.
#  11. codex declares no SessionHostArgv → the wiring test goes red: the
#      scanner has no argv filter.
#  12. no mint lag: a placeholder must be first seen no later than its root →
#      the mint-lag test's 4s row goes red: a TUI launched with a prompt keeps
#      its placeholder for the grace period.
#  13. an option's value counts as the subcommand → the codex table's
#      `--cd app-server` row goes red.
#  14. the scanner wiring ignores SessionHostArgv → the wiring test goes red.
#  15. SkipsPreSession ignores SessionHostArgv → its table's session-host row
#      goes red.
#  16. the diagnostics process landscape reads ExcludeArgv alone → the
#      landscape test goes red: the app-server is not flagged infra.
#  17. agents.SessionHosts projects no adapter → the opt-in pin goes red:
#      startup would hand the PID manager no session host.
#
# Not covered: the startup.go line that passes agents.SessionHosts to
# SetSessionHosts is reached by no test, like its SetInfraReaper and
# SetHostGate neighbours.
#
# There is no mutation 6: which adapter's session host applies is a map
# index (pm.sessionHosts[s.Adapter]), not a guard; the scope lock's "adapter
# declares no session host" row pins it.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on codex-released-root-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: codex-appserver-placeholder-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: codex-appserver-placeholder-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "codex-appserver-placeholder-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/codex-appserver-placeholder-mutations_test.sh" >&2
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
PM_FILE="core/application/services/pid_manager.go"
CODEX_FILE="core/adapters/inbound/agents/codex/agent.go"
SCOPE='^TestCheckPIDLiveness_CodexPlaceholderPromptRetirementScope$'
PER_ROOT='^TestCheckPIDLiveness_CodexPlaceholdersRetiredAtMostOnePerRoot$/^two_TUIs,_one_root$'
CODEX_TABLE='^TestAgentDeclaresCodexAppServerAsSessionHost$'
PAIR_LINE='if paired[i] || p.FirstSeen > root.FirstSeen+mintLag || p.PID == root.PID {'

# ── 0. the sweep never runs the hosted pairing ──
assert_go_test_goes_red \
  "the periodic sweep without the hosted pairing" \
  "$PM_FILE" \
  $'\tvictims = append(victims, pm.hostedPlaceholderVictims(states)...)\n' \
  '' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_CodexPlaceholderRetiredOnceItsRootBindsToAppServer$' \
  "survived a sweep after a newer codex root in its cwd bound to the app-server daemon"

# ── 1. a placeholder newer than the root is paired ──
assert_go_test_goes_red \
  "a placeholder first seen well after the root paired with it" \
  "$PM_FILE" \
  "$PAIR_LINE" \
  'if paired[i] || mintLag < 0 || p.PID == root.PID {' \
  "$SERVICES" \
  "$SCOPE/^placeholder_newer_than_the_root\$" \
  "was retired within its grace period"

# ── 2. a root's pairing is forgotten between sweeps ──
assert_go_test_goes_red \
  "hosted-root claims not carried across sweeps" \
  "$PM_FILE" \
  'if pm.hostClaims[root.SessionID] {' \
  'if false && pm.hostClaims[root.SessionID] {' \
  "$SERVICES" \
  "$PER_ROOT" \
  "after sweep 2 placeholder #1 (oldest first) present = false, want true"

# ── 3. a root pairs with every eligible placeholder ──
assert_go_test_goes_red \
  "a hosted root pairing with more than one placeholder" \
  "$PM_FILE" \
  $'reason: "bound to its adapter\'s session host"})\n\t\t\tbreak' \
  $'reason: "bound to its adapter\'s session host"})' \
  "$SERVICES" \
  "$PER_ROOT" \
  "after sweep 1 placeholder #1 (oldest first) present = false, want true"

# ── 4. the root's PID need not be a session host ──
assert_go_test_goes_red \
  "a root paired without its bound PID being its adapter's session host" \
  "$PM_FILE" \
  'if claims[root.SessionID] || !g.isHost(pm.hostReadArgv(root.PID)) {' \
  'if claims[root.SessionID] || g.isHost == nil {' \
  "$SERVICES" \
  "$SCOPE/^root_bound_to_a_TUI,_not_an_app-server\$" \
  "was retired within its grace period"

# ── 5. no consent gate ──
assert_go_test_goes_red \
  "hosted-root pairing ignoring the consent gate" \
  "$PM_FILE" \
  'if !pm.observeAllowed(s.Adapter) {' \
  'if false {' \
  "$SERVICES" \
  "$SCOPE/^observe_consent_withheld\$" \
  "was retired within its grace period"

# ── 7. a placeholder on the root's own PID is paired ──
assert_go_test_goes_red \
  "a placeholder on the root's own PID using up its pairing" \
  "$PM_FILE" \
  "$PAIR_LINE" \
  'if paired[i] || p.FirstSeen > root.FirstSeen+mintLag {' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_CodexPlaceholderOnTheRootsOwnPIDLeavesItsPairing$' \
  "the root's pairing went to the placeholder on its own pid"

# ── 8. codex also names the app-server in ExcludeArgv ──
assert_go_test_goes_red \
  "codex declaring the app-server as ExcludeArgv" \
  "$CODEX_FILE" \
  $'\t\t\tSessionHostArgv: IsAppServerArgv,\n' \
  $'\t\t\tSessionHostArgv: IsAppServerArgv,\n\t\t\tExcludeArgv: IsAppServerArgv,\n' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_IdleCodexRootOnAppServerIsNotReapedAsInfra$' \
  "was reaped as an infra-bound ghost"

# ── 9. argv[1] only ──
assert_go_test_goes_red \
  "codex.IsAppServerArgv reading argv[1] only" \
  "$CODEX_FILE" \
  'for i := 1; i < len(argv); i++ {' \
  'for i := 1; i < len(argv) && i < 2; i++ {' \
  "$CODEX" \
  "$CODEX_TABLE" \
  "VS Code extension app-server (pid 3766): SessionHostArgv("

# ── 10. a short or unreadable argv matches ──
assert_go_test_goes_red \
  "codex.IsAppServerArgv matching an unreadable argv" \
  "$CODEX_FILE" \
  $'\t}\n\treturn false\n}\n\n// valueOptions' \
  $'\t}\n\treturn len(argv) < 2\n}\n\n// valueOptions' \
  "$CODEX" \
  "$CODEX_TABLE" \
  "unreadable argv: SessionHostArgv("

# ── 11. codex declares no SessionHostArgv ──
assert_go_test_goes_red \
  "codex without a SessionHostArgv declaration" \
  "$CODEX_FILE" \
  $'\t\t\tSessionHostArgv: IsAppServerArgv,\n' \
  '' \
  "./core/cmd/irrlichd/" \
  '^TestCodexScannerCarriesArgvFilter$' \
  "the codex scanner has no argv filter"

# ── 12. no mint lag ──
assert_go_test_goes_red \
  "a placeholder minted just after its root left for the grace period" \
  "$PM_FILE" \
  'mintLag := int64(hostedPlaceholderMintLag / time.Second)' \
  'mintLag := int64(0)' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_CodexPlaceholderMintedJustAfterItsRootIsRetired$/^4s$' \
  "minted 4s after its root survived a sweep"

# ── 13. an option's value counts as the subcommand ──
assert_go_test_goes_red \
  "codex.IsAppServerArgv matching an option's value" \
  "$CODEX_FILE" \
  'if argv[i] == "app-server" && !valueOptions[argv[i-1]] {' \
  'if argv[i] == "app-server" {' \
  "$CODEX" \
  "$CODEX_TABLE" \
  "TUI with --cd app-server: SessionHostArgv("

# ── 14. the scanner wiring ignores SessionHostArgv ──
assert_go_test_goes_red \
  "the scanner wiring without SessionHostArgv" \
  "core/cmd/irrlichd/wiring.go" \
  'if a.Process.ExcludeArgv != nil || a.Process.SessionHostArgv != nil {' \
  'if a.Process.ExcludeArgv != nil {' \
  "./core/cmd/irrlichd/" \
  '^TestCodexScannerCarriesArgvFilter$' \
  "the codex scanner has no argv filter"

# ── 15. SkipsPreSession ignores SessionHostArgv ──
assert_go_test_goes_red \
  "agent.Process.SkipsPreSession without SessionHostArgv" \
  "core/domain/agent/declaration.go" \
  '(p.SessionHostArgv != nil && p.SessionHostArgv(argv))' \
  '(p.SessionHostArgv != nil && false)' \
  "./core/domain/agent/" \
  '^TestProcessSkipsPreSession$' \
  "session host: SkipsPreSession("

# ── 16. the diagnostics landscape reads ExcludeArgv alone ──
assert_go_test_goes_red \
  "the diagnostics process landscape without SessionHostArgv" \
  "core/application/services/diagnostics_service.go" \
  'procs = append(procs, s.procInfo(pid, a.Process.SkipsPreSession, red))' \
  'procs = append(procs, s.procInfo(pid, a.Process.ExcludeArgv, red))' \
  "$SERVICES" \
  '^TestDiagnosticsFlagsASessionHostOnlyInTheLandscape$' \
  "should be flagged infra in the process landscape"

# ── 17. the SessionHosts projection drops every adapter ──
assert_go_test_goes_red \
  "agents.SessionHosts projecting no adapter" \
  "core/adapters/inbound/agents/maps.go" \
  'if a.Process.SessionHostArgv != nil {' \
  'if false && a.Process.SessionHostArgv != nil {' \
  "./core/adapters/inbound/agents/" \
  '^TestSessionHosts_OptInSet$' \
  "session hosts = [], want [codex]"

if [[ $fails -gt 0 ]]; then
  echo "codex-appserver-placeholder-mutations: $fails FAILED"
  exit 1
fi
echo "codex-appserver-placeholder-mutations: ALL PASS"
