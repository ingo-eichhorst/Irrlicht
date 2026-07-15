package services_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

func TestSessionDetector_Removed_TransitionsToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["rm1"] = &session.SessionState{
		SessionID:      "rm1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/rm1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:      agent.EventRemoved,
		SessionID: "rm1",
	}

	// Poll for the removal transition instead of a fixed sleep — under
	// parallel-load scheduling the Run loop may not have processed the event
	// within a fixed window (issue #606 flaky sibling).
	waitForSessionState(repo, "rm1", session.StateReady, time.Second)
	cancel()
	<-done

	state, _ := repo.Load("rm1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
}

func TestSessionDetector_Removed_PrunesMetricsLedger(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	mm := &mockMetrics{}

	transcriptPath := "/home/.claude/projects/-Users-test/rm-prune.jsonl"
	repo.states["rm-prune"] = &session.SessionState{
		SessionID:      "rm-prune",
		State:          session.StateWorking,
		TranscriptPath: transcriptPath,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := services.NewSessionDetector([]inbound.Watcher{tw}, services.SessionDetectorDeps{
		PW:           pw,
		Repo:         repo,
		Log:          &mockLogger{},
		Git:          &mockGit{},
		Metrics:      mm,
		Broadcaster:  nil,
		Version:      "test",
		ReadyTTL:     0,
		PIDDiscovers: nil,
		ProcessNames: nil,
		LiveCWDs:     nil,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{Type: agent.EventRemoved, SessionID: "rm-prune"}

	// Poll for the prune instead of a fixed sleep — under parallel-load
	// scheduling the Run loop may not have reached PruneMetrics within a fixed
	// window (issue #606 flaky sibling). PruneEntry runs after the ready Save,
	// so wait on the prune log directly.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(mm.prunedSnapshot()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	pruned := mm.prunedSnapshot()
	if len(pruned) != 1 || pruned[0] != transcriptPath {
		t.Errorf("PruneEntry calls: got %v, want [%q]", pruned, transcriptPath)
	}
}

func TestSessionDetector_Removed_SkipsTerminalState(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["rm2"] = &session.SessionState{
		SessionID:      "rm2",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/rm2.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:      agent.EventRemoved,
		SessionID: "rm2",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("rm2")
	if state.State != session.StateReady {
		t.Errorf("state should remain ready, got %q", state.State)
	}
}

// A transcript "removal" that is really a relocation to a sibling project-dir
// slug (the same session cd'ing between the main repo and a git worktree) must
// NOT flip the live session to ready — it should re-point tracking at the moved
// file. The detection is direction-agnostic, so both entering a worktree
// (main→worktree slug) and closing one (worktree→main slug) are covered.
// Regression test for issue #877.
func TestSessionDetector_Removed_TranscriptRelocation_StaysAlive(t *testing.T) {
	const (
		mainSlug = "-Users-test"
		wtSlug   = "-Users-test--claude-worktrees-877"
	)
	tests := []struct {
		name     string
		fromSlug string // slug the transcript is renamed away from (the "removed" path)
		toSlug   string // slug it moved to (the surviving path)
	}{
		{name: "enter worktree", fromSlug: mainSlug, toSlug: wtSlug},
		{name: "close worktree", fromSlug: wtSlug, toSlug: mainSlug},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			toDir := filepath.Join(root, tc.toSlug)
			if err := os.MkdirAll(toDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// The "from" path is intentionally absent (renamed away); only the
			// destination copy exists on disk — exactly what Claude Code leaves
			// after a cwd change.
			fromPath := filepath.Join(root, tc.fromSlug, "reloc1.jsonl")
			toPath := filepath.Join(toDir, "reloc1.jsonl")
			if err := os.WriteFile(toPath, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			tw := newMockAgentWatcher()
			pw := newMockProcessWatcher()
			repo := newMockRepo()

			repo.states["reloc1"] = &session.SessionState{
				SessionID:      "reloc1",
				State:          session.StateWorking,
				TranscriptPath: fromPath,
				FirstSeen:      time.Now().Unix(),
				UpdatedAt:      time.Now().Unix(),
			}

			det := newDetector(tw, pw, repo)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- det.Run(ctx) }()

			tw.ch <- agent.Event{
				Type:           agent.EventRemoved,
				SessionID:      "reloc1",
				TranscriptPath: fromPath,
			}

			// Poll for the transcript-path follow via the race-free probe rather
			// than a fixed sleep (issue #606 flaky sibling) — background
			// goroutines mutate the shared *SessionState pointer, so a bare
			// Load().TranscriptPath read races.
			waitForCondition(func() bool {
				return repo.transcriptPathOf("reloc1") == toPath
			}, time.Second)
			cancel()
			<-done

			if got := repo.transcriptPathOf("reloc1"); got != toPath {
				t.Errorf("transcript_path: got %q, want %q (must follow the moved file)", got, toPath)
			}
			// The session must never have been flipped to ready.
			repo.mu.Lock()
			gotState := repo.lastSavedState["reloc1"]
			repo.mu.Unlock()
			if gotState == session.StateReady {
				t.Errorf("state: got %q, want working (a relocation must not force ready)", gotState)
			}
		})
	}
}

// The companion to the relocation test above: when the transcript is really
// gone — no surviving copy under any sibling slug — onRemoved must take the
// normal removal path and flip the session to ready.
//
// This is the documented fallback for issue #1088's first edge. relocatedTranscript
// depends on the destination already existing when the Remove arrives, which
// holds because Claude Code renames transcripts rather than copying them (see
// that function's doc comment for the on-disk census). This test pins what
// happens if that assumption is ever violated — a clean, self-recovering flip
// to ready rather than a hang or a wrong path — so the fallback stays a
// deliberate behaviour rather than an accident. It passes on main by
// construction; it is a lock, not a bug reproduction.
func TestSessionDetector_Removed_TranscriptGone_FlipsReady(t *testing.T) {
	root := t.TempDir()
	// Both slugs exist, but the transcript is absent from both: a genuine
	// deletion, not a relocation.
	gonePath := filepath.Join(root, "-Users-test", "gone1.jsonl")
	if err := os.MkdirAll(filepath.Join(root, "-Users-test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "-Users-test--claude-worktrees-1088"), 0o755); err != nil {
		t.Fatal(err)
	}

	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	repo.states["gone1"] = &session.SessionState{
		SessionID:      "gone1",
		State:          session.StateWorking,
		TranscriptPath: gonePath,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventRemoved,
		SessionID:      "gone1",
		TranscriptPath: gonePath,
	}

	waitForCondition(func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.lastSavedState["gone1"] == string(session.StateReady)
	}, time.Second)
	cancel()
	<-done

	repo.mu.Lock()
	gotState := repo.lastSavedState["gone1"]
	repo.mu.Unlock()
	if gotState != string(session.StateReady) {
		t.Errorf("state: got %q, want ready (a genuine removal must flip the session to ready)", gotState)
	}
	// The dead path must be left as-is — there is no surviving copy to follow.
	if got := repo.transcriptPathOf("gone1"); got != gonePath {
		t.Errorf("transcript_path: got %q, want %q (unchanged on a genuine removal)", got, gonePath)
	}
}

func TestSessionDetector_ExistingSession_UpdatesTranscriptPath(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["hook1"] = &session.SessionState{
		SessionID: "hook1",
		State:     session.StateWorking,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "hook1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/hook1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("hook1")
	if state.TranscriptPath != "/home/.claude/projects/-Users-test/hook1.jsonl" {
		t.Errorf("transcript_path should be updated, got %q", state.TranscriptPath)
	}
}

func TestSessionDetector_ContextCancel_StopsGracefully(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	cancel()
	err := <-done

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestSessionDetector_HandleProcessExit_DeletesSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["exit1"] = &session.SessionState{
		SessionID: "exit1",
		State:     session.StateWorking,
		PID:       12345,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	det.HandleProcessExit(12345, "exit1", "test: pid exited")

	state, _ := repo.Load("exit1")
	if state != nil {
		t.Errorf("session should be deleted, but still exists with state %q", state.State)
	}
}

func TestSessionDetector_HandleProcessExit_DeletesReadySession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["exit2"] = &session.SessionState{
		SessionID: "exit2",
		State:     session.StateReady,
		PID:       12345,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	det.HandleProcessExit(12345, "exit2", "test: pid exited")

	state, _ := repo.Load("exit2")
	if state != nil {
		t.Errorf("ready session should be deleted on process exit, but still exists")
	}
}

func TestSessionDetector_ContinueSession_RecreatableAfterProcessExit(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Session exists with a PID.
	repo.states["cont1"] = &session.SessionState{
		SessionID:      "cont1",
		State:          session.StateWorking,
		PID:            12345,
		TranscriptPath: "/tmp/test-cont1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)
	det.SetDeletedCooldown(0) // allow immediate re-creation

	// Process exits — session is deleted and added to deletedSessions.
	det.HandleProcessExit(12345, "cont1", "test: pid exited")

	state, _ := repo.Load("cont1")
	if state != nil {
		t.Fatal("session should be deleted after process exit")
	}

	// Start the event loop.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond) // wait for seedFromDisk

	// Create a fresh transcript file (simulating --continue writing to it).
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "cont1.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Activity event for the deleted session with a fresh transcript.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "cont1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Session should be re-created (--continue with fresh transcript).
	state, err := repo.Load("cont1")
	if err != nil || state == nil {
		t.Fatal("session should be re-created after --continue (fresh transcript)")
	}
	if state.TranscriptPath != transcriptPath {
		t.Errorf("transcript_path: got %q, want %q", state.TranscriptPath, transcriptPath)
	}
}

func TestSessionDetector_LateWriteAfterQuit_NoGhostSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	repo.states["ghost1"] = &session.SessionState{
		SessionID:      "ghost1",
		State:          session.StateWorking,
		PID:            12345,
		TranscriptPath: "/tmp/test-ghost1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)
	// Keep default 10s cooldown — late writes happen within milliseconds.

	// Process exits — session deleted.
	det.HandleProcessExit(12345, "ghost1", "test: pid exited")

	state, _ := repo.Load("ghost1")
	if state != nil {
		t.Fatal("session should be deleted after process exit")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	// Late-arriving write from the dying process (within cooldown).
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "ghost1.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"assistant"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "ghost1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Session should NOT be re-created — still within cooldown.
	state, _ = repo.Load("ghost1")
	if state != nil {
		t.Error("session should NOT be re-created from late writes after quit (within cooldown)")
	}
}

func TestSessionDetector_HandleProcessExit_UnknownSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	// Should not panic for unknown session.
	det.HandleProcessExit(99999, "nonexistent", "test: pid exited")
}

// TestSessionDetector_SeedFromDisk_PersistsRefreshedMetrics is a regression
// test for irrlicht-qha: after PR #110 fixed the codex parser to read the
// per-turn last_token_usage, persisted sessions from the pre-fix daemon
// still served the stale cumulative count. seedFromDisk called RefreshMetrics
// and mutated the in-memory state, but only Save()'d when the classified
// state transitioned — so idle ready sessions kept the bad numbers on disk
// indefinitely. The fix is to persist after RefreshMetrics regardless of
// whether the state changed.
func TestSessionDetector_SeedFromDisk_PersistsRefreshedMetrics(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete the session as dead.
	myPID := os.Getpid()

	// Stale persisted state: cumulative token count from the buggy daemon,
	// already-ready state (so the state transition path would not fire).
	repo.states["rollout-stale"] = &session.SessionState{
		SessionID:      "rollout-stale",
		State:          session.StateReady,
		Adapter:        "codex",
		PID:            myPID,
		TranscriptPath: "/tmp/rollout-stale.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			TotalTokens:        2282896, // stale cumulative
			ContextWindow:      258400,
			ContextUtilization: 883.47,
			ModelName:          "gpt-5.4",
			PressureLevel:      "critical",
		},
	}

	// Fresh tailer output: per-turn snapshot. seedFromDisk should merge this
	// into state.Metrics and then persist, overwriting the stale cumulative.
	freshMetrics := &session.SessionMetrics{
		TotalTokens:        123496,
		ContextWindow:      258400,
		ContextUtilization: 47.79,
		ModelName:          "gpt-5.4",
		PressureLevel:      "safe",
	}
	metrics := &funcMetrics{
		fn: func(path, adapter string) (*session.SessionMetrics, error) {
			if path == "/tmp/rollout-stale.jsonl" {
				// Return a fresh copy so the detector can mutate without
				// affecting subsequent calls.
				cp := *freshMetrics
				return &cp, nil
			}
			return nil, nil
		},
	}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)

	// Record the save count before Run: seedFromDisk must call Save() for
	// this session even though its classified state (ready) is unchanged,
	// otherwise the refreshed metrics would never reach disk. In the real
	// filesystem repo, an un-saved in-memory mutation is lost because Load
	// deep-copies from disk; the mockRepo hands back the same pointer, so
	// we assert on the Save call count, not the loaded state.
	repo.mu.Lock()
	savesBefore := repo.saves
	repo.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	repo.mu.Lock()
	savesAfter := repo.saves
	repo.mu.Unlock()
	if savesAfter <= savesBefore {
		t.Errorf("expected Save() to be called during seedFromDisk after "+
			"RefreshMetrics, but saves count did not increase (before=%d after=%d)",
			savesBefore, savesAfter)
	}

	state, err := repo.Load("rollout-stale")
	if err != nil || state == nil {
		t.Fatalf("rollout-stale should still exist after seed: err=%v state=%v", err, state)
	}
	if state.Metrics == nil {
		t.Fatal("state.Metrics is nil after seed")
	}
	if state.Metrics.TotalTokens != 123496 {
		t.Errorf("TotalTokens = %d, want 123496 (fresh). Stale cumulative leak?",
			state.Metrics.TotalTokens)
	}
	if state.Metrics.ContextUtilization < 40 || state.Metrics.ContextUtilization > 55 {
		t.Errorf("ContextUtilization = %.2f, want ~47.79", state.Metrics.ContextUtilization)
	}
}

func TestSessionDetector_SeedFromDisk_DeletesDeadPIDs(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Derive PIDs that are GUARANTEED dead: spawn a process, reap it, reuse the
	// now-defunct PID. syscall.Kill(pid, 0) on it returns ESRCH — the signal the
	// detector uses to classify a seeded session's process as dead. Hardcoded
	// low PIDs are not reliable here: 42/99 are live system daemons on some
	// hosts (CI macOS runners) and live kernel threads on a Linux CI box, which
	// made this test fail on both.
	deadPID1, deadPID2 := deadPID(t), deadPID(t)
	repo.states["seed1"] = &session.SessionState{
		SessionID:      "seed1",
		State:          session.StateWorking,
		PID:            deadPID1,
		TranscriptPath: "/home/.claude/projects/-Users-test/seed1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}
	repo.states["seed2"] = &session.SessionState{
		SessionID: "seed2",
		State:     session.StateReady,
		PID:       deadPID2,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// seed1 has a dead PID — should be deleted.
	if state, _ := repo.Load("seed1"); state != nil {
		t.Errorf("seed1 should be deleted (PID %d is dead)", deadPID1)
	}
	// seed2 has a dead PID — should be deleted.
	if state, _ := repo.Load("seed2"); state != nil {
		t.Errorf("seed2 should be deleted (PID %d is dead)", deadPID2)
	}

	// Dead PIDs should NOT be registered with ProcessWatcher.
	if _, ok := pw.watched[deadPID1]; ok {
		t.Errorf("PID %d should not be watched (dead process)", deadPID1)
	}
	if _, ok := pw.watched[deadPID2]; ok {
		t.Errorf("PID %d should not be watched (dead process)", deadPID2)
	}
}

// deadPID spawns a trivial process, waits for it to exit (which reaps it), and
// returns its now-defunct PID. Because the child has been reaped,
// syscall.Kill(pid, 0) returns ESRCH — exactly what the detector treats as a
// dead process — without relying on the host's low PIDs being unused.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn dead-pid helper: %v", err)
	}
	return cmd.Process.Pid
}

// TestSessionDetector_HandlePermissionHook_PreToolUseTransitionsToWaiting is
// the regression test for issue #307. The Claude Code transcript may not yet
// contain the AskUserQuestion tool_use (assistant message persistence can
// lag the overlay by minutes), so metrics show no open tool call. A
// PreToolUse hook fires synchronously when the model emits the tool_use —
// the detector must transition working → waiting on that signal alone,
// without depending on the JSONL flush.
func TestSessionDetector_HandlePermissionHook_PreToolUseTransitionsToWaiting(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sessID = "pre-307"
	const transcript = "/home/.claude/projects/-Users-test/" + sessID + ".jsonl"

	now := time.Now().Unix()
	repo.states[sessID] = &session.SessionState{
		SessionID:      sessID,
		State:          session.StateWorking,
		TranscriptPath: transcript,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     2,
		Metrics: &session.SessionMetrics{
			// Mimics the bug scenario: prior user turn flushed, but the
			// AskUserQuestion tool_use is still in Claude Code's write
			// buffer. No open tool call is visible to the classifier.
			LastEventType:   "user",
			HasOpenToolCall: false,
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// dispatchHookActivity enqueues onto a 64-buffered debouncedEvents
	// channel, so this can safely fire before Run()'s event loop starts
	// without dropping the event — no need to wait for the loop first.
	det.HandlePermissionHook(sessID, transcript, claudecode.HookPreToolUse)

	// Poll instead of a fixed sleep: under scheduler contention the event
	// loop can take longer than any fixed window to process the
	// hook-triggered activity event (issue #965).
	waitForSessionState(repo, sessID, session.StateWaiting, 2*time.Second)

	cancel()
	<-done

	state, _ := repo.Load(sessID)
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting (PreToolUse should flip working→waiting via permissionPending overlay)", state.State)
	}
}

// TestSessionDetector_SeedFromDisk_ConsentGated verifies the #570 consent
// gate: persisted sessions of an un-consented adapter must NOT have their
// transcripts re-read at startup (the upgrade contract — previously
// monitored agents pause until the wizard is answered), while consented
// adapters keep the normal seed refresh.
func TestSessionDetector_SeedFromDisk_ConsentGated(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	myPID := os.Getpid()

	mk := func(id, adapter string) *session.SessionState {
		return &session.SessionState{
			SessionID:      id,
			State:          session.StateReady,
			Adapter:        adapter,
			PID:            myPID,
			TranscriptPath: "/tmp/" + id + ".jsonl",
			FirstSeen:      time.Now().Unix(),
			UpdatedAt:      time.Now().Unix(),
		}
	}
	repo.states["granted-session"] = mk("granted-session", "codex")
	repo.states["pending-session"] = mk("pending-session", "claude-code")

	var mu sync.Mutex
	read := map[string]bool{}
	metrics := &funcMetrics{
		fn: func(path, adapter string) (*session.SessionMetrics, error) {
			mu.Lock()
			read[path] = true
			mu.Unlock()
			return nil, nil
		},
	}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	det.SetConsentGate(func(adapter string) bool { return adapter == "codex" })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !read["/tmp/granted-session.jsonl"] {
		t.Error("consented adapter's transcript was not refreshed at seed")
	}
	if read["/tmp/pending-session.jsonl"] {
		t.Error("un-consented adapter's transcript was read at seed — consent gate bypassed")
	}
}
