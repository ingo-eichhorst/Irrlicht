package services_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// writeTranscript creates a transcript file at path with the given mtime.
func writeTranscript(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// newPIDManagerForTest builds a PIDManager wired to the shared mockRepo and
// mockLogger from testhelpers_test.go. readyTTL is set large so the normal
// idle sweep doesn't interfere with the fast-delete path under test.
func newPIDManagerForTest(repo *mockRepo) *services.PIDManager {
	return services.NewPIDManager(
		nil, // no ProcessWatcher
		repo,
		&mockLogger{},
		nil, // no broadcaster
		10*time.Minute,
		nil, // no pid discovers
		nil, // no process names
		nil, // no live-CWDs lookup
		func(string) {},
	)
}

// newPIDManagerForTestWithLiveCWDs builds a PIDManager whose startup zombie
// sweep can use the DB-backed-orphan branch — the sweep needs both an
// adapter→process-name map and a live-CWDs lookup.
func newPIDManagerForTestWithLiveCWDs(repo *mockRepo, processNames map[string]string, liveCWDs services.LiveCWDsFunc) *services.PIDManager {
	return services.NewPIDManager(
		nil,
		repo,
		&mockLogger{},
		nil,
		10*time.Minute,
		nil,
		processNames,
		liveCWDs,
		func(string) {},
	)
}

// TestAllowsSession covers the #784 host-ancestry admission gate: an adapter
// that opts into RequireKnownHost rejects a candidate PID whose ancestry
// isn't a known terminal/IDE, but only once a PID actually resolves — a
// same-tick "no PID yet" must fail open, and adapters that never opt in must
// be entirely unaffected.
func TestAllowsSession(t *testing.T) {
	tests := []struct {
		name             string
		requireKnownHost bool
		discoverPID      int
		discoverErr      error
		isKnownHost      bool
		want             bool
	}{
		{"adapter doesn't opt in: always allowed regardless of host", false, 12345, nil, false, true},
		{"opts in, no PID discoverable yet: fails open", true, 0, errors.New("not found"), false, true},
		{"opts in, PID found, known host: allowed", true, 12345, nil, true, true},
		{"opts in, PID found, unknown host (e.g. CodexBar): rejected", true, 12345, nil, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockRepo()
			discoverCalls := 0
			discover := agent.PIDDiscoverFunc(func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
				discoverCalls++
				return tc.discoverPID, tc.discoverErr
			})
			pm := services.NewPIDManager(
				nil, repo, &mockLogger{}, nil, 10*time.Minute,
				map[string]agent.PIDDiscoverFunc{"antigravity": discover},
				nil, nil, func(string) {},
			)
			requireKnownHost := map[string]bool{}
			if tc.requireKnownHost {
				requireKnownHost["antigravity"] = true
			}
			pm.SetHostGate(requireKnownHost, func(pid int) bool { return tc.isKnownHost })

			if got := pm.AllowsSession("antigravity", "/some/cwd", "/some/transcript.jsonl"); got != tc.want {
				t.Errorf("AllowsSession() = %v, want %v", got, tc.want)
			}
			if tc.requireKnownHost && discoverCalls != 1 {
				t.Errorf("expected discover to be called once, got %d", discoverCalls)
			}
		})
	}
}

// TestCheckPIDLiveness_FreshTranscript_NotDeleted verifies the Layer 2 fix for
// issue #109: a ready session with PID=0 and a freshly-written transcript must
// NOT be fast-deleted after 30s. PID discovery may still be catching up (e.g.
// Claude hasn't written ~/.claude/sessions/<pid>.json yet).
func TestCheckPIDLiveness_FreshTranscript_NotDeleted(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "fresh.jsonl")
	writeTranscript(t, transcript, time.Now())

	repo := newMockRepo()
	// Updated 60s ago → past the 30s threshold, but transcript is fresh.
	repo.states["fresh"] = &session.SessionState{
		SessionID:      "fresh",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["fresh"] == nil {
		t.Fatal("session was deleted but transcript is fresh — fast-delete guard failed")
	}
}

// TestCheckPIDLiveness_StaleTranscript_Deleted verifies the existing behavior
// still works: a ready session with PID=0 AND a stale transcript (>2m) is
// still fast-deleted after 30s.
func TestCheckPIDLiveness_StaleTranscript_Deleted(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "stale.jsonl")
	writeTranscript(t, transcript, time.Now().Add(-10*time.Minute))

	repo := newMockRepo()
	repo.states["stale"] = &session.SessionState{
		SessionID:      "stale",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["stale"] != nil {
		t.Fatal("session should be deleted (stale transcript + ready + pid=0 + >30s)")
	}
}

// TestCheckPIDLiveness_DeadProcessWorking_Reaped guards the end-state the #667
// fix relies on: once UpdatedAt is allowed to age (the activity bump no longer
// refreshes it on no-op refresh passes), a working session with pid=0 whose
// UpdatedAt is past readyTTL is reaped by the existing sweep — even with a fresh
// transcript on disk, since the daemon can't probe liveness of a pid=0 session.
func TestCheckPIDLiveness_DeadProcessWorking_Reaped(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "gem.jsonl")
	writeTranscript(t, transcript, time.Now()) // fresh transcript; only UpdatedAt is stale

	repo := newMockRepo()
	repo.states["gem"] = &session.SessionState{
		SessionID:      "gem",
		Adapter:        "gemini-cli",
		State:          session.StateWorking,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-11 * time.Minute).Unix(), // > 10m readyTTL
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["gem"] != nil {
		t.Fatal("dead-process working session (pid=0, UpdatedAt past readyTTL) should be reaped")
	}
}

// deadPIDForTest spawns and reaps a short-lived process, returning its PID.
// Skips the test if the kernel races us and recycles the PID before we can
// confirm it is dead — keeps the test deterministic.
func deadPIDForTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	if err := syscall.Kill(pid, 0); err == nil {
		t.Skipf("dead PID %d was recycled before test could observe it", pid)
	}
	return pid
}

// liveProcessForTest spawns a long-lived child and returns its PID; the process
// is killed at cleanup. Gives the sweep a PID for which syscall.Kill(pid, 0)
// succeeds — the infra-ghost signature (alive, but not the real session).
func liveProcessForTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// newPIDManagerForTestWithInfraReaper wires Fix B's infra-reaper seam (#727):
// the sweep reaps a session bound to a still-alive PID whose argv the adapter's
// ExcludeArgv rejects.
func newPIDManagerForTestWithInfraReaper(repo *mockRepo, readArgv func(int) []string) *services.PIDManager {
	pm := newPIDManagerForTest(repo)
	pm.SetInfraReaper(
		map[string]func([]string) bool{"claude-code": claudecode.IsInfraArgv},
		readArgv,
	)
	return pm
}

// infraGhostState builds a working claude-code session bound to a live PID with
// a stale transcript and stale UpdatedAt — the persisted ghost shape from #727
// (a session mis-bound to a --bg-spare pool helper that outlives it).
func infraGhostState(t *testing.T, pid int, transcriptMtime time.Time) *session.SessionState {
	t.Helper()
	transcript := filepath.Join(t.TempDir(), "ghost.jsonl")
	writeTranscript(t, transcript, transcriptMtime)
	return &session.SessionState{
		SessionID:      "ghost",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            pid,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-11 * time.Minute).Unix(), // > 10m readyTTL
	}
}

// TestCheckPIDLiveness_GhostBoundToInfra_Reaped is the core #727 fix: a working
// session bound to a still-alive PID that is Claude Code's --bg-spare pool helper
// (per the real argv shape: path argv[0] + --bg-spare) is reaped by the sweep
// even though syscall.Kill on its PID succeeds.
func TestCheckPIDLiveness_GhostBoundToInfra_Reaped(t *testing.T) {
	pid := liveProcessForTest(t)
	repo := newMockRepo()
	repo.states["ghost"] = infraGhostState(t, pid, time.Now().Add(-10*time.Minute))

	readArgv := func(int) []string {
		return []string{"/Users/x/.local/share/claude/versions/2.1.185", "--bg-spare", "/tmp/x.claim.sock"}
	}
	newPIDManagerForTestWithInfraReaper(repo, readArgv).CheckPIDLiveness()

	if repo.states["ghost"] != nil {
		t.Fatal("session bound to a live --bg-spare infra PID should be reaped (#727)")
	}
}

// TestCheckPIDLiveness_LiveInfraFreshTranscript_NotReaped: even when the bound
// PID's argv is infra, a fresh transcript means the session is plausibly active
// — the staleness guard must keep it.
func TestCheckPIDLiveness_LiveInfraFreshTranscript_NotReaped(t *testing.T) {
	pid := liveProcessForTest(t)
	repo := newMockRepo()
	st := infraGhostState(t, pid, time.Now()) // fresh transcript
	st.UpdatedAt = time.Now().Unix()          // and fresh UpdatedAt
	repo.states["ghost"] = st

	readArgv := func(int) []string { return []string{"claude", "--bg-spare", "/tmp/x.sock"} }
	newPIDManagerForTestWithInfraReaper(repo, readArgv).CheckPIDLiveness()

	if repo.states["ghost"] == nil {
		t.Fatal("active session (fresh transcript/UpdatedAt) must not be reaped even if its PID looks infra")
	}
}

// TestCheckPIDLiveness_LiveSessionRealArgv_NotReaped: a stale session bound to a
// live PID whose argv is a real interactive `claude` (not infra) must be kept —
// the bound process IS the session (e.g. a long idle agent / permission prompt).
func TestCheckPIDLiveness_LiveSessionRealArgv_NotReaped(t *testing.T) {
	pid := liveProcessForTest(t)
	repo := newMockRepo()
	repo.states["ghost"] = infraGhostState(t, pid, time.Now().Add(-10*time.Minute))

	readArgv := func(int) []string { return []string{"claude", "-p", "do the thing"} }
	newPIDManagerForTestWithInfraReaper(repo, readArgv).CheckPIDLiveness()

	if repo.states["ghost"] == nil {
		t.Fatal("session bound to a real (non-infra) live claude PID must not be reaped")
	}
}

// TestCheckPIDLiveness_NilArgv_NotReaped: an unreadable argv must never trip the
// reaper — we never reap on absence of evidence (the ExcludeArgv contract).
func TestCheckPIDLiveness_NilArgv_NotReaped(t *testing.T) {
	pid := liveProcessForTest(t)
	repo := newMockRepo()
	repo.states["ghost"] = infraGhostState(t, pid, time.Now().Add(-10*time.Minute))

	readArgv := func(int) []string { return nil }
	newPIDManagerForTestWithInfraReaper(repo, readArgv).CheckPIDLiveness()

	if repo.states["ghost"] == nil {
		t.Fatal("session with unreadable argv must not be reaped (no reap on absence of evidence)")
	}
}

// TestCheckPIDLiveness_NoReaperWired_NoChange: with no infra reaper installed
// (demo mode / pre-Fix-B), a stale working session bound to a live PID is left
// untouched — the sweep's pre-existing behavior.
func TestCheckPIDLiveness_NoReaperWired_NoChange(t *testing.T) {
	pid := liveProcessForTest(t)
	repo := newMockRepo()
	repo.states["ghost"] = infraGhostState(t, pid, time.Now().Add(-10*time.Minute))

	newPIDManagerForTest(repo).CheckPIDLiveness() // no SetInfraReaper

	if repo.states["ghost"] == nil {
		t.Fatal("without an infra reaper the live-PID working session must be kept (pre-Fix-B behavior)")
	}
}

// TestSeedAlivePIDs_DeletesWhenCWDMissing is the seed-time half of issue
// #321. On daemon startup, a previously-tracked session whose worktree has
// been deleted must be cleaned up even when its transcript was touched
// within the staleness window (e.g. by `claude --resume` from elsewhere).
// A missing cwd directory is the unambiguous orphan signal.
func TestSeedAlivePIDs_DeletesWhenCWDMissing(t *testing.T) {
	tmp := t.TempDir()
	freshTranscript := filepath.Join(tmp, "zombie.jsonl")
	writeTranscript(t, freshTranscript, time.Now())

	// CWD that does not exist (worktree was deleted).
	missingCWD := filepath.Join(tmp, "deleted-worktree")

	repo := newMockRepo()
	repo.states["zombie"] = &session.SessionState{
		SessionID:      "zombie",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		CWD:            missingCWD,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}

	// Child session with the same missing cwd — should be cleaned up too,
	// via deleteWithChildren, when the parent is removed.
	repo.states["zombie-child"] = &session.SessionState{
		SessionID:       "zombie-child",
		ParentSessionID: "zombie",
		Adapter:         "claude-code",
		State:           session.StateReady,
		PID:             0,
		CWD:             missingCWD,
		TranscriptPath:  freshTranscript,
		UpdatedAt:       time.Now().Unix(),
	}

	states := []*session.SessionState{repo.states["zombie"], repo.states["zombie-child"]}
	newPIDManagerForTest(repo).SeedPIDs(states)

	if repo.states["zombie"] != nil {
		t.Error("zombie session with missing cwd should have been deleted at seed time")
	}
	if repo.states["zombie-child"] != nil {
		t.Error("child of zombie session should have been deleted via deleteWithChildren")
	}
}

// TestCleanupZombies covers the two predicates plus the happy-path
// exemptions: dead PID is deleted; live PID is kept (regardless of how old
// the record is — see the comment on isStartupZombie about the deliberate
// absence of recycled-PID detection); PID=0 + stale transcript + no parent
// is deleted; PID=0 + fresh transcript is kept; PID=0 child is kept.
func TestCleanupZombies(t *testing.T) {
	tmp := t.TempDir()
	freshTranscript := filepath.Join(tmp, "fresh.jsonl")
	staleTranscript := filepath.Join(tmp, "stale.jsonl")
	writeTranscript(t, freshTranscript, time.Now())
	writeTranscript(t, staleTranscript, time.Now().Add(-10*time.Minute))

	deadPID := deadPIDForTest(t)
	livePID := os.Getpid() // the test process itself is reliably alive

	repo := newMockRepo()
	// 1. Known PID, dead → deleted.
	repo.states["dead-pid"] = &session.SessionState{
		SessionID:      "dead-pid",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            deadPID,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}
	// 2. Known PID, alive, recent → kept.
	repo.states["alive-fresh"] = &session.SessionState{
		SessionID:      "alive-fresh",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            livePID,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}
	// 3. Known PID, alive, idle for a long time + stale transcript → KEPT.
	// Documents the explicit non-goal: we never delete a session whose
	// process is alive, because reliably distinguishing a recycled PID from
	// a long-idle agent needs an adapter-specific process-name check that
	// this sweep doesn't do.
	repo.states["alive-idle"] = &session.SessionState{
		SessionID:      "alive-idle",
		Adapter:        "claude-code",
		State:          session.StateWaiting,
		PID:            livePID,
		TranscriptPath: staleTranscript,
		UpdatedAt:      time.Now().Add(-10 * time.Minute).Unix(),
	}
	// 4. PID=0, stale transcript, no parent → deleted (orphan).
	repo.states["orphan"] = &session.SessionState{
		SessionID:      "orphan",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            0,
		TranscriptPath: staleTranscript,
		UpdatedAt:      time.Now().Add(-10 * time.Minute).Unix(),
	}
	// 5. PID=0, fresh transcript → kept (PID discovery may still resolve).
	repo.states["pid0-fresh"] = &session.SessionState{
		SessionID:      "pid0-fresh",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            0,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}
	// 6. PID=0 child session → kept (subagent runs inside parent's process).
	repo.states["child"] = &session.SessionState{
		SessionID:       "child",
		ParentSessionID: "alive-fresh",
		Adapter:         "claude-code",
		State:           session.StateWorking,
		PID:             0,
		TranscriptPath:  staleTranscript,
		UpdatedAt:       time.Now().Add(-10 * time.Minute).Unix(),
	}

	deleted := newPIDManagerForTest(repo).CleanupZombies()
	if deleted != 2 {
		t.Errorf("CleanupZombies returned %d, want 2 (dead-pid, orphan)", deleted)
	}

	wantDeleted := []string{"dead-pid", "orphan"}
	for _, id := range wantDeleted {
		if repo.states[id] != nil {
			t.Errorf("session %q should have been deleted but is still present", id)
		}
	}
	wantKept := []string{"alive-fresh", "alive-idle", "pid0-fresh", "child"}
	for _, id := range wantKept {
		if repo.states[id] == nil {
			t.Errorf("session %q should have been kept but is gone", id)
		}
	}
}

// TestCleanupZombies_DBBackedOrphan covers the carryover-state path for
// DB-backed adapters (OpenCode): a PID=0 session whose TranscriptPath
// contains "?session=" is deleted iff the adapter's process name has no
// live process owning the session's CWD. This is the cleanup half of the
// v0.3.12 ghost-sessions fix — the watcher gates new emissions, but
// existing on-disk state from the buggy daemon needs this branch to clear
// without a manual wipe.
func TestCleanupZombies_DBBackedOrphan(t *testing.T) {
	wal := "/Users/test/.local/share/opencode/opencode.db-wal"
	repo := newMockRepo()

	// 1. DB-backed session whose CWD is NOT held by any live process → deleted.
	repo.states["opencode-orphan"] = &session.SessionState{
		SessionID:      "opencode-orphan",
		Adapter:        "opencode",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/orphan-project",
		TranscriptPath: wal + "?session=ses_orphan",
		UpdatedAt:      time.Now().Unix(),
	}
	// 2. DB-backed session whose CWD IS held by a live process → kept.
	repo.states["opencode-live"] = &session.SessionState{
		SessionID:      "opencode-live",
		Adapter:        "opencode",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/active-project",
		TranscriptPath: wal + "?session=ses_live",
		UpdatedAt:      time.Now().Unix(),
	}
	// 3. DB-backed session whose adapter has no registered process name →
	//    kept (lookup is inconclusive; safer not to delete).
	repo.states["unknown-adapter"] = &session.SessionState{
		SessionID:      "unknown-adapter",
		Adapter:        "future-db-adapter",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/somewhere",
		TranscriptPath: wal + "?session=ses_unknown",
		UpdatedAt:      time.Now().Unix(),
	}
	// 4. DB-backed session whose lookup fails (lookup returns error) →
	//    kept (we don't delete on uncertain liveness signals).
	repo.states["opencode-lookup-error"] = &session.SessionState{
		SessionID:      "opencode-lookup-error",
		Adapter:        "opencode-flaky",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/whatever",
		TranscriptPath: wal + "?session=ses_err",
		UpdatedAt:      time.Now().Unix(),
	}

	processNames := map[string]string{
		"opencode":       "opencode",
		"opencode-flaky": "opencode-flaky", // mapped, but lookup will fail
	}
	calls := make(map[string]int)
	liveCWDs := func(name string) (map[string]struct{}, error) {
		calls[name]++
		switch name {
		case "opencode":
			return map[string]struct{}{
				"/home/user/active-project": {},
			}, nil
		case "opencode-flaky":
			return nil, errors.New("pgrep failed")
		}
		return nil, nil
	}

	deleted := newPIDManagerForTestWithLiveCWDs(repo, processNames, liveCWDs).CleanupZombies()
	if deleted != 1 {
		t.Errorf("CleanupZombies returned %d, want 1 (only opencode-orphan)", deleted)
	}
	if repo.states["opencode-orphan"] != nil {
		t.Error("opencode-orphan should have been deleted (no live process for its CWD)")
	}
	for _, id := range []string{"opencode-live", "unknown-adapter", "opencode-lookup-error"} {
		if repo.states[id] == nil {
			t.Errorf("session %q should have been kept", id)
		}
	}

	// Cache assertion: each registered adapter is looked up at most once,
	// even though "opencode" has two ghost candidates and "opencode-flaky"
	// errors out. Without the per-sweep cache this would be 3 calls.
	if calls["opencode"] != 1 {
		t.Errorf("liveCWDs(opencode) call count = %d, want 1 (cached)", calls["opencode"])
	}
	if calls["opencode-flaky"] != 1 {
		t.Errorf("liveCWDs(opencode-flaky) call count = %d, want 1 (cached error)", calls["opencode-flaky"])
	}
}

// TestHandlePIDAssigned_LauncherCaptureIsIdempotent verifies the reader runs
// exactly once when a session first gets a launcher, and is never invoked
// again even if a different PID later arrives.
func TestHandlePIDAssigned_LauncherCaptureIsIdempotent(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		State:     session.StateWorking,
		UpdatedAt: time.Now().Unix(),
	}

	pm := newPIDManagerForTest(repo)
	var calls int
	pm.SetLauncherEnvReader(func(pid int) *session.Launcher {
		calls++
		return &session.Launcher{TermProgram: "iTerm.app"}
	})

	pm.HandlePIDAssigned(42, "s")
	if calls != 1 {
		t.Fatalf("first assign: reader calls = %d, want 1", calls)
	}
	if repo.states["s"].Launcher == nil {
		t.Fatal("first assign: launcher not captured")
	}

	// Same PID, same session: HandlePIDAssigned early-returns at the
	// `state.PID == pid` guard before reaching captureLauncher.
	pm.HandlePIDAssigned(42, "s")
	if calls != 1 {
		t.Errorf("repeat same PID: reader re-invoked (%d calls)", calls)
	}

	// Different PID on a session that already has a launcher: state.PID
	// changes, but captureLauncher's `state.Launcher != nil` guard prevents
	// a re-read — preserving the original launcher identity even across
	// process restarts / /clear scenarios.
	pm.HandlePIDAssigned(99, "s")
	if calls != 1 {
		t.Errorf("new PID with existing launcher: reader re-invoked (%d calls)", calls)
	}
	if repo.states["s"].Launcher == nil || repo.states["s"].Launcher.TermProgram != "iTerm.app" {
		t.Errorf("launcher clobbered by later PID assignment: %+v", repo.states["s"].Launcher)
	}
}

// TestBackfillLauncher_KittyFieldsMergedFromFreshEnv exercises the
// SeedPIDs → handleAlivePIDState → backfillLauncher path for issue #326:
// pre-existing kitty sessions that shipped with KittyPID == 0 (because
// the daemon binary at session-birth didn't whitelist KITTY_PID, or
// because the agent's env was unreadable via sysctl) must pick up
// KittyPID / KittyListenOn / KittyWindowID on the next daemon startup
// from a fresh env read. Existing fields must not be clobbered.
func TestBackfillLauncher_KittyFieldsMergedFromFreshEnv(t *testing.T) {
	repo := newMockRepo()
	// Session has a partial kitty launcher: term_program is set but none of
	// the kitty signals — exactly the shape of a pi session pre-fix or a
	// session captured by a v0.4.0 daemon.
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		State:     session.StateWorking,
		PID:       os.Getpid(), // alive — handleAlivePIDState's syscall.Kill probe must succeed
		UpdatedAt: 0,
		Launcher: &session.Launcher{
			TermProgram: "kitty",
			TTY:         "/dev/ttys001", // already populated; must survive merge
		},
	}

	pm := newPIDManagerForTest(repo)
	pm.SetLauncherEnvReader(func(pid int) *session.Launcher {
		return &session.Launcher{
			TermProgram:   "kitty",
			TTY:           "/dev/ttys999", // intentionally different — should NOT overwrite
			KittyPID:      31155,
			KittyListenOn: "unix:/tmp/kitty-31155",
			KittyWindowID: "2",
		}
	})

	pm.SeedPIDs([]*session.SessionState{repo.states["s"]})

	got := repo.states["s"].Launcher
	if got == nil {
		t.Fatal("launcher went nil after backfill")
	}
	// New fields filled in from fresh env.
	if got.KittyPID != 31155 {
		t.Errorf("KittyPID: got %d, want 31155", got.KittyPID)
	}
	if got.KittyListenOn != "unix:/tmp/kitty-31155" {
		t.Errorf("KittyListenOn: got %q, want unix:/tmp/kitty-31155", got.KittyListenOn)
	}
	if got.KittyWindowID != "2" {
		t.Errorf("KittyWindowID: got %q, want 2", got.KittyWindowID)
	}
	// Pre-existing fields untouched.
	if got.TermProgram != "kitty" {
		t.Errorf("TermProgram clobbered: %q", got.TermProgram)
	}
	if got.TTY != "/dev/ttys001" {
		t.Errorf("TTY clobbered by fresh value: got %q, want /dev/ttys001", got.TTY)
	}
	// Modified state must be persisted via Save (UpdatedAt updated from 0).
	if repo.states["s"].UpdatedAt == 0 {
		t.Errorf("UpdatedAt not refreshed after merge")
	}
}

// TestBackfillLauncher_NoChangeWhenNothingMissing verifies the early-return
// path: a session with a complete kitty launcher must not trigger a fresh
// env read on each SeedPIDs invocation (which would be a wasted syscall
// and, on macOS, a wasted kitten shell-out per session).
func TestBackfillLauncher_NoChangeWhenNothingMissing(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		State:     session.StateWorking,
		PID:       os.Getpid(),
		UpdatedAt: 42,
		Launcher: &session.Launcher{
			TermProgram:   "kitty",
			TTY:           "/dev/ttys001",
			KittyPID:      31155,
			KittyListenOn: "unix:/tmp/kitty-31155",
			KittyWindowID: "1",
		},
	}

	pm := newPIDManagerForTest(repo)
	var calls int
	pm.SetLauncherEnvReader(func(pid int) *session.Launcher {
		calls++
		return &session.Launcher{TermProgram: "iTerm.app"} // would corrupt if called
	})

	pm.SeedPIDs([]*session.SessionState{repo.states["s"]})

	if calls != 0 {
		t.Errorf("env reader invoked when launcher was complete: %d calls", calls)
	}
	if repo.states["s"].Launcher.TermProgram != "kitty" {
		t.Errorf("launcher mutated: %+v", repo.states["s"].Launcher)
	}
	if repo.states["s"].UpdatedAt != 42 {
		t.Errorf("UpdatedAt mutated despite no change: got %d, want 42", repo.states["s"].UpdatedAt)
	}
}

// TestBackfillLauncher_NonKittyUnaffected ensures the kitty-specific merge
// doesn't fire for sessions on other terminals — there's no reason for an
// iTerm session to trigger a kitten shell-out.
func TestBackfillLauncher_NonKittyUnaffected(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		State:     session.StateWorking,
		PID:       os.Getpid(),
		UpdatedAt: 42,
		Launcher: &session.Launcher{
			TermProgram: "iTerm.app",
			TTY:         "/dev/ttys001",
		},
	}

	pm := newPIDManagerForTest(repo)
	var calls int
	pm.SetLauncherEnvReader(func(pid int) *session.Launcher {
		calls++
		// Even if a fresh read suggested kitty fields, the missing-checks
		// gate `isKitty := TermProgram == "kitty"` — none of the
		// missingKitty* flags will be true for an iTerm launcher.
		return &session.Launcher{TermProgram: "iTerm.app", KittyPID: 9999}
	})

	pm.SeedPIDs([]*session.SessionState{repo.states["s"]})

	if calls != 0 {
		t.Errorf("env reader fired for non-kitty session: %d calls", calls)
	}
	if repo.states["s"].Launcher.KittyPID != 0 {
		t.Errorf("KittyPID leaked into non-kitty launcher: %d", repo.states["s"].Launcher.KittyPID)
	}
}

// TestTryDiscoverPID_ProcSession_BypassesDiscoverFn is the regression guard for
// issue #345. Pre-sessions (proc-<pid>) encode their PID in the session ID, so
// TryDiscoverPID must not invoke the adapter's CWD-based discoverFn for them —
// doing so misattributes the PID to a sibling agent process in the same CWD
// and triggers the same-PID cleanup that evicts the legitimate neighbor.
func TestTryDiscoverPID_ProcSession_BypassesDiscoverFn(t *testing.T) {
	repo := newMockRepo()

	// Neighbor session that shares the encoded PID space: it has a *different*
	// PID, so the same-PID cleanup must NOT touch it after we assign 12345.
	repo.states["neighbor-uuid"] = &session.SessionState{
		SessionID:      "neighbor-uuid",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            99999,
		TranscriptPath: filepath.Join(t.TempDir(), "neighbor.jsonl"),
		UpdatedAt:      time.Now().Unix(),
	}

	// Pre-session as the scanner emits it: PID=0, ID encodes the PID.
	repo.states["proc-12345"] = &session.SessionState{
		SessionID: "proc-12345",
		Adapter:   "claude-code",
		State:     session.StateReady,
		PID:       0,
		CWD:       "/tmp/shared-cwd",
		UpdatedAt: time.Now().Unix(),
	}

	// Stub discoverFn that fails the test if it is ever called.
	discoverCalls := 0
	discovers := map[string]agent.PIDDiscoverFunc{
		"claude-code": func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
			discoverCalls++
			// Return a wrong PID to make sure the test fails loudly if the
			// short-circuit regresses — this is exactly the bug scenario:
			// CWD-based discovery returning the neighbor's PID.
			return 99999, nil
		},
	}

	pm := services.NewPIDManager(
		newMockProcessWatcher(),
		repo,
		&mockLogger{},
		nil,
		10*time.Minute,
		discovers,
		nil,
		nil,
		func(string) {},
	)

	if !pm.TryDiscoverPID("proc-12345", "/tmp/shared-cwd", "", "claude-code") {
		t.Fatal("TryDiscoverPID returned false for proc-12345")
	}

	if discoverCalls != 0 {
		t.Errorf("adapter discoverFn was called %d times for a proc-<pid> session; want 0", discoverCalls)
	}

	if got := repo.states["proc-12345"]; got == nil {
		t.Fatal("proc-12345 was deleted")
	} else if got.PID != 12345 {
		t.Errorf("proc-12345 PID = %d, want 12345", got.PID)
	}

	if repo.states["neighbor-uuid"] == nil {
		t.Fatal("neighbor-uuid was evicted by same-PID cleanup — issue #345 regression")
	}
}

// newPIDManagerForTestWithDeleteSpy builds a PIDManager whose onSessionDeleted
// callback records every evicted session ID — the seam main.go wires to the
// detector's history/projectSessions eviction helper. Locks the #593 deletion
// funnel: every PIDManager removal must fire it, or history rings leak.
func newPIDManagerForTestWithDeleteSpy(repo *mockRepo, deleted *[]string) *services.PIDManager {
	return services.NewPIDManager(
		nil,
		repo,
		&mockLogger{},
		nil,
		10*time.Minute,
		nil,
		nil,
		nil,
		func(sid string) { *deleted = append(*deleted, sid) },
	)
}

// TestCheckPIDLiveness_ChildSweep_EvictsHistory: before #593 the liveness
// sweep deleted finished children straight from the repo, bypassing
// onSessionDeleted — their history rings leaked into history.json forever.
func TestCheckPIDLiveness_ChildSweep_EvictsHistory(t *testing.T) {
	tmp := t.TempDir()
	parentTranscript := filepath.Join(tmp, "parent.jsonl")
	writeTranscript(t, parentTranscript, time.Now())
	childTranscript := filepath.Join(tmp, "agent-abc.jsonl")
	writeTranscript(t, childTranscript, time.Now())

	repo := newMockRepo()
	repo.states["parent"] = &session.SessionState{
		SessionID:      "parent",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		TranscriptPath: parentTranscript,
		UpdatedAt:      time.Now().Unix(),
	}
	repo.states["agent-abc"] = &session.SessionState{
		SessionID:       "agent-abc",
		Adapter:         "claude-code",
		State:           session.StateReady,
		ParentSessionID: "parent",
		TranscriptPath:  childTranscript,
		UpdatedAt:       time.Now().Unix(),
	}

	var deleted []string
	newPIDManagerForTestWithDeleteSpy(repo, &deleted).CheckPIDLiveness()

	if repo.states["agent-abc"] != nil {
		t.Fatal("ready child should be swept")
	}
	if !slices.Contains(deleted, "agent-abc") {
		t.Errorf("onSessionDeleted not fired for swept child; got %v", deleted)
	}
	if repo.states["parent"] == nil {
		t.Fatal("parent must survive the child sweep")
	}
}

// TestHandleProcessExit_EvictsChildrenViaFunnel: deleteWithChildren must route
// both the children and the session itself through the deletion funnel so
// caller-side tracking (history rings) is evicted for every removal (#593).
func TestHandleProcessExit_EvictsChildrenViaFunnel(t *testing.T) {
	repo := newMockRepo()
	repo.states["parent"] = &session.SessionState{
		SessionID: "parent",
		Adapter:   "claude-code",
		State:     session.StateReady,
		PID:       4242,
	}
	repo.states["agent-kid"] = &session.SessionState{
		SessionID:       "agent-kid",
		Adapter:         "claude-code",
		State:           session.StateWorking,
		ParentSessionID: "parent",
	}

	var deleted []string
	newPIDManagerForTestWithDeleteSpy(repo, &deleted).HandleProcessExit(4242, "parent", "test: pid exited")

	if len(repo.states) != 0 {
		t.Fatalf("parent and child should both be gone, repo has %d sessions", len(repo.states))
	}
	if !slices.Contains(deleted, "agent-kid") {
		t.Errorf("onSessionDeleted not fired for child; got %v", deleted)
	}
	if !slices.Contains(deleted, "parent") {
		t.Errorf("onSessionDeleted not fired for parent; got %v", deleted)
	}
}

// TestCheckPIDLiveness_GhostPreSession_PIDBoundToSibling reproduces issue #645:
// a real session is PID-bound to a sibling process (a cc-daemon --resume copy),
// so the scanner's PID-strict live check never marks the original TUI's proc-*
// pre-session superseded. The seed-time sweep ran once before the ghost was
// minted, so it's permanent. The periodic sweep must retire it — and it must
// stay gone on the next sweep (a flapping ghost is a failed fix).
func TestCheckPIDLiveness_GhostPreSession_PIDBoundToSibling(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "real.jsonl")
	writeTranscript(t, transcript, time.Now())

	ghostPID := os.Getpid()    // the original TUI — alive
	siblingPID := os.Getppid() // the --resume copy that won the binding — alive, distinct
	if siblingPID == ghostPID || siblingPID <= 0 {
		t.Skip("need a distinct alive parent PID for the sibling-binding scenario")
	}

	repo := newMockRepo()
	// Real session, PID-bound to the sibling (not the ghost's PID).
	repo.states["bb9f6ebf"] = &session.SessionState{
		SessionID:      "bb9f6ebf",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            siblingPID,
		CWD:            tmp,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Unix(),
	}
	// Ghost pre-session for the original TUI: same adapter+CWD, distinct alive
	// PID, minted well past the grace window.
	repo.states["proc-ghost"] = &session.SessionState{
		SessionID: "proc-ghost",
		Adapter:   "claude-code",
		State:     session.StateReady,
		PID:       ghostPID,
		CWD:       tmp,
		FirstSeen: time.Now().Add(-5 * time.Minute).Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	var deleted []string
	pm := newPIDManagerForTestWithDeleteSpy(repo, &deleted)
	pm.CheckPIDLiveness()

	if repo.states["proc-ghost"] != nil {
		t.Fatal("ghost pre-session should be swept (real session PID-bound to a sibling)")
	}
	if repo.states["bb9f6ebf"] == nil {
		t.Fatal("real session must survive the sweep")
	}
	if !slices.Contains(deleted, "proc-ghost") {
		t.Errorf("onSessionDeleted not fired for ghost; got %v", deleted)
	}

	// Stays gone: a second sweep must not see it (it's deleted from the repo)
	// and must not error. This guards against a delete→re-mint→delete flap.
	pm.CheckPIDLiveness()
	if repo.states["proc-ghost"] != nil {
		t.Fatal("ghost re-appeared after a second sweep — flapping")
	}
}

// TestCheckPIDLiveness_FreshPreSession_NotSwept is the issue #113 guard: a
// freshly-opened process in a dir that already has an active session must keep
// its pre-session (two claude instances in one cwd). The PID-strict scanner
// check protects this at poll time; the periodic CWD-fallback sweep must not
// undo it — within the grace window it leaves the young pre-session alone even
// though the adapter+CWD match (and a distinct alive PID) are present.
func TestCheckPIDLiveness_FreshPreSession_NotSwept(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "real.jsonl")
	writeTranscript(t, transcript, time.Now())

	existingPID := os.Getppid()
	freshPID := os.Getpid()
	if existingPID == freshPID || existingPID <= 0 {
		t.Skip("need a distinct alive parent PID for the two-instances scenario")
	}

	repo := newMockRepo()
	repo.states["existing"] = &session.SessionState{
		SessionID:      "existing",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            existingPID,
		CWD:            tmp,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Unix(),
	}
	// A second claude just opened in the same dir; its pre-session was minted
	// moments ago. Its own transcript + PID binding haven't landed yet.
	repo.states["proc-fresh"] = &session.SessionState{
		SessionID: "proc-fresh",
		Adapter:   "claude-code",
		State:     session.StateReady,
		PID:       freshPID,
		CWD:       tmp,
		FirstSeen: time.Now().Unix(), // within the grace window
		UpdatedAt: time.Now().Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["proc-fresh"] == nil {
		t.Fatal("fresh pre-session swept within grace window — #113 regression")
	}
	if repo.states["existing"] == nil {
		t.Fatal("existing session must survive")
	}
}

// TestCheckPIDLiveness_PIDMatchPreSession_SweptImmediately: when the proc-*
// pre-session and a real session share the SAME PID, that's ordinary
// supersession (PID discovery completed). It's always safe and needs no grace
// — sweep it on the first periodic pass even if it's brand new.
func TestCheckPIDLiveness_PIDMatchPreSession_SweptImmediately(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "real.jsonl")
	writeTranscript(t, transcript, time.Now())

	pid := os.Getpid()
	repo := newMockRepo()
	repo.states["real"] = &session.SessionState{
		SessionID:      "real",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            pid,
		CWD:            tmp,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Unix(),
	}
	repo.states["proc-same"] = &session.SessionState{
		SessionID: "proc-same",
		Adapter:   "claude-code",
		State:     session.StateReady,
		PID:       pid, // same PID as the real session
		CWD:       tmp,
		FirstSeen: time.Now().Unix(), // brand new — but PID match is grace-exempt
		UpdatedAt: time.Now().Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["proc-same"] != nil {
		t.Fatal("PID-matched pre-session should be swept immediately (no grace)")
	}
	if repo.states["real"] == nil {
		t.Fatal("real session must survive")
	}
}
