#!/usr/bin/env bash
# opencode-v2-service-mutations_test.sh — committed mutation fixture for issue
# #2107: opencode 2.x sessions with no client process (made through the
# service's HTTP API, the desktop app or the web app) are admitted, bound to the
# one `opencode serve --service` process, share its PID, and end.
#
# WHY THIS FILE EXISTS. #2107's defect tests were seen red on the base (the PR
# body carries the run): a client-less session was never admitted and bound to
# no PID, two of them on the service's PID evicted each other on every same-PID
# path, an idle one outlived the ready TTL while the service lived, and a
# service-bound root retired a TUI's placeholder. What those tests cannot show
# is that each guard the change ADDS reaches what it protects, or that the
# locks it relies on still bite. Each mutation below breaks one and requires the
# named test to go red with the named message:
#
#   1. opencode declares no SharedPIDOwner → client-less roots on the service
#      evict each other.
#   2. OwnsSharedPID skips the argv check → two roots on one TUI both survive,
#      where 1.x's exclusive supersession keeps only the newer.
#   3. OwnsSharedPID skips the session_v2 row → an archived root keeps the
#      service's PID forever.
#   4. DiscoverPID has no service fallback → a client-less session binds to
#      nothing.
#   5. the fallback binds the first of several services → it guesses.
#   6. the fallback skips the session_v2 row → an archived session, an unknown
#      one and a 1.x store's session bind to a 2.x service.
#   7. PIDsByCmdlineKeepingArgv ignores its predicate → every opencode process
#      counts as a service, and with clients running none is bound.
#   8. the watcher's admission ignores the service → a client-less session is
#      never admitted.
#   9. the ready TTL keeps protecting a root on a live host → an idle
#      client-less root outlives the TTL.
#  10. the host rule ignores ReleasedPID → an idle codex root its app-server
#      still holds is reaped (codex's lock).
#  11. the host rule ignores the bound PID's argv → an idle root on a TUI is
#      reaped.
#  12. the host rule ignores observe consent → it reads argv it may not.
#  13. the hosted-placeholder pairing ignores LauncherPID → a service-bound
#      root retires an unrelated TUI's placeholder.
#  14. admitStaleTranscript asks the writer probe about a DB-backed key → the
#      exemption the SharedPIDOwner doc states is gone (the lock's red).
#  15. launcher capture reads a service-bound root's host → every client-less
#      session takes the spawning client's terminal (review of #2114).
#  16. the launcher skip ignores LauncherPID → a hosted codex root, whose
#      LauncherPID names its TUI, loses its launcher (codex's lock).
#  17. withoutExcludedArgv keeps a PID whose argv it cannot read → a process
#      with an unreadable argv counts as the 2.x service.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on opencode-v2-reader-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: opencode-v2-service-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: opencode-v2-service-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "opencode-v2-service-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/opencode-v2-service-mutations_test.sh" >&2
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
SERVICES="./core/application/services/"
AGENT="core/adapters/inbound/agents/opencode/agent.go"
PID="core/adapters/inbound/agents/opencode/pid.go"
WATCHER_V2="core/adapters/inbound/agents/opencode/watcher_v2.go"
DISCOVERY="core/adapters/inbound/agents/processlifecycle/discovery.go"
PM_FILE="core/application/services/pid_manager.go"
ACTIVITY="core/application/services/session_detector_activity.go"
OWNS='return IsServiceArgv(processlifecycle.ReadArgv(pid)) && v2SessionLive(transcriptPath)'
HOST_RULE='return pm.releasedPIDs[snap.adapter] == nil && pm.onSessionHost(snap.adapter, snap.pid)'
HOST_ARGV='return pm.observeAllowed(adapter) && isHost(pm.hostReadArgv(pid))'
BIND='^TestOpenCodeDiscoverPID_BindsAClientlessSessionToTheService$'
TTL='^TestCheckPIDLiveness_IdleClientlessOpencodeRootEndsAtTheReadyTTL$'

# ── 1. no SharedPIDOwner ──
assert_go_test_goes_red \
  "opencode declaring no SharedPIDOwner" \
  "$AGENT" \
  $'\t\t\tSharedPIDOwner: OwnsSharedPID,\n' \
  '' \
  "$SERVICES" \
  '^TestSamePIDPaths_ClientlessOpencodeRootsShareTheServicePID$' \
  "removed a client-less opencode root sharing the service's pid"

# ── 2. the probe without the argv check ──
assert_go_test_goes_red \
  "OwnsSharedPID without the service argv check" \
  "$PID" \
  "$OWNS" \
  'return v2SessionLive(transcriptPath)' \
  "$SERVICES" \
  '^TestHandlePIDAssigned_OpencodeRootsOnOneClientStayExclusive$' \
  "the older opencode root survived a newer root binding its TUI's pid"

# ── 3. the probe without the session_v2 row ──
assert_go_test_goes_red \
  "OwnsSharedPID without the session_v2 row" \
  "$PID" \
  "$OWNS" \
  'return IsServiceArgv(processlifecycle.ReadArgv(pid))' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ArchivedOpencodeRootOnTheServiceIsRetired$' \
  "an opencode root whose session was archived survived the same-PID sweep"

# ── 4. no service fallback ──
assert_go_test_goes_red \
  "DiscoverPID without the service fallback" \
  "$PID" \
  'return servicePIDFor(transcriptPath)' \
  'return 0, nil' \
  "$PROCLIFE" \
  "$BIND" \
  "client-less 2.x session binds to the service"

# ── 5. the fallback picks one of several services ──
assert_go_test_goes_red \
  "servicePIDFor binding the first of several services" \
  "$PID" \
  'if err != nil || len(pids) != 1 {' \
  'if err != nil || len(pids) == 0 {' \
  "$PROCLIFE" \
  '^TestOpenCodeDiscoverPID_BindsNoServiceWhenItCannotTellWhich$' \
  "DiscoverPID = (43001, <nil>), want (0, nil)"

# ── 6. the fallback without the session_v2 row ──
assert_go_test_goes_red \
  "servicePIDFor without the session_v2 row" \
  "$PID" \
  'if !v2SessionLive(transcriptPath) {' \
  'if false && !v2SessionLive(transcriptPath) {' \
  "$PROCLIFE" \
  "$BIND" \
  "a 1.x store's session is not bound to a 2.x service"

# ── 7. the PID lister ignores its predicate ──
assert_go_test_goes_red \
  "PIDsByCmdlineKeepingArgv keeping every match" \
  "$DISCOVERY" \
  'return withoutExcludedArgv(pids, func(argv []string) bool { return !keepArgv(argv) }), nil' \
  'return pids, nil' \
  "$PROCLIFE" \
  "$BIND" \
  "client-less 2.x session binds to the service"

# ── 8. admission ignores the service ──
assert_go_test_goes_red \
  "the 2.x watcher admitting only under a client" \
  "$WATCHER_V2" \
  'serviceUp := sync.OnceValue(w.serviceUp)' \
  'serviceUp := func() bool { return false }' \
  "$OPENCODE" \
  '^TestV2Reader_AdmitsAClientlessSessionOnLiveActivity$' \
  "live activity in a client-less session produced 0 EventNewSession, want 1"

# ── 9. a live host still protects every root ──
assert_go_test_goes_red \
  "the ready TTL without the host rule" \
  "$PM_FILE" \
  'if snap.pid > 0 && syscall.Kill(snap.pid, 0) == nil && !pm.idlesOnHost(snap) {' \
  'if snap.pid > 0 && syscall.Kill(snap.pid, 0) == nil {' \
  "$SERVICES" \
  "$TTL" \
  "ready, idle past the TTL: idle opencode root on a live pid removed = false, want true"

# ── 10. the host rule reaches an adapter with a release probe ──
assert_go_test_goes_red \
  "the host rule ignoring ReleasedPID" \
  "$PM_FILE" \
  "$HOST_RULE" \
  'return pm.onSessionHost(snap.adapter, snap.pid)' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_IdleCodexRootOnAppServerIsNotReapedAsInfra$' \
  "an idle codex root hosted by the app-server daemon was reaped"

# ── 11. the host rule ignores the bound PID's argv ──
assert_go_test_goes_red \
  "the host rule ignoring the bound PID's argv" \
  "$PM_FILE" \
  "$HOST_ARGV" \
  'return pm.observeAllowed(adapter)' \
  "$SERVICES" \
  "$TTL" \
  "bound to a TUI client: idle opencode root on a live pid removed = true, want false"

# ── 12. the host rule ignores observe consent ──
assert_go_test_goes_red \
  "the host rule ignoring observe consent" \
  "$PM_FILE" \
  "$HOST_ARGV" \
  'return isHost(pm.hostReadArgv(pid))' \
  "$SERVICES" \
  "$TTL" \
  "observe consent withheld: idle opencode root on a live pid removed = true, want false"

# ── 13. the pairing without LauncherPID ──
assert_go_test_goes_red \
  "the hosted-placeholder pairing without the LauncherPID gate" \
  "$PM_FILE" \
  'if s.CWD == "" || isHost == nil || pm.launcherPIDs[s.Adapter] == nil {' \
  'if s.CWD == "" || isHost == nil {' \
  "$SERVICES" \
  '^TestCheckPIDLiveness_ServiceBoundOpencodeRootLeavesTheTUIPlaceholder$' \
  "was retired by a service-bound opencode root in its cwd"

# ── 14. a DB-backed key reaches the writer probe ──
assert_go_test_goes_red \
  "admitStaleTranscript asking the writer probe about a DB-backed key" \
  "$ACTIVITY" \
  'if !isStaleTranscript(ev.TranscriptPath) {' \
  'if false && !isStaleTranscript(ev.TranscriptPath) {' \
  "$SERVICES" \
  '^TestAdmitStaleTranscript_DBBackedKeyNeverAsksTheWriterProbe$' \
  "admitting a DB-backed key asked opencode's PID discovery 1 time(s), want 0"

# ── 15. launcher capture reads the service ──
assert_go_test_goes_red \
  "captureLauncher without the host-without-client skip" \
  "$PM_FILE" \
  $'\tif pm.hostedWithoutClient(state.Adapter, pid) {\n\t\treturn\n\t}\n' \
  '' \
  "$SERVICES" \
  '^TestLauncherCapture_ServiceBoundOpencodeRootTakesNoLauncher$' \
  "service, assignment: launcher = &{TermProgram:SpawningClientsTerminal"

# ── 16. the launcher skip reaches an adapter with a LauncherPID ──
assert_go_test_goes_red \
  "hostedWithoutClient ignoring LauncherPID" \
  "$PM_FILE" \
  'return pm.launcherPIDs[adapter] == nil && pm.onSessionHost(adapter, pid)' \
  'return pm.onSessionHost(adapter, pid)' \
  "$SERVICES" \
  '^TestHandlePIDAssigned_HostedCodexRootTakesItsTUIsLauncher$' \
  "want the TUI's {TermProgram:ghostty"

# ── 17. an unreadable argv survives the filter ──
assert_go_test_goes_red \
  "withoutExcludedArgv keeping a PID whose argv is unreadable" \
  "$DISCOVERY" \
  $'if excludeArgv(argv) {\n\t\t\tcontinue\n\t\t}\n\t\tkept = append(kept, pid)' \
  $'if argv != nil && excludeArgv(argv) {\n\t\t\tcontinue\n\t\t}\n\t\tkept = append(kept, pid)' \
  "$PROCLIFE" \
  '^TestOpenCodeServicePIDs_AnUnreadableArgvIsNoService$' \
  "ServicePIDs() = ([44001 44002], <nil>), want ([44001], nil)"

if [[ $fails -gt 0 ]]; then
  echo "opencode-v2-service-mutations: $fails FAILED"
  exit 1
fi
echo "opencode-v2-service-mutations: ALL PASS"
