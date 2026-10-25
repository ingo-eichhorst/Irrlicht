#!/usr/bin/env bash
# opencode-v2-reader-mutations_test.sh — committed mutation fixture for issue
# #2106: opencode 2.x sessions are read from session_v2 / session_message and
# bound to their client process.
#
# WHY THIS FILE EXISTS. #2106's defect tests were seen red on the base (the PR
# body carries the run): no 2.x session was admitted, ComputeMetrics read
# nothing, the parser skipped every 2.x row, StorePath ignored OPENCODE_DB and
# XDG_DATA_HOME, and DiscoverPID bound a $HOME session to the 2.x service and
# found no `opencode2` client. What those tests cannot show is that each guard
# the change ADDS reaches what it protects. Each mutation below breaks one and
# requires the named test to go red with the named message:
#
#   1. the watcher's schema selector never picks the 2.x reader → no 2.x
#      session is admitted.
#   2. ComputeMetrics' schema selector never picks the 2.x reader → it reads
#      nothing from a 2.x store.
#   3. the activity cursor is seq, not (time_updated, id) → an assistant row
#      rewritten in place (same seq) is not activity.
#   4. first sight is activity → a row 2.x imported from a 1.x store is
#      admitted on its imported state.
#   5. an idle row does not end the turn → the activity is never Terminal.
#   6. DiscoverPID's service predicate never matches → a session in $HOME
#      binds to the 2.x service.
#   7. LiveClientCWDs' service predicate never matches → the service's $HOME
#      reads as a client's directory.
#   8. the matcher spells "space or end" `\s` → macOS pgrep finds no
#      `opencode2` client (its ERE has no `\s`). Darwin only: Linux matches
#      with Go's regexp, which has it, so there the mutation is not a defect.
#   9. a schema switch keeps the other reader's cursors → a 1.x session
#      migrated under a running watcher replays its imported row as activity.
#  10. a failed first-sight read leaves an unseeded cursor → the next scan
#      reads the session's stored history as activity and admits it.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on opencode-v2-harmless-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: opencode-v2-reader-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: opencode-v2-reader-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "opencode-v2-reader-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/opencode-v2-reader-mutations_test.sh" >&2
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

OPENCODE="./core/adapters/inbound/agents/opencode/"
PROCLIFE="./core/adapters/inbound/agents/processlifecycle/"
WATCHER="core/adapters/inbound/agents/opencode/watcher.go"
WATCHER_V2="core/adapters/inbound/agents/opencode/watcher_v2.go"
METRICS="core/adapters/inbound/agents/opencode/metrics.go"
PID="core/adapters/inbound/agents/opencode/pid.go"
ADAPTER="core/adapters/inbound/agents/opencode/adapter.go"

# ── 1. the watcher's selector never picks the 2.x reader ──
assert_go_test_goes_red \
  "scanSessions never selecting the 2.x reader" \
  "$WATCHER" \
  $'if isV2 {\n\t\tw.scanV2(db)' \
  $'if isV2 && false {\n\t\tw.scanV2(db)' \
  "$OPENCODE" \
  '^TestV2Reader_AdmitsLiveSessionsOnTheirTurn$' \
  "0 EventNewSession, want 1"

# ── 2. ComputeMetrics' selector never picks the 2.x reader ──
assert_go_test_goes_red \
  "ComputeMetrics never selecting the 2.x reader" \
  "$METRICS" \
  $'if isV2 {\n\t\treturn queryV2SessionMetrics(db, sid)' \
  $'if isV2 && false {\n\t\treturn queryV2SessionMetrics(db, sid)' \
  "$OPENCODE" \
  '^TestComputeMetrics_V2Store$' \
  "= nil, want the session's metrics"

# ── 3. a seq cursor ──
assert_go_test_goes_red \
  "scanV2Messages with a seq cursor" \
  "$WATCHER_V2" \
  $'SELECT id, type, time_updated\n\t\tFROM session_message\n\t\tWHERE session_id = ?\n\t\t  AND time_updated >= ?\n\t\tORDER BY time_updated ASC, id ASC' \
  $'SELECT id, type, seq\n\t\tFROM session_message\n\t\tWHERE session_id = ?\n\t\t  AND seq >= ?\n\t\tORDER BY seq ASC, id ASC' \
  "$OPENCODE" \
  '^TestV2Reader_AnAssistantRowUpdatedInPlaceIsActivity$' \
  "the in-place rewrite of seq 5 produced 0 activity event(s)"

# ── 4. first sight counts as activity ──
assert_go_test_goes_red \
  "scanV2Session admitting on first sight" \
  "$WATCHER_V2" \
  'case !known, !acc.hasActivity:' \
  'case !acc.hasActivity:' \
  "$OPENCODE" \
  '^TestV2Reader_ImportedV1RowWaitsForNewActivity$' \
  "rows already in the migrated store produced"

# ── 5. an idle row does not end the turn ──
assert_go_test_goes_red \
  "scanV2Messages ignoring the idle row" \
  "$WATCHER_V2" \
  'if acc.observe(id, timeUpdated, cur, lastTS) && typ == v2TypeIdle {' \
  'if acc.observe(id, timeUpdated, cur, lastTS) && false {' \
  "$OPENCODE" \
  '^TestV2Reader_AdmitsLiveSessionsOnTheirTurn$' \
  "want exactly one Terminal EventActivity"

# ── 6. DiscoverPID binds to the service ──
assert_go_test_goes_red \
  "DiscoverPID without the service predicate" \
  "$PID" \
  'processCmdPattern, cwd, disambiguate, IsServiceArgv)' \
  'processCmdPattern, cwd, disambiguate, func([]string) bool { return false })' \
  "$PROCLIFE" \
  '^TestOpenCodeDiscoverPID_BindsClientsNotTheService$' \
  "service in \$HOME is not a client"

# ── 7. the live-client set includes the service ──
assert_go_test_goes_red \
  "LiveClientCWDs without the service predicate" \
  "$PID" \
  'LiveCWDsByCmdline(processCmdPattern, IsServiceArgv)' \
  'LiveCWDsByCmdline(processCmdPattern, func([]string) bool { return false })' \
  "$PROCLIFE" \
  '^TestOpenCodeLiveClientCWDs_ExcludesTheService$' \
  "service in \$HOME is not a client"

# ── 8. `\s` in the matcher (darwin only) ──
if [[ "$(uname -s)" == "Darwin" ]]; then
  assert_go_test_goes_red \
    "processCmdPattern spelling space-or-end as \\s" \
    "$ADAPTER" \
    'const processCmdPattern = `^([^ ]*/)?opencode(2|\.exe)?( |$)`' \
    'const processCmdPattern = `^([^ ]*/)?opencode(2|\.exe)?(\s|$)`' \
    "$OPENCODE" \
    '^TestDiscoverPID_FindsAnOpencode2ClientOnThisHost$' \
    "want the opencode2 process"
else
  echo "skip  processCmdPattern \\s mutation — $(uname -s) matches with Go's regexp, which has \\s;"
  echo "      the defect is macOS pgrep's dialect (test.yml runs this on macos-latest)"
fi

# ── 9. a schema switch keeps the other reader's cursors ──
assert_go_test_goes_red \
  "storeSchema without the cursor reset" \
  "$WATCHER" \
  $'\t\tw.mu.Lock()\n\t\tw.cursors = make(map[string]*sessionCursor)\n\t\tw.mu.Unlock()\n' \
  '' \
  "$OPENCODE" \
  '^TestScanSessions_MigrationUnderARunningWatcherReplaysNothing$' \
  "the migrated, untouched 1.x session produced 1 event(s)"

# ── 10. a failed first-sight read leaves an unseeded cursor ──
assert_go_test_goes_red \
  "scanV2Session keeping an unseeded cursor after a failed read" \
  "$WATCHER_V2" \
  $'\t\t\tdelete(w.cursors, s.id)\n' \
  '' \
  "$OPENCODE" \
  '^TestV2Reader_AFailedFirstSightDoesNotSeedHistoryAsActivity$' \
  "stored history produced"

if [[ $fails -gt 0 ]]; then
  echo "opencode-v2-reader-mutations: $fails FAILED"
  exit 1
fi
echo "opencode-v2-reader-mutations: ALL PASS"
