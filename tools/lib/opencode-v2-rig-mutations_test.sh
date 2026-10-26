#!/usr/bin/env bash
# opencode-v2-rig-mutations_test.sh — committed mutation fixture for #2109 P5a:
# the opencode-v2 recording column, routed to the opencode adapter.
#
# WHY THIS FILE EXISTS. P5a's defect tests were seen red on the base (the PR
# body carries the runs). What they cannot show is that each guard, map entry,
# row-shape check, tripwire and hook-drop-chain link the change ADDS reaches
# what it protects. Each mutation below breaks one and requires the named test
# to go red with the named message:
#
#   The column → adapter map (shard.DaemonAdapterForSlug) without opencode-v2,
#   graded once per consumer:
#    1. detectAdapter cannot infer an opencode-v2 fixture's adapter;
#    2. resolveParser falls back to the Claude Code parser;
#    3. `of hookcheck` reads "declares no hooks" for the column;
#    4. the viewer has no opencode-v2 column;
#    5. the shell copy no longer matches the Go map;
#    6. the rig-home row names an adapter the registry projection lacks.
#   The shell copy (lib/adapter-slug.sh) without opencode-v2:
#    7. IRRLICHT_RECORD_ADAPTERS names the slug — hook-drop link 1.
#   The rig-home row and its tripwires:
#    8. the row without XDG_DATA_HOME → the relocation test sees a half
#       relocation (the store stays real);
#    9. the driver's SERVICE_ENV_VARS without XDG_DATA_HOME → the no-launch
#       arm of the passthrough test;
#   10. a "service.json" literal in a non-test opencode adapter file → the
#       service-secret tripwire.
#   The hook-drop chain, links 2 and 3:
#   11. the unapplied-grants filter on the slug;
#   12. hook-install-wait's refusal filter on the slug;
#   13. hookcov.Declared without its column keys;
#   14. hook attribution comparing the raw slug.
#   The export and the passive version read:
#   15. the export without `$.type` → lines fall to the 1.x part switch;
#   16. resolve_cli_version ignoring "@store" → a binary is run for opencode-v2;
#   17. precheck.sh's opencode-v2 arm naming a binary → adapter-tables' tripwire.
#   The rig gates:
#   18. driver_launches_tmux claiming every driver launches tmux;
#   19. spawn_record_daemon without the live-socket refusal;
#   20. the XDG hook wait polling the whole shared base again.
#   21. agent_home_isolate without its half-relocation refusal → the half-set
#       run still exits 1 (the absolute-path check refuses the unset
#       variable's empty value, measured), but no longer names the half
#       relocation;
#   22. the state-store wipe without its production-home refusal (#2109
#       review finding 1);
#   23. the live relocation probe without its outranking-override clear,
#       under an exported OPENCODE_DB → the opencode-v2 row reads as a half
#       relocation (review finding 4).
#
# Corpus mutations live beside their subjects and are not repeated here:
# righome_corpus_test.go's TestGradeRelocationNamesEveryWrongShape (a half
# row, an unread variable, an unmarked XDG_STATE_HOME, a stale driver marker).
# The driver's own behavioural mutations are
# opencode-v2-driver-mutations_test.sh.
#
# tools/mutate.sh owns the mechanics: the stale/ambiguous-anchor guards and the
# byte-for-byte restore that never touches git state.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

command -v go >/dev/null 2>&1 || { echo "FAIL: opencode-v2-rig-mutations — go not found" >&2; exit 1; }
[[ -x "$MUTATE_SH" ]] || { echo "FAIL: opencode-v2-rig-mutations — $MUTATE_SH is missing or not executable" >&2; exit 1; }

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "opencode-v2-rig-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/opencode-v2-rig-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0
quote_output() { sed 's/^/      | /'; return 0; }

# judge <label> <mutate-rc> <output> <red-marker> <want>: mutate.sh applied and
# restored; the inner test went red (its own marker says so); and it went red
# for the named reason.
judge() {
  local label="$1" rc="$2" out="$3" red="$4" want="$5"
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating — not that the guard is fine."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if ! grep -qF -- "$red" <<<"$out"; then
    if grep -qE '(GO_TEST|SH_TEST)_RC=[1-9]' <<<"$out"; then
      echo "FAIL: $label — the test exited non-zero WITHOUT reporting a failure: it could not run"
      echo "      under the mutation, which is not evidence about the guard."
    else
      echo "FAIL: $label — the test stayed GREEN under the mutation, so the guard does not reach"
      echo "      what it claims to protect."
    fi
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:|syntax error' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD or the shell parse, not the guard."
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output; fails=$((fails + 1)); return
  fi
  echo "ok  $label"
}

assert_go_red() { # <label> <file> <anchor> <replacement> <pkg> <run> <want>
  local out rc
  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$2" "$3" "$4" \
    bash -c "go test $5 -run '$6' -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"; rc=$?
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then out="$out"$'\n'"(stayed green)"; fi
  judge "$1" "$rc" "$out" "--- FAIL" "$7"
}

assert_sh_red() { # <label> <file> <anchor> <replacement> <test-file> <want>
  local out rc
  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$2" "$3" "$4" \
    bash -c "bash $5 2>&1; echo SH_TEST_RC=\$?" 2>&1)"; rc=$?
  if grep -q 'SH_TEST_RC=0' <<<"$out"; then out="$out"$'\n'"(stayed green)"; fi
  judge "$1" "$rc" "$out" "FAIL:" "$6"
}

SHARD="tools/onboarding-factory/internal/shard/shard.go"
SLUG_SH="tools/onboarding-factory/scripts/lib/adapter-slug.sh"
AGENT_HOME="tools/onboarding-factory/scripts/lib/agent-home.sh"
DRIVER="replaydata/agents/opencode-v2/driver-interactive.sh"
SERVICE_API="replaydata/agents/opencode-v2/service-api.sh"
PID_GO="core/adapters/inbound/agents/opencode/pid.go"
UGC="tools/onboarding-factory/scripts/lib/unapplied-grants-check.sh"
HIW="tools/onboarding-factory/scripts/lib/hook-install-wait.sh"
HOOKCOV="tools/onboarding-factory/internal/hookcov/hookcov.go"
CLIVER="tools/onboarding-factory/scripts/lib/cli-version.sh"
PRECHECK="tools/onboarding-factory/scripts/precheck.sh"
TMUXCHK="tools/onboarding-factory/scripts/lib/tmux-teardown-check.sh"
SPAWN="tools/onboarding-factory/scripts/lib/spawn-record-daemon.sh"
LIB="tools/onboarding-factory/scripts/lib"
MAP_ENTRY=$'\t"opencode-v2":      "opencode",\n'

# ── 1–6. the Go column map without opencode-v2, once per consumer ──
assert_go_red "map without opencode-v2: detectAdapter" "$SHARD" "$MAP_ENTRY" "" \
  ./tools/onboarding-factory/cmd/replay/ '^TestDetectAdapter$' "cannot infer adapter from path"
assert_go_red "map without opencode-v2: resolveParser" "$SHARD" "$MAP_ENTRY" "" \
  ./tools/onboarding-factory/internal/replay/ '^TestResolveParser_mapsSlugToCanonical$' \
  'resolveParser("opencode-v2") canonical = "claude-code"; want "opencode"'
assert_go_red "map without opencode-v2: of hookcheck" "$SHARD" "$MAP_ENTRY" "" \
  ./tools/onboarding-factory/cmd/of/ '^TestHookCheckDeclaresAndHasEvent$' "declares_hooks: got false want true"
assert_go_red "map without opencode-v2: viewer columns" "$SHARD" "$MAP_ENTRY" "" \
  ./tools/onboarding-factory/internal/viewer/ '^TestCatalogColumnsComeFromTheCatalogThroughTheColumnMap$' \
  'agents has no "opencode-v2" column'
assert_go_red "map without opencode-v2: shell parity" "$SHARD" "$MAP_ENTRY" "" \
  ./tools/onboarding-factory/internal/shard/ '^TestShellSlugMapMatchesTheGoMap$' \
  'column "opencode-v2": the shell rig names adapter "opencode"'
assert_go_red "map without opencode-v2: rig-home registry join" "$SHARD" "$MAP_ENTRY" "" \
  ./tools/onboarding-factory/internal/righome/ '^TestRigHomeTableMatchesTheAdapterRegistry$' \
  'names adapter "opencode-v2", which is not in the daemon'"'"'s adapter registry'

# ── 7. the shell map without opencode-v2 (hook-drop link 1) ──
assert_sh_red "shell map without opencode-v2: IRRLICHT_RECORD_ADAPTERS" "$SLUG_SH" \
  $'    opencode-v2) printf \'%s\' "opencode" ;;\n' "" \
  "$LIB/spawn-record-daemon_test.sh" "FAIL: bare opencode-v2 slug -> opencode"

# ── 8. the row without XDG_DATA_HOME: a half relocation ──
assert_go_red "rig-home row without XDG_DATA_HOME" "$AGENT_HOME" \
  'opencode-v2 XDG_CONFIG_HOME,XDG_DATA_HOME,XDG_STATE_HOME:driver optin' \
  'opencode-v2 XDG_CONFIG_HOME,XDG_STATE_HOME:driver optin' \
  ./tools/onboarding-factory/internal/righome/ '^TestEveryRigHomeRowRelocatesBothHalves$' \
  "the session root still resolves"

# ── 9. the driver's SERVICE_ENV_VARS without XDG_DATA_HOME ──
assert_go_red "driver SERVICE_ENV_VARS without XDG_DATA_HOME" "$DRIVER" \
  'SERVICE_ENV_VARS="XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME"' \
  'SERVICE_ENV_VARS="XDG_CONFIG_HOME XDG_STATE_HOME"' \
  ./tools/onboarding-factory/internal/righome/ '^TestEveryRigHomeRowsDriverPassesTheHomeThroughTmux$' \
  "does not name XDG_DATA_HOME"

# ── 10. a service.json literal in the daemon's opencode adapter ──
assert_go_red "the daemon naming service.json" "$PID_GO" \
  'func servicePIDFor(transcriptPath string) (int, error) {' \
  $'var _ = "service.json"\n\nfunc servicePIDFor(transcriptPath string) (int, error) {' \
  ./tools/onboarding-factory/internal/righome/ '^TestTheOpenCodeDaemonNeverNamesTheServiceSecret$' \
  'names "service.json" in a string literal'

# ── 11–14. the hook-drop chain, links 2 and 3 ──
assert_sh_red "unapplied-grants filter on the slug" "$UGC" \
  '--arg a "$(adapter_slug_to_daemon_name "$adapter")" "$filter"' '--arg a "$adapter" "$filter"' \
  "$LIB/unapplied-grants-check_test.sh" "FAIL: opencode-v2 column refuses on the opencode adapter's unapplied grant"
assert_sh_red "hook-install-wait refusal filter on the slug" "$HIW" \
  'jq -r --arg a "$(adapter_slug_to_daemon_name "$adapter")" "$reasons_filter"' 'jq -r --arg a "$adapter" "$reasons_filter"' \
  "tools/lib/hook-install-wait_test.sh" "FAIL: …and names the opencode adapter's refusal"
assert_go_red "hookcov.Declared without its column keys" "$HOOKCOV" \
  $'\t\tif declares, ok := out[slug(shard.DaemonAdapterForSlug(col))]; ok {' $'\t\tif declares, ok := out[col]; ok {' \
  ./tools/onboarding-factory/cmd/of/ '^TestHookCheckDeclaresAndHasEvent$' "declares_hooks: got false want true"
assert_go_red "hook attribution comparing the raw slug" "$HOOKCOV" \
  'func owner(name string) string { return shard.DaemonAdapterForSlug(name) }' \
  'func owner(name string) string { return slug(name) }' \
  ./tools/onboarding-factory/cmd/of/ '^TestHookCheckDeclaresAndHasEvent$' "has_hook_event: got false want true"

# ── 15. the export without $.type ──
assert_go_red "export without \$.type" "$SERVICE_API" \
  "json_set(m.data, '\\\$.type', m.type, " "json_set(m.data, " \
  ./tools/onboarding-factory/cmd/replay/ '^TestOpenCodeV2ExportFeedsTheV2Parser$' "did not take the 2.x branch"

# ── 16–17. the passive version read ──
assert_sh_red "resolve_cli_version ignoring @store" "$CLIVER" \
  '  if [[ "$bin" == "$CLI_VERSION_FROM_STORE" ]]; then' '  if false; then' \
  "$LIB/cli-version_test.sh" "FAIL: reads the newest row's version"
assert_sh_red "precheck's opencode-v2 arm naming a binary" "$PRECHECK" \
  '  opencode-v2) CLI_BIN="@store"; VER_FIELD=0 ;;' '  opencode-v2) CLI_BIN="opencode"; VER_FIELD=1 ;;' \
  "$LIB/adapter-tables_test.sh" "FAIL: precheck.sh: opencode-v2's arm"

# ── 18–20. the rig gates ──
assert_sh_red "driver_launches_tmux claiming every driver launches" "$TMUXCHK" \
  $'  [[ -r "$driver" ]] || return 0\n' $'  return 0\n' \
  "$LIB/driver-launches-tmux_test.sh" "FAIL: an API driver launches no tmux"
assert_sh_red "spawn without the live-socket refusal" "$SPAWN" \
  $'  record_daemon_refuse_live_sock "$RECORD_DAEMON_SOCK" || return 1\n' "" \
  "$LIB/spawn-record-daemon_test.sh" "FAIL: the refusal names the socket"
assert_sh_red "XDG hook wait polling the whole shared base" "$HIW" \
  '        XDG_*_HOME) prefix="${!var}/$(adapter_slug_to_daemon_name "$adapter")" ;;' \
  '        XDG_*_HOME) prefix="${!var}" ;;' \
  "tools/lib/hook-install-wait_test.sh" "FAIL: opencode-v2: another tool's file under the shared XDG base is not waited on"

# ── 21. agent_home_isolate without its half-relocation refusal ──
assert_sh_red "agent_home_isolate without the half-relocation refusal" "$AGENT_HOME" \
  $'  if [[ -n "$unset_vars" ]]; then\n    echo "agent-home: $adapter — HALF-relocated:' \
  $'  if false; then\n    echo "agent-home: $adapter — HALF-relocated:' \
  "$LIB/agent-home_test.sh" "FAIL: half set → names the half relocation"

# ── 22. the wipe without its production-home refusal ──
assert_sh_red "state-store wipe without the production-home refusal" "$SPAWN" \
  '  if [[ "$real" == "$prod" ]]; then' '  if false; then' \
  "$LIB/spawn-record-daemon_test.sh" "FAIL: the production default home is refused"

# ── 23. the probe leaving OPENCODE_DB in place ──
# The variable is exported for this one call only: it is what a runner that
# followed the opencode 1.x column's habits would carry.
OPENCODE_DB=/nonexistent/2109-mutation.db assert_go_red "relocation probe keeping an exported OPENCODE_DB" \
  "tools/onboarding-factory/internal/righome/righome_test.go" \
  $'\toverrides := outrankingOverrides[shard.DaemonAdapterForSlug(row.Adapter)]' $'\toverrides := []string{}' \
  ./tools/onboarding-factory/internal/righome/ '^TestEveryRigHomeRowRelocatesBothHalves$' \
  "the session root still resolves"

if [[ $fails -gt 0 ]]; then
  echo "opencode-v2-rig-mutations: $fails FAILED"
  exit 1
fi
echo "opencode-v2-rig-mutations: ALL PASS"
