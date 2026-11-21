#!/usr/bin/env bash
# workerlist-mutations_test.sh — the committed mutation fixtures for #2148's
# new guards, none of which has a "before the fix" to run red
# (docs/testing-philosophy.md: anything a change ADDS owes a deliberate
# mutation instead):
#
#   - the orchestrator monitor's registration scoping (a revoked
#     orchestrator's snapshot is never served, and a late one never replaces
#     a live one) and its subscribe-before-return —
#     core/application/services/orchestrator_monitor.go;
#   - the worker-list watcher being built only in the permission's Apply —
#     core/cmd/irrlichd/startup.go;
#   - the dashboard's first-claim rule, State.Name, the stale and last_exit
#     copies onto the worker row and the group-name collision rule —
#     core/domain/session/grouped.go;
#   - the worker-list adapter's staleness thresholds (slot and team), schema
#     refusal, duplicate-slot refusal, keep-last-good, directory watch,
#     re-evaluation tick, newest-snapshot broadcast, no read after a revoke,
#     and detection probe — core/adapters/inbound/orchestrators/workerlist/.
#
# Each was first run by hand during #2148 and recorded in its PR; this file is
# the same perturbations, committed so they re-run (AGENTS.md: "Commit that
# mutation as a fixture"). Same assert_go_test_goes_red shape as
# tools/lib/cost-unattributed-mutations_test.sh, same tools/mutate.sh
# underneath.
#
# Not here, on purpose:
#   - the web mutations (stale / last_exit in structureSignature, exit 0 never
#     "failed"): tools/mutate.sh refuses platforms/web/irrlicht.js, which
#     carries a literal NUL byte (#1816), so they were run byte-exact by hand
#     and are recorded in the PR;
#   - the Swift mutations (CodingKeys, failedExitCode): one `swift test` run
#     costs minutes, more than this shell-lib gate can spend per fixture.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests.
need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: workerlist-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}

quote_output() {
  sed 's/^/      | /'
  return 0
}
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: workerlist-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses a dirty tree; a dirty tree must not silently pass where
# the answer is load-bearing (see cost-unattributed-mutations_test.sh).
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "workerlist-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/workerlist-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
#
# Applies one mutation and requires the named Go test to FAIL under it, for
# the expected reason: a green test means the guard does not reach what it
# protects, and a red one caused by a build failure proves nothing.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -count=1 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
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
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard."
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

MON=core/application/services/orchestrator_monitor.go
WL=core/adapters/inbound/orchestrators/workerlist
GROUPED=core/domain/session/grouped.go

# ── monitor: a revoked orchestrator's snapshot is served ───────────────────
assert_go_test_goes_red \
  "State serving a snapshot whose registration ended reddens the at-once drop test" \
  "$MON" \
  $'\tif rs, ok := m.states[name]; ok && rs.live() {' \
  $'\tif rs, ok := m.states[name]; ok {' \
  "./core/application/services/" \
  "TestOrchestratorMonitor_RegistrationEndDropsStateAtOnce" \
  "still served right after its registration ended"

# ── monitor: a late snapshot replaces a live registration's ─────────────────
assert_go_test_goes_red \
  "storing an ended registration's late snapshot reddens the live-snapshot test" \
  "$MON" \
  $'\tif rs.live() {\n\t\tm.states[rs.state.Adapter] = rs' \
  $'\tif true {\n\t\tm.states[rs.state.Adapter] = rs' \
  "./core/application/services/" \
  "TestOrchestratorMonitor_EndedSnapshotDoesNotReplaceLiveOne" \
  "want the live registration's snapshot"

# ── consent: the watcher runs before the grant ──────────────────────────────
assert_go_test_goes_red \
  "starting the worker-list watcher when its effects are built reddens pending_no_effect" \
  "core/cmd/irrlichd/startup.go" \
  'func workerlistEffects(orchCtx context.Context, orchMonitor *services.OrchestratorMonitor, path string, logger outbound.Logger) (start, stop func() error) {' \
  $'func workerlistEffects(orchCtx context.Context, orchMonitor *services.OrchestratorMonitor, path string, logger outbound.Logger) (start, stop func() error) {\n\tdefer func() { _ = start() }()' \
  "./core/cmd/irrlichd/" \
  "TestWorkerlistPermission_GateContract" \
  'effect observed while permission "state" is pending'

# ── dashboard: a later orchestrator steals a claimed session ────────────────
assert_go_test_goes_red \
  "letting a later orchestrator's claim overwrite an earlier one reddens the shared-session test" \
  "$GROUPED" \
  'if _, claimed := merged[id]; !claimed {' \
  'if _, claimed := merged[id]; true || !claimed {' \
  "./core/domain/session/" \
  "TestBuildDashboard_FirstOrchestratorKeepsASharedSession" \
  "Gas Town rig agents"

# ── dashboard: a pool beside Gas Town changes Gas Town's group ─────────────
assert_go_test_goes_red \
  "filing every claimed session under the last orchestrator changes Gas Town's group" \
  "$GROUPED" \
  'p.orchs[wi.orch].add(wi, agent)' \
  'p.orchs[len(p.orchs)-1].add(wi, agent)' \
  "./core/domain/session/" \
  "TestBuildDashboard_PoolBesideGasTownLeavesGasTownUnchanged" \
  "Gas Town group changed when a pool was passed beside it"

# ── dashboard: the stale indication never reaches the row ───────────────────
assert_go_test_goes_red \
  "dropping the Stale copy onto the unattached worker reddens the pool test" \
  "$GROUPED" \
  $'\t\t\t\tStale:       w.Stale,\n' \
  '' \
  "./core/domain/session/" \
  "TestBuildDashboard_PoolListsEverySlotWithItsIndications" \
  "busy stale slot: stale = <nil>, want true"

# ── workerlist: a heartbeat exactly at the threshold reads as stale ─────────
assert_go_test_goes_red \
  "comparing slot staleness with >= reddens the stale-heartbeat replay at exactly T" \
  "$WL/document.go" \
  'out.Stale = teamStale || now.Sub(ref) > threshold' \
  'out.Stale = teamStale || now.Sub(ref) >= threshold' \
  "./$WL/" \
  "TestWorkerlistReplay" \
  "TestWorkerlistReplay/stale-heartbeat"

# ── workerlist: the same mutation against the boundary table ────────────────
assert_go_test_goes_red \
  "comparing slot staleness with >= reddens the boundary table's exactly-T heartbeat row" \
  "$WL/document.go" \
  'out.Stale = teamStale || now.Sub(ref) > threshold' \
  'out.Stale = teamStale || now.Sub(ref) >= threshold' \
  "./$WL/" \
  "TestBuildState_StalenessBoundary" \
  "TestBuildState_StalenessBoundary/heartbeat_exactly_T"

# ── workerlist: an unknown schema is accepted ───────────────────────────────
assert_go_test_goes_red \
  "accepting schema 2 reddens the keep-last-good test's schema row" \
  "$WL/document.go" \
  'case *d.Schema != schemaVersion:' \
  'case *d.Schema != schemaVersion && *d.Schema != 2:' \
  "./$WL/" \
  "TestAdapter_UnreadableKeepsLastGoodSnapshot" \
  "TestAdapter_UnreadableKeepsLastGoodSnapshot/schema_2"

# ── workerlist: an unreadable file hides the pool ───────────────────────────
assert_go_test_goes_red \
  "dropping the keep-last-good branch reddens the unreadable-document test" \
  "$WL/adapter.go" \
  $'\t\treturn markUnreadable(buildState(a.lastGood, now))' \
  $'\t\treturn notRunning(now)' \
  "./$WL/" \
  "TestAdapter_UnreadableKeepsLastGoodSnapshot" \
  "want the last good snapshot"

# ── workerlist: the file, not its directory, is watched ─────────────────────
assert_go_test_goes_red \
  "watching the file instead of its directory misses a status file created after the grant" \
  "$WL/adapter.go" \
  $'\tdir := filepath.Dir(a.path)\n' \
  $'\tdir := a.path\n' \
  "./$WL/" \
  "TestAdapter_WatchSeesFileCreatedAfterStart" \
  "created file: not observed"

# ── workerlist: the detection probe trusts the variable alone ───────────────
assert_go_test_goes_red \
  "reporting detected whenever IRRLICHT_WORKERLIST_FILE is set reddens the probe test" \
  "$WL/permission.go" \
  'return fileExists(ResolvePath(dataDir))' \
  'return os.Getenv(EnvFile) != "" || fileExists(ResolvePath(dataDir))' \
  "./$WL/" \
  "TestFileDetected" \
  "detected although the file the variable names is missing"

# ── monitor: the first snapshot is broadcast before anyone subscribed ──────
assert_go_test_goes_red \
  "subscribing inside the drain goroutine again reddens the subscribe-before-return test" \
  "$MON" \
  $'\tch := w.Subscribe()\n\tgo m.drainWatcher(ctx, w, ch)' \
  $'\tgo func() { m.drainWatcher(ctx, w, w.Subscribe()) }()' \
  "./core/application/services/" \
  "TestOrchestratorMonitor_AddWatcherSubscribesBeforeReturning" \
  "AddWatcher returned with 0 subscriptions"

# ── dashboard: the adapter's group name is ignored ──────────────────────────
assert_go_test_goes_red \
  "ignoring State.Name names the pool \"Gas Town\"" \
  "$GROUPED" \
  $'\tcase orch.Name != "":\n\t\treturn orch.Name\n' \
  $'\tcase false:\n\t\treturn orch.Name\n' \
  "./core/domain/session/" \
  "TestBuildDashboard_PoolListsEverySlotWithItsIndications" \
  'pool group name = Gas Town, want "Build pool"'

# ── dashboard: the last-run outcome never reaches the row ───────────────────
assert_go_test_goes_red \
  "dropping the LastExit copy onto the unattached worker reddens the pool test" \
  "$GROUPED" \
  $'\t\t\t\tLastExit:    w.LastExit,\n' \
  '' \
  "./core/domain/session/" \
  "TestBuildDashboard_PoolListsEverySlotWithItsIndications" \
  "busy slot: last_exit = <nil>, want 2"

# ── dashboard: a pool takes a project's group key ───────────────────────────
assert_go_test_goes_red \
  "dropping the name-collision rule gives a pool a project's group name" \
  "$GROUPED" \
  $'\tif orch.Name != "" && taken[name] {' \
  $'\tif false && orch.Name != "" && taken[name] {' \
  "./core/domain/session/" \
  "TestBuildDashboard_PoolNameNeverCollidesWithAnotherGroup" \
  "top-level group names ="

# ── workerlist: a team exactly at the threshold reads as stale ──────────────
assert_go_test_goes_red \
  "comparing team staleness with >= reddens the exactly-T team row" \
  "$WL/document.go" \
  'teamStale := now.Sub(*t.Updated) > threshold' \
  'teamStale := now.Sub(*t.Updated) >= threshold' \
  "./$WL/" \
  "TestBuildState_StalenessBoundary" \
  "TestBuildState_StalenessBoundary/team_updated_exactly_T"

# ── workerlist: a repeated slot name is accepted ────────────────────────────
assert_go_test_goes_red \
  "accepting a repeated slot name reddens the duplicate-worker row" \
  "$WL/document.go" \
  $'\t\tcase seen[w.Name]:' \
  $'\t\tcase false && seen[w.Name]:' \
  "./$WL/" \
  "TestDecodeDocument_RefusesInvalid" \
  "TestDecodeDocument_RefusesInvalid/duplicate_worker"

# ── workerlist: any `state` value is accepted (#2145) ───────────────────────
assert_go_test_goes_red \
  "accepting any state value reddens the unknown-state row" \
  "$WL/document.go" \
  $'\t\tcase w.State != "" && w.State != workerWaiting:' \
  $'\t\tcase false && w.State != "" && w.State != workerWaiting:' \
  "./$WL/" \
  "TestDecodeDocument_RefusesInvalid" \
  "TestDecodeDocument_RefusesInvalid/worker_with_an_unknown_state"

# ── workerlist: a free slot may be waiting (#2145) ──────────────────────────
assert_go_test_goes_red \
  "accepting a waiting free slot reddens the waiting-free row" \
  "$WL/document.go" \
  $'\t\tcase w.State == workerWaiting && !*w.Busy:' \
  $'\t\tcase false && w.State == workerWaiting && !*w.Busy:' \
  "./$WL/" \
  "TestDecodeDocument_RefusesInvalid" \
  "TestDecodeDocument_RefusesInvalid/waiting_worker_that_is_free"

# ── workerlist: a waiting slot reaches the wire as busy (#2145) ─────────────
assert_go_test_goes_red \
  "dropping the waiting token reddens the waiting-slot unit test" \
  "$WL/document.go" \
  $'\t\t\tout.State = workerWaiting' \
  $'\t\t\tout.State = workerBusy' \
  "./$WL/" \
  "TestBuildState_WaitingSlot" \
  "TestBuildState_WaitingSlot/fresh_heartbeat"
assert_go_test_goes_red \
  "dropping the waiting token reddens the waiting-slot replay scenario" \
  "$WL/document.go" \
  $'\t\t\tout.State = workerWaiting' \
  $'\t\t\tout.State = workerBusy' \
  "./$WL/" \
  "TestWorkerlistReplay" \
  "TestWorkerlistReplay/waiting-slot"

# ── workerlist: the tick no longer re-reads ─────────────────────────────────
assert_go_test_goes_red \
  "removing the tick's re-read leaves a dead pool fresh forever" \
  "$WL/adapter.go" \
  'reread, tick = true, true' \
  'tick = true' \
  "./$WL/" \
  "TestAdapter_TickTurnsSlotStaleWithoutFileChange" \
  "slot stale after the threshold: not observed"

# ── workerlist: a slow subscriber keeps the oldest snapshot ─────────────────
assert_go_test_goes_red \
  "keeping the pending snapshot in broadcast hands a slow subscriber a stale one" \
  "$WL/adapter.go" \
  $'\t\tselect {\n\t\tcase <-ch:\n\t\tdefault:\n\t\t}\n' \
  '' \
  "./$WL/" \
  "TestAdapter_SlowSubscriberGetsNewestSnapshot" \
  "want the newest (p-2)"

# ── workerlist: a Watch started after a revoke reads the file ───────────────
assert_go_test_goes_red \
  "dropping Watch's first cancellation check reads the file after a revoke" \
  "$WL/adapter.go" \
  $'\t// A revoke can land before this goroutine first runs; read nothing then.\n\tif ctx.Err() != nil {\n\t\treturn ctx.Err()\n\t}\n' \
  '' \
  "./$WL/" \
  "TestAdapter_WatchAfterCancellationReadsNothing" \
  "Watch on a cancelled context read the file"

# ── workerlist: a replay with nothing to replay passes ──────────────────────
# Not a source mutation: the fixture directory is pointed at an empty one
# through WORKERLIST_FIXTURES_DIR, and the replay must fail rather than pass
# having checked nothing.
empty_dir="$(mktemp -d)"
out="$(cd "$REPO_ROOT" && WORKERLIST_FIXTURES_DIR="$empty_dir" \
  go test "./$WL/" -run TestWorkerlistReplay -count=1 2>&1; echo "GO_TEST_RC=$?")"
rmdir "$empty_dir"
if grep -q 'GO_TEST_RC=0' <<<"$out" || ! grep -qF "no scenarios under" <<<"$out"; then
  echo "FAIL: an empty scenarios directory must fail the replay with \"no scenarios under\""
  echo "$out" | quote_output
  fails=$((fails + 1))
else
  echo "ok  an empty scenarios directory fails the replay"
fi

if [[ $fails -gt 0 ]]; then
  echo "workerlist-mutations: $fails FAILED"
  exit 1
fi
echo "workerlist-mutations: ALL PASS"
