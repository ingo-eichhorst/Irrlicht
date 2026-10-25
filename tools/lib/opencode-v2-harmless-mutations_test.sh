#!/usr/bin/env bash
# opencode-v2-harmless-mutations_test.sh — committed mutation fixture for issue
# #2105: after a plain upgrade to opencode 2.x, irrlicht does no harm.
#
# WHY THIS FILE EXISTS. #2105's defect tests were seen red on the base (the PR
# body carries the runs): a 2.1.4 CLI passed the Min-only hook gate, the 1.x
# plugin file survived a grant against it, the 1.x reader admitted a frozen row
# from a migrated store and ran 1.x SQL against a fresh one, and the scanner
# minted a placeholder row for `serve --service`. What those tests cannot show
# is that each guard the fix ADDS reaches what it protects — the ceiling in
# agent.VersionGate.Check, its contract arm and disclosure clause, the
# service's ceiling-only removal, the store stand-down, and the service
# predicate are all new. Each mutation below breaks one guard and requires the
# named test to go red with the named message:
#
#   1. VersionGate.Check ignores Max → opencode's wiring of the shared
#      AssertHookVersionGate contract goes red (the arm grades domain code no
#      wiring can get wrong, so its mutation is here, in the domain).
#   2. the service never removes after a ceiling refusal → the 1.x plugin
#      survives a grant against opencode 2.1.4.
#   3. a ceiling refusal is an untyped error → the service cannot tell it from
#      a floor refusal and removes nothing.
#   4. the service treats a gate as declared only when it has a floor → a
#      ceiling-only gate installs at 2.1.4.
#   5. the watcher scans a 2.x store anyway → a frozen 1.x row is admitted.
#   6. ComputeMetrics reads a 2.x store anyway → it folds a frozen 1.x row.
#   7. opencode's SessionHostArgv never matches → the scanner mints a
#      placeholder row for the service.
#   8. --prompt is dropped from the value-taking options → `opencode --prompt
#      serve --service` (a client) reads as the service.
#   9. the disclosure arm ignores a declared ceiling → copy stating only "X or
#      newer" for a ceilinged gate passes.
#  10. opencode's copy states only the floor → opencode's real disclosure
#      wiring goes red.
#  11. the contract arm accepts an unparseable ceiling → the "ceiling that
#      never refuses" self-test goes red.
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
    echo "FAIL: opencode-v2-harmless-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: opencode-v2-harmless-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "opencode-v2-harmless-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/opencode-v2-harmless-mutations_test.sh" >&2
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

DOMAIN_FILE="core/domain/agent/declaration.go"
SERVICE_FILE="core/application/services/permission_service.go"
OPENCODE="./core/adapters/inbound/agents/opencode/"
OPENCODE_AGENT="core/adapters/inbound/agents/opencode/agent.go"
SERVICES="./core/application/services/"
CONTRACTS="./core/internal/contracttesting/"

# ── 1. VersionGate.Check ignores Max ──
assert_go_test_goes_red \
  "agent.VersionGate.Check without the ceiling" \
  "$DOMAIN_FILE" \
  'if g.Max != "" {' \
  'if false && g.Max != "" {' \
  "$OPENCODE" \
  '^TestHooksPermission_DeclaresVersionGate$' \
  "as at or above its ceiling 2.0.0"

# ── 2. the service never removes after a ceiling refusal ──
assert_go_test_goes_red \
  "runClosureEffect without the ceiling removal" \
  "$SERVICE_FILE" \
  $'} else {\n\t\t\terr = s.removeAfterCeilingRefusal(e, err)\n\t\t}' \
  '}' \
  "$SERVICES" \
  '^TestOpenCodeCeiling_RemovesTheStaleV1Plugin$' \
  "survived a grant against opencode 2.1.4"

# ── 3. a ceiling refusal is an untyped error ──
assert_go_test_goes_red \
  "hookVersionRefusal returning an untyped ceiling refusal" \
  "$SERVICE_FILE" \
  'return &ceilingRefusal{why: why}' \
  'return errors.New(why)' \
  "$SERVICES" \
  '^TestHookVersionGate_CeilingRefusesAndRemovesTheStaleArtifact$' \
  "Writes.Uninstall ran 0 time(s), want 1"

# ── 4. a gate counts as declared only with a floor ──
assert_go_test_goes_red \
  "hookVersionRefusal skipping a ceiling-only gate" \
  "$SERVICE_FILE" \
  '(p.Writes.Version.Min == "" && p.Writes.Version.Max == "")' \
  'p.Writes.Version.Min == ""' \
  "$SERVICES" \
  '^TestHookVersionGate_CeilingAloneIsEnforced$' \
  "Apply ran at 2.1.4 against a gate whose only bound is a 2.0.0 ceiling"

# ── 5. the watcher scans a 2.x store anyway ──
assert_go_test_goes_red \
  "scanSessions without the stand-down" \
  "core/adapters/inbound/agents/opencode/watcher.go" \
  'if w.standsDownForV2(db) {' \
  'if w.standsDownForV2(db) && false {' \
  "$OPENCODE" \
  '^TestScanSessions_MigratedV2StoreAdmitsNoFrozenV1Row$' \
  "EventNewSession count = 1, want 0"

# ── 6. ComputeMetrics reads a 2.x store anyway ──
assert_go_test_goes_red \
  "ComputeMetrics without the stand-down" \
  "core/adapters/inbound/agents/opencode/metrics.go" \
  'if isV2, err := storeIsV2(db); err != nil || isV2 {' \
  'if isV2, err := storeIsV2(db); false && (err != nil || isV2) {' \
  "$OPENCODE" \
  '^TestComputeMetrics_MigratedV2StoreReadsNothing$' \
  "ComputeMetrics returned nil = false, want true"

# ── 7. opencode's SessionHostArgv never matches ──
assert_go_test_goes_red \
  "opencode's SessionHostArgv stubbed to false" \
  "$OPENCODE_AGENT" \
  'SessionHostArgv: IsServiceArgv,' \
  'SessionHostArgv: func([]string) bool { return false },' \
  "./core/adapters/inbound/agents/processlifecycle/" \
  '^TestOpenCodeServiceGetsNoPlaceholderRow$' \
  "was minted a placeholder row"

# ── 8. --prompt is not a value-taking option ──
assert_go_test_goes_red \
  "opencode.IsServiceArgv without --prompt's value" \
  "core/adapters/inbound/agents/opencode/serviceargv.go" \
  $'\t"--prompt":      true,\n' \
  '' \
  "$OPENCODE" \
  '^TestAgentDeclaresTheV2ServiceAsSessionHost$' \
  "serve as the value of --prompt: SessionHostArgv("

# ── 9. the disclosure arm ignores a declared ceiling ──
assert_go_test_goes_red \
  "assertStatesVersionFloor without the ceiling clause" \
  "core/internal/contracttesting/hook_disclosure.go" \
  'if gate.Max != "" && !strings.Contains(disclosure, gate.Max) {' \
  'if false && gate.Max != "" && !strings.Contains(disclosure, gate.Max) {' \
  "$CONTRACTS" \
  '^TestDisclosureArm_StatesTheVersionCeiling$' \
  "a declared ceiling the consent copy never mentions (#2105)"

# ── 10. opencode's copy states only the floor ──
assert_go_test_goes_red \
  "opencode's consent copy without the ceiling" \
  "$OPENCODE_AGENT" \
  'hookjson.RequiresVersionRange("opencode", minOpenCodeVersion, maxOpenCodeVersion) +' \
  'hookjson.RequiresVersion("opencode", minOpenCodeVersion) +' \
  "$OPENCODE" \
  '^TestHooksPermission_DisclosureMatchesInstalled$' \
  "consent copy never states the 2.0.0 ceiling"

# ── 11. the contract arm accepts an unparseable ceiling ──
assert_go_test_goes_red \
  "assertCeilingRefusesNewer accepting an unparseable Max" \
  "core/internal/contracttesting/hook_version.go" \
  $'ceiling, ok := cliversion.Parse(gate.Max)\n\tif !ok {' \
  $'ceiling, ok := cliversion.Parse(gate.Max)\n\tif !ok && false {' \
  "$CONTRACTS" \
  '^TestVersionArm_CeilingRefusesANewerCLI$/^a_ceiling_that_never_refuses$' \
  "an unparseable ceiling, which fails open on every comparison"

if [[ $fails -gt 0 ]]; then
  echo "opencode-v2-harmless-mutations: $fails FAILED"
  exit 1
fi
echo "opencode-v2-harmless-mutations: ALL PASS"
