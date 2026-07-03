package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

func TestSessionDetector_PIDAssigned_CleansUpOldSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Old session: real transcript session with known PID (previous /clear victim).
	repo.states["old-session"] = &session.SessionState{
		SessionID:      "old-session",
		State:          session.StateReady,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/old-session.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	// New session: just created after /clear, PID not yet discovered.
	repo.states["new-session"] = &session.SessionState{
		SessionID:      "new-session",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/new-session.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)

	// Simulate PID discovery for the new session — same PID as old session.
	det.HandlePIDAssigned(42, "new-session")

	// Old session should be deleted (replaced by /clear).
	if state, _ := repo.Load("old-session"); state != nil {
		t.Errorf("old session should be deleted, but still exists with state %q", state.State)
	}

	// New session should have PID assigned.
	newState, _ := repo.Load("new-session")
	if newState == nil {
		t.Fatal("new session should exist")
	}
	if newState.PID != 42 {
		t.Errorf("new session PID: got %d, want 42", newState.PID)
	}

	// ProcessWatcher should track the PID for the new session.
	if pw.watched[42] != "new-session" {
		t.Errorf("ProcessWatcher: got %q for PID 42, want new-session", pw.watched[42])
	}
}

func TestSessionDetector_PIDAssigned_CapturesLauncher(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		State:     session.StateWorking,
		FirstSeen: now,
		UpdatedAt: now,
	}

	det := newDetector(tw, pw, repo)
	var calledPID int
	det.SetLauncherEnvReader(func(pid int) *session.Launcher {
		calledPID = pid
		return &session.Launcher{
			TermProgram:    "iTerm.app",
			ITermSessionID: "w0t0p0",
		}
	})

	det.HandlePIDAssigned(4242, "s")

	if calledPID != 4242 {
		t.Errorf("launcherEnvReader pid: got %d, want 4242", calledPID)
	}
	state, _ := repo.Load("s")
	if state == nil || state.Launcher == nil {
		t.Fatal("expected Launcher to be set after HandlePIDAssigned")
	}
	if state.Launcher.TermProgram != "iTerm.app" {
		t.Errorf("Launcher.TermProgram: got %q, want iTerm.app", state.Launcher.TermProgram)
	}

	// Subsequent PID assignment with the same PID must not clobber — the
	// guard `state.PID == pid` bails before we re-read env, so the reader
	// should not even be invoked again.
	calledPID = 0
	det.HandlePIDAssigned(4242, "s")
	if calledPID != 0 {
		t.Errorf("launcherEnvReader invoked again for same PID: got %d", calledPID)
	}
}

func TestSessionDetector_PIDAssigned_SkipsSubagents(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Parent session with known PID.
	repo.states["parent"] = &session.SessionState{
		SessionID:      "parent",
		State:          session.StateWorking,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	// Subagent session — shares parent's PID but has ParentSessionID set.
	repo.states["subagent"] = &session.SessionState{
		SessionID:       "subagent",
		State:           session.StateWorking,
		ParentSessionID: "parent",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent/subagents/subagent.jsonl",
		FirstSeen:       now,
		UpdatedAt:       now,
	}

	det := newDetector(tw, pw, repo)

	// Assign same PID to subagent — should NOT delete parent.
	det.HandlePIDAssigned(42, "subagent")

	if state, _ := repo.Load("parent"); state == nil {
		t.Error("parent session should NOT be deleted when subagent gets same PID")
	}
}

func TestSessionDetector_CWDFallback_DoesNotAssignDuplicatePID(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	cwd := t.TempDir() // see #321 — daemon rejects sessions with missing cwd

	// Mock CWD discovery: always returns the same two candidate PIDs.
	cwdFn := func(cwd string, disambiguate func([]int) int) (int, error) {
		return disambiguate([]int{1000, 1001}), nil
	}

	det := newDetectorWithCWDDiscovery(tw, pw, repo, cwdFn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Send two new sessions in the same project (same CWD).
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "sess-a",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/sess-a.jsonl",
		CWD:            cwd,
	}

	// Wait for the first session's PID to be assigned before sending the
	// second — sess-b's discovery must see sess-a's PID already claimed for
	// the disambiguation under test. Poll instead of a fixed sleep so it's
	// robust under parallel load (issue #606).
	waitForPID(repo, "sess-a", time.Second)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "sess-b",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/sess-b.jsonl",
		CWD:            cwd,
	}

	// Wait for the second session's PID before inspecting state — the
	// discovery goroutine writes state.PID on the shared pointer, so reading
	// it before the write completes both flakes and races (issue #606).
	waitForPID(repo, "sess-b", time.Second)
	cancel()
	<-done

	stateA, _ := repo.Load("sess-a")
	stateB, _ := repo.Load("sess-b")

	if stateA == nil {
		t.Fatal("sess-a should still exist (must not be deleted by sess-b's PID assignment)")
	}
	if stateB == nil {
		t.Fatal("sess-b should exist")
	}

	// Both sessions should exist and have different PIDs.
	if stateA.PID == stateB.PID {
		t.Errorf("sessions should have different PIDs, both got %d", stateA.PID)
	}
	if stateA.PID != 1001 {
		t.Errorf("sess-a PID: got %d, want 1001 (highest unclaimed)", stateA.PID)
	}
	if stateB.PID != 1000 {
		t.Errorf("sess-b PID: got %d, want 1000 (1001 already claimed)", stateB.PID)
	}
}

func TestSessionDetector_CWDFallback_CleansUpOldSessionOnClear(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete sess-a as a dead process.
	myPID := os.Getpid()

	cwd := t.TempDir() // see #321 — daemon rejects sessions with missing cwd

	// Mock CWD discovery returns only our PID — simulates the /clear scenario
	// where the same process starts a new transcript. The new session should
	// claim the PID and clean up the old session.
	cwdFn := func(cwd string, disambiguate func([]int) int) (int, error) {
		return disambiguate([]int{myPID}), nil
	}

	det := newDetectorWithCWDDiscovery(tw, pw, repo, cwdFn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk, then inject sessions.
	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Session A already has a PID assigned (discovered earlier).
	repo.Save(&session.SessionState{
		SessionID:      "sess-a",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            myPID,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-a.jsonl",
		CWD:            cwd,
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Session B has no PID yet (new transcript after /clear).
	repo.Save(&session.SessionState{
		SessionID:      "sess-b",
		Adapter:        "claude-code",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-b.jsonl",
		CWD:            cwd,
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Trigger activity on sess-b to initiate PID discovery.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "sess-b",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-b.jsonl",
	}

	// Wait for sess-b's PID assignment before inspecting — the discovery
	// goroutine writes state.PID on the shared pointer, so a fixed sleep both
	// flakes and races it under parallel load (issue #606).
	waitForPID(repo, "sess-b", time.Second)
	// The same-PID cleanup deletes sess-a in the same discovery goroutine,
	// right after the PID write — poll for that too so we don't read before
	// the goroutine finishes.
	waitForSessionDeleted(repo, "sess-a", time.Second)
	cancel()
	<-done

	// sess-a should be deleted — replaced by sess-b (same PID, /clear).
	stateA, _ := repo.Load("sess-a")
	if stateA != nil {
		t.Error("sess-a should be deleted (replaced by sess-b with same PID)")
	}

	// sess-b should have the PID.
	stateB, _ := repo.Load("sess-b")
	if stateB == nil {
		t.Fatal("sess-b should exist")
	}
	if stateB.PID != myPID {
		t.Errorf("sess-b PID: got %d, want %d", stateB.PID, myPID)
	}
}

// TestSessionDetector_ClearWithStaleMetadata_DeletesOldSessionImmediately is the
// end-to-end regression for #169. It drives the real claudecode.DiscoverPID
// with a stale ~/.claude/sessions/<pid>.json pointing at the old session and
// asserts the full pipeline — DiscoverPIDWithRetry → HandlePIDAssigned →
// same-PID cleanup — deletes the old session within the retry window.
func TestSessionDetector_ClearWithStaleMetadata_DeletesOldSessionImmediately(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete sess-old as a dead process.
	myPID := os.Getpid()

	// Install a fake sessionsDir with a stale metadata file that points at
	// the OLD sessionId — simulating Claude's post-/clear behaviour where
	// <pid>.json lingers on the previous session for up to ~2 min.
	sessionsDir := t.TempDir()
	if err := claudecode.WriteSessionMetaForTest(sessionsDir, myPID, "sess-old", time.Now().Add(-30*time.Second)); err != nil {
		t.Fatalf("write stale meta: %v", err)
	}

	// Real transcript file so DiscoverPID can stat its mtime (fresh, > stale
	// metadata + staleMetaSlack). Without a real file, the mtime gate would
	// be inert and current negative-filter behaviour would keep applying.
	transcriptDir := t.TempDir()
	newTranscript := filepath.Join(transcriptDir, "sess-new.jsonl")
	if err := os.WriteFile(newTranscript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	restore := claudecode.ReplaceTestDeps(
		sessionsDir,
		func(pid int) bool { return pid == myPID },
		func(_ string, _ string, disambiguate func([]int) int) (int, error) {
			return disambiguate([]int{myPID}), nil
		},
	)
	defer restore()

	discovers := map[string]agent.PIDDiscoverFunc{
		"claude-code": claudecode.DiscoverPID,
	}
	det := services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0, discovers, nil, nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk before injecting state.
	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	cwd := t.TempDir() // see #321 — daemon rejects sessions with missing cwd

	// Old session from before /clear — holds the live PID.
	repo.Save(&session.SessionState{
		SessionID:      "sess-old",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            myPID,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-old.jsonl",
		CWD:            cwd,
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// New session from after /clear — PID not yet discovered, fresh transcript.
	repo.Save(&session.SessionState{
		SessionID:      "sess-new",
		Adapter:        "claude-code",
		State:          session.StateReady,
		TranscriptPath: newTranscript,
		CWD:            cwd,
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Activity on sess-new triggers PID discovery. The real DiscoverPID must
	// see the stale metadata, skip the negative filter (mtime gate), return
	// myPID via the CWD stub, and fire HandlePIDAssigned's same-PID cleanup.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "sess-new",
		ProjectDir:     "-Users-test",
		TranscriptPath: newTranscript,
	}

	// Poll for discovery to complete instead of a fixed sleep — the spawned
	// discovery goroutine writes state.PID on the shared pointer, so reading
	// it before the write finishes both flakes and races under parallel load
	// (issue #606). sess-new's PID write happens first, then the same-PID
	// cleanup deletes sess-old in the same goroutine.
	waitForPID(repo, "sess-new", time.Second)
	waitForSessionDeleted(repo, "sess-old", time.Second)
	cancel()
	<-done

	if stateOld, _ := repo.Load("sess-old"); stateOld != nil {
		t.Error("sess-old should be deleted (stale metadata must not block /clear cleanup)")
	}
	stateNew, _ := repo.Load("sess-new")
	if stateNew == nil {
		t.Fatal("sess-new should exist")
	}
	if stateNew.PID != myPID {
		t.Errorf("sess-new PID: got %d, want %d", stateNew.PID, myPID)
	}
}

func TestSessionDetector_HandlePIDAssigned_CleansUpOldSession(t *testing.T) {
	// Verify that HandlePIDAssigned cleans up old sessions with the same PID
	// (the /clear scenario).
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Old session with PID 42 (from before /clear).
	repo.states["old"] = &session.SessionState{
		SessionID:      "old",
		State:          session.StateReady,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/old.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	// New session after /clear, PID not yet discovered.
	repo.states["new"] = &session.SessionState{
		SessionID:      "new",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/new.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)

	// PID assignment should clean up old session.
	det.HandlePIDAssigned(42, "new")

	if state, _ := repo.Load("old"); state != nil {
		t.Error("old session should be deleted by /clear cleanup")
	}
	newState, _ := repo.Load("new")
	if newState == nil {
		t.Fatal("new session should exist")
	}
	if newState.PID != 42 {
		t.Errorf("new session PID: got %d, want 42", newState.PID)
	}
}
