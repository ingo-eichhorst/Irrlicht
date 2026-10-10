#!/usr/bin/env bash
# codex-appserver-placeholder-mutations_test.sh — committed mutation fixture for
# issue #2082: codex app-server processes get no placeholder row, and a TUI's
# placeholder goes once a newer codex root in its cwd is bound to an app-server.
#
# WHY THIS FILE EXISTS. The red-first tests for #2082 (the codex declaration,
# the scanner wiring, and the prompt retirement) were seen red on the base.
# What they cannot show is that each guard the fix ADDS reaches what it
# protects: the locks for those guards pass on the base by construction. Each
# mutation below breaks one guard and requires the named test to go red with
# the named message:
#
#   1. a placeholder first seen after the root is paired with it → the scope
#      lock's "placeholder newer than the root" row goes red: a TUI opened
#      with no thread yet loses its row.
#   2. a root's pairing is forgotten between sweeps → the two-TUI lock goes red
#      on sweep 2: one root retires a second placeholder.
#   3. a root pairs with every eligible placeholder → the two-TUI lock goes red
#      on sweep 1.
#   4. the root's bound PID need not be an app-server → the scope lock's "root
#      bound to a TUI" row goes red.
#   5. the observe-consent gate is dropped → the scope lock's consent row goes
#      red.
#   6. an adapter without its own release probe is paired → the scope lock's
#      "adapter declares no release probe" row goes red.
#   7. a placeholder on the root's own PID is paired → the own-PID lock goes
#      red: the TUI placeholder keeps its row.
#   8. the infra reaper's release-probe exemption is dropped → the idle-root
#      lock goes red: an idle codex root on the app-server is reaped.
#   9. the predicate reads argv[1] only → the codex table's VS Code row goes
#      red.
#  10. the predicate excludes a short or unreadable argv → the codex table's
#      unreadable-argv row goes red.
#  11. codex declares no ExcludeArgv → the wiring test goes red: the scanner
#      has no argv filter.
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

# ── 1. a placeholder newer than the root is paired ──
assert_go_test_goes_red \
  "a placeholder first seen after the root paired with it" \
  "$PM_FILE" \
  'if paired[i] || p.FirstSeen >= root.FirstSeen || p.PID == root.PID {' \
  'if paired[i] || p.PID == root.PID {' \
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
  $'victims = append(victims, hostedVictim{placeholder: p, root: root.SessionID})\n\t\t\tbreak' \
  $'victims = append(victims, hostedVictim{placeholder: p, root: root.SessionID})' \
  "$SERVICES" \
  "$PER_ROOT" \
  "after sweep 1 placeholder #1 (oldest first) present = false, want true"

# ── 4. the root's PID need not be an app-server ──
assert_go_test_goes_red \
  "a root paired without its bound PID's argv being the adapter's host" \
  "$PM_FILE" \
  'if claims[root.SessionID] || !exclude(pm.readArgv(root.PID)) {' \
  'if claims[root.SessionID] || exclude == nil {' \
  "$SERVICES" \
  "$SCOPE/^root_bound_to_a_TUI,_not_an_app-server\$" \
  "was retired within its grace period"

# ── 5. no consent gate ──
assert_go_test_goes_red \
  "hosted-root pairing ignoring the consent gate" \
  "$PM_FILE" \
  'if len(g.placeholders) == 0 || !pm.observeAllowed(g.adapter) {' \
  'if len(g.placeholders) == 0 {' \
  "$SERVICES" \
  "$SCOPE/^observe_consent_withheld\$" \
  "was retired within its grace period"

# ── 6. an adapter without its own release probe is paired ──
assert_go_test_goes_red \
  "hosted-root pairing for an adapter without a release probe" \
  "$PM_FILE" \
  'if s.CWD == "" || !pm.probesRelease(s.Adapter) {' \
  'if s.CWD == "" {' \
  "$SERVICES" \
  "$SCOPE/^adapter_declares_no_release_probe\$" \
  "was retired within its grace period"

# ── 7. a placeholder on the root's own PID is paired ──
assert_go_test_goes_red \
  "a placeholder on the root's own PID using up its pairing" \
  "$PM_FILE" \
  'if paired[i] || p.FirstSeen >= root.FirstSeen || p.PID == root.PID {' \
  'if paired[i] || p.FirstSeen >= root.FirstSeen {' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_CodexPlaceholderOnTheRootsOwnPIDLeavesItsPairing$' \
  "the root's pairing went to the placeholder on its own pid"

# ── 8. the infra reaper's exemption is dropped ──
assert_go_test_goes_red \
  "the infra reaper ending a root its adapter's host still hosts" \
  "$PM_FILE" \
  $'\tif pm.probesRelease(snap.adapter) {\n\t\treturn false\n\t}' \
  $'\tif false {\n\t\treturn false\n\t}' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_IdleCodexRootOnAppServerIsNotReapedAsInfra$' \
  "was reaped as an infra-bound ghost"

# ── 9. argv[1] only ──
assert_go_test_goes_red \
  "codex.IsAppServerArgv reading argv[1] only" \
  "$CODEX_FILE" \
  'return slices.Contains(argv[1:], "app-server")' \
  'return slices.Contains(argv[1:2], "app-server")' \
  "$CODEX" \
  '^TestAgentExcludesCodexAppServerProcesses$' \
  "VS Code extension app-server (pid 3766): ExcludeArgv("

# ── 10. a short or unreadable argv is excluded ──
assert_go_test_goes_red \
  "codex.IsAppServerArgv excluding an unreadable argv" \
  "$CODEX_FILE" \
  $'\tif len(argv) < 2 {\n\t\treturn false' \
  $'\tif len(argv) < 2 {\n\t\treturn true' \
  "$CODEX" \
  '^TestAgentExcludesCodexAppServerProcesses$' \
  "unreadable argv: ExcludeArgv("

# ── 11. codex declares no ExcludeArgv ──
assert_go_test_goes_red \
  "codex without an ExcludeArgv declaration" \
  "$CODEX_FILE" \
  $'\t\t\tExcludeArgv:    IsAppServerArgv,\n' \
  '' \
  "./core/cmd/irrlichd/" \
  '^TestCodexScannerCarriesArgvFilter$' \
  "the codex scanner has no argv filter"

if [[ $fails -gt 0 ]]; then
  echo "codex-appserver-placeholder-mutations: $fails FAILED"
  exit 1
fi
echo "codex-appserver-placeholder-mutations: ALL PASS"
