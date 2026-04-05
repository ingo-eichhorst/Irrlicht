package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// --- tests -------------------------------------------------------------------

func TestSessionDetector_NewSession_CreatesState(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "new1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/new1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, err := repo.Load("new1")
	if err != nil {
		t.Fatalf("session not created: %v", err)
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
	if state.TranscriptPath != "/home/.claude/projects/-Users-test-project/new1.jsonl" {
		t.Errorf("transcript_path: got %q", state.TranscriptPath)
	}
	if state.Confidence != "medium" {
		t.Errorf("confidence: got %q, want medium", state.Confidence)
	}
}

func TestSessionDetector_NewSession_SkipsOrphanTranscript(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	// Create a transcript file with an old mtime (orphan).
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "orphan1.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mtime to 10 minutes ago to exceed orphanTranscriptAge.
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(transcriptPath, old, old); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "orphan1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("orphan1")
	if state != nil {
		t.Errorf("orphan session should not be created, but found state %q", state.State)
	}
}

func TestSessionDetector_Activity_TransitionsToWaiting_WhenToolUseOpen(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["act1"] = &session.SessionState{
		SessionID:      "act1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/act1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     1,
		Metrics: &session.SessionMetrics{
			LastEventType:     "tool_use",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"AskUserQuestion"},
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "act1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/act1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("act1")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting (open tool call blocks on user)", state.State)
	}
}

func TestSessionDetector_Activity_TransitionsToReady_WhenAgentDone(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["wait1"] = &session.SessionState{
		SessionID:      "wait1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/wait1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     3,
		Metrics: &session.SessionMetrics{
			LastEventType:   "turn_done",
			HasOpenToolCall: false,
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "wait1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/wait1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("wait1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (turn_done signal, no open tools)", state.State)
	}
}

func TestSessionDetector_Activity_TransitionsToWaiting_WhenAssistantButOpenTools(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["otc1"] = &session.SessionState{
		SessionID:      "otc1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/otc1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"ExitPlanMode"},
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "otc1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/otc1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("otc1")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting (open tool call blocks on user)", state.State)
	}
}

func TestSessionDetector_Activity_CancellationFromWorking_TransitionsToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk to complete, then inject the session.
	// This avoids seedFromDisk re-evaluating the state before onActivity.
	time.Sleep(20 * time.Millisecond)

	// Simulate post-ESC state: session was working, user cancelled via ESC.
	// Claude Code writes tool_result rejections (is_error=true) followed by
	// "[Request interrupted by user for tool use]".
	repo.Save(&session.SessionState{
		SessionID:      "esc1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/esc1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:          "user",
			HasOpenToolCall:        false,
			LastToolResultWasError: true,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "esc1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/esc1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("esc1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (ESC cancellation: user event while working, no open tools)", state.State)
	}
}

func TestSessionDetector_Activity_CancellationFromWaiting_TransitionsToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk to complete, then inject the session.
	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:        "wake1",
		State:            session.StateWaiting,
		TranscriptPath:   "/home/.claude/projects/-Users-test/wake1.jsonl",
		FirstSeen:        now,
		UpdatedAt:        now,
		WaitingStartTime: &now,
		EventCount:       3,
		Metrics: &session.SessionMetrics{
			LastEventType:          "user",
			HasOpenToolCall:        false,
			LastToolResultWasError: true,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "wake1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/wake1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("wake1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (ESC from permission prompt)", state.State)
	}
}

func TestSessionDetector_Activity_NormalToolCompletion_StaysWorking(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	// Simulate mid-turn state: last tool_result completed normally
	// (is_error=false). Agent is still working between tool calls.
	repo.Save(&session.SessionState{
		SessionID:      "fp1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/fp1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:          "user",
			HasOpenToolCall:        false,
			LastToolResultWasError: false,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "fp1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/fp1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("fp1")
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (normal tool completion should not transition to ready)", state.State)
	}
}

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

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("rm1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
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

func TestSessionDetector_Activity_UnknownSession_TreatedAsNew(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "unknown1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/unknown1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, err := repo.Load("unknown1")
	if err != nil {
		t.Fatalf("session should have been created: %v", err)
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
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

	det.HandleProcessExit(12345, "exit1")

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

	det.HandleProcessExit(12345, "exit2")

	state, _ := repo.Load("exit2")
	if state != nil {
		t.Errorf("ready session should be deleted on process exit, but still exists")
	}
}

func TestSessionDetector_HandleProcessExit_UnknownSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	// Should not panic for unknown session.
	det.HandleProcessExit(99999, "nonexistent")
}

func TestSessionDetector_SeedFromDisk_DeletesDeadPIDs(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// PIDs 42 and 99 don't exist as real processes, so syscall.Kill(pid, 0)
	// returns ESRCH. All sessions with dead PIDs should be deleted.
	repo.states["seed1"] = &session.SessionState{
		SessionID:      "seed1",
		State:          session.StateWorking,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/seed1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}
	repo.states["seed2"] = &session.SessionState{
		SessionID: "seed2",
		State:     session.StateReady,
		PID:       99,
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
		t.Error("seed1 should be deleted (PID 42 is dead)")
	}
	// seed2 has a dead PID — should be deleted.
	if state, _ := repo.Load("seed2"); state != nil {
		t.Error("seed2 should be deleted (PID 99 is dead)")
	}

	// Dead PIDs should NOT be registered with ProcessWatcher.
	if _, ok := pw.watched[42]; ok {
		t.Error("PID 42 should not be watched (dead process)")
	}
	if _, ok := pw.watched[99]; ok {
		t.Error("PID 99 should not be watched (dead process)")
	}
}

func TestNeedsUserAttention(t *testing.T) {
	tests := []struct {
		name    string
		metrics *session.SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"no open tools", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: false}, false},
		{"open tool call (Bash)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}, false},
		{"open tool call (Write)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Write"}}, false},
		{"open tool call (Agent)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Agent"}}, false},
		{"open tool call (mcp__tool)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"mcp__claude-in-chrome__navigate"}}, false},
		{"open tool call (AskUserQuestion)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"AskUserQuestion"}}, true},
		{"open tool call (ExitPlanMode)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"ExitPlanMode"}}, true},
		{"mixed tools with AskUserQuestion", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash", "AskUserQuestion"}}, true},
		{"open tool call, no names", &session.SessionMetrics{HasOpenToolCall: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metrics.NeedsUserAttention()
			if got != tt.want {
				t.Errorf("NeedsUserAttention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionDetector_PIDAssigned_MergesIntoOldSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Old session: real transcript session with known PID.
	repo.states["old-session"] = &session.SessionState{
		SessionID:      "old-session",
		State:          session.StateReady,
		PID:            42,
		CWD:            "/Users/test/project",
		TranscriptPath: "/home/.claude/projects/-Users-test/old-session.jsonl",
		FirstSeen:      now - 300,
		UpdatedAt:      now,
	}

	// New session: just created after /clear, PID not yet discovered.
	repo.states["new-session"] = &session.SessionState{
		SessionID:      "new-session",
		State:          session.StateReady,
		CWD:            "/Users/test/project",
		TranscriptPath: "/home/.claude/projects/-Users-test/new-session.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)

	// Simulate PID discovery for the new session — same PID as old session.
	det.HandlePIDAssigned(42, "new-session")

	// Old session should survive with the new transcript path.
	oldState, _ := repo.Load("old-session")
	if oldState == nil {
		t.Fatal("old session should survive the merge")
	}
	if oldState.TranscriptPath != "/home/.claude/projects/-Users-test/new-session.jsonl" {
		t.Errorf("old session transcript: got %q, want new-session.jsonl", oldState.TranscriptPath)
	}
	if oldState.State != session.StateReady {
		t.Errorf("old session state: got %q, want ready", oldState.State)
	}
	if oldState.FirstSeen != now-300 {
		t.Errorf("old session FirstSeen should be preserved, got %d", oldState.FirstSeen)
	}

	// New session should be deleted (absorbed into old).
	if state, _ := repo.Load("new-session"); state != nil {
		t.Errorf("new session should be deleted after merge, but still exists")
	}

	// ProcessWatcher should track the PID for the OLD session.
	if pw.watched[42] != "old-session" {
		t.Errorf("ProcessWatcher: got %q for PID 42, want old-session", pw.watched[42])
	}
}

func TestSessionDetector_PIDAssigned_NoMergeAcrossProjects(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Session in project A with PID 42.
	repo.states["proj-a"] = &session.SessionState{
		SessionID:      "proj-a",
		State:          session.StateReady,
		PID:            42,
		CWD:            "/Users/test/project-a",
		TranscriptPath: "/home/.claude/projects/-Users-test-project-a/proj-a.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	// Session in project B, PID not yet discovered.
	repo.states["proj-b"] = &session.SessionState{
		SessionID:      "proj-b",
		State:          session.StateReady,
		CWD:            "/Users/test/project-b",
		TranscriptPath: "/home/.claude/projects/-Users-test-project-b/proj-b.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)

	// Assign same PID to proj-b (authoritative). Different CWD means no merge.
	det.HandlePIDAssigned(42, "proj-b")

	// Both sessions should exist (no merge across projects).
	if s, _ := repo.Load("proj-a"); s == nil {
		t.Error("proj-a should still exist")
	}
	if s, _ := repo.Load("proj-b"); s == nil {
		t.Error("proj-b should still exist")
	}
}

func TestSessionDetector_TranscriptAlreadyOwned(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Inject a session that adopted a transcript via merge (after Run starts
	// to avoid seedFromDisk deleting it for a dead PID).
	repo.Save(&session.SessionState{
		SessionID:      "merged",
		State:          session.StateReady,
		PID:            os.Getpid(),
		TranscriptPath: "/home/.claude/projects/-Users-test/new.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Fsnotify fires EventNewSession for the same transcript (different session ID).
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "new-duplicate",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/new.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// The duplicate should NOT be created.
	if s, _ := repo.Load("new-duplicate"); s != nil {
		t.Error("duplicate session should not be created when transcript is already owned")
	}

	// The merged session should still exist unchanged.
	if s, _ := repo.Load("merged"); s == nil {
		t.Fatal("merged session should still exist")
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
		CWD:            "/Users/test/project",
	}

	// Wait for first session's PID discovery retry goroutine to complete.
	time.Sleep(200 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "sess-b",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/sess-b.jsonl",
		CWD:            "/Users/test/project",
	}

	// Wait for second session's PID discovery.
	time.Sleep(200 * time.Millisecond)
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

func TestSessionDetector_CWDFallback_SkipsAlreadyClaimedPID(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete sess-a as a dead process.
	myPID := os.Getpid()

	// Mock CWD discovery returns only our PID — the only candidate is
	// already claimed by sess-a, so the disambiguator should filter it out
	// and sess-b should NOT get a PID assigned at all.
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
		State:          session.StateWorking,
		PID:            myPID,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-a.jsonl",
		CWD:            "/Users/test/project",
		FirstSeen:      now,
		UpdatedAt:       now,
	})

	// Session B has no PID yet.
	repo.Save(&session.SessionState{
		SessionID:      "sess-b",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-b.jsonl",
		CWD:            "/Users/test/project",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Trigger activity on sess-b to initiate PID discovery (CWD fallback).
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "sess-b",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-b.jsonl",
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// sess-a must NOT be deleted.
	stateA, _ := repo.Load("sess-a")
	if stateA == nil {
		t.Error("sess-a must not be deleted")
	}

	// sess-b should still exist but with PID=0 (no unclaimed PID available).
	stateB, _ := repo.Load("sess-b")
	if stateB == nil {
		t.Fatal("sess-b should exist")
	}
	if stateB.PID != 0 {
		t.Errorf("sess-b PID: got %d, want 0 (all candidates were claimed)", stateB.PID)
	}
}

func TestSessionDetector_LsofPath_MergesOnClear(t *testing.T) {
	// Verify that authoritative PID assignment (HandlePIDAssigned) merges
	// the new session into the old one, preserving session identity.
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Old session with PID 42 (from before /clear).
	repo.states["old"] = &session.SessionState{
		SessionID:      "old",
		State:          session.StateReady,
		PID:            42,
		CWD:            "/Users/test/project",
		TranscriptPath: "/home/.claude/projects/-Users-test/old.jsonl",
		FirstSeen:      now - 600,
		UpdatedAt:      now,
	}

	// New session after /clear, PID not yet discovered.
	repo.states["new"] = &session.SessionState{
		SessionID:      "new",
		State:          session.StateReady,
		CWD:            "/Users/test/project",
		TranscriptPath: "/home/.claude/projects/-Users-test/new.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)

	// Authoritative PID assignment should merge new into old.
	det.HandlePIDAssigned(42, "new")

	// Old session survives with new transcript.
	oldState, _ := repo.Load("old")
	if oldState == nil {
		t.Fatal("old session should survive the merge")
	}
	if oldState.TranscriptPath != "/home/.claude/projects/-Users-test/new.jsonl" {
		t.Errorf("old session transcript: got %q, want new.jsonl", oldState.TranscriptPath)
	}
	if oldState.FirstSeen != now-600 {
		t.Error("old session FirstSeen should be preserved")
	}

	// New session should be deleted (absorbed).
	if state, _ := repo.Load("new"); state != nil {
		t.Error("new session should be deleted after merge")
	}

	// ProcessWatcher should track the PID for the OLD session.
	if pw.watched[42] != "old" {
		t.Errorf("ProcessWatcher: got %q for PID 42, want old", pw.watched[42])
	}
}

func TestIsAgentDone(t *testing.T) {
	tests := []struct {
		name    string
		metrics *session.SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"turn_done", &session.SessionMetrics{LastEventType: "turn_done"}, true},
		{"turn_done, open tools (subagent running)", &session.SessionMetrics{LastEventType: "turn_done", HasOpenToolCall: true}, false},
		{"assistant_message, no open tools (legacy)", &session.SessionMetrics{LastEventType: "assistant_message", HasOpenToolCall: false}, true},
		{"assistant, no open tools (intermediate — NOT done)", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: false}, false},
		{"assistant, open tools", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: true}, false},
		{"user, no open tools", &session.SessionMetrics{LastEventType: "user", HasOpenToolCall: false}, false},
		{"empty", &session.SessionMetrics{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metrics.IsAgentDone()
			if got != tt.want {
				t.Errorf("IsAgentDone() = %v, want %v", got, tt.want)
			}
		})
	}
}
