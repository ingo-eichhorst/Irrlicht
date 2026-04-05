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
			TurnDone:        true,
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

func TestSessionDetector_Activity_StaysWorking_WhenAssistantMidTurn(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Mid-turn: last event is "assistant" with no open tools, but no turn_done.
	// This happens between tool calls — should NOT transition to ready.
	repo.states["nosys1"] = &session.SessionState{
		SessionID:      "nosys1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/nosys1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     3,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant",
			HasOpenToolCall: false,
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "nosys1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/nosys1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("nosys1")
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (mid-turn assistant with no turn_done should stay working)", state.State)
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
		Adapter:        "claude-code",
		SessionID:      "sess-a",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/sess-a.jsonl",
		CWD:            "/Users/test/project",
	}

	// Wait for first session's PID discovery retry goroutine to complete.
	time.Sleep(200 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		Adapter:        "claude-code",
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

func TestSessionDetector_CWDFallback_CleansUpOldSessionOnClear(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete sess-a as a dead process.
	myPID := os.Getpid()

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
		CWD:            "/Users/test/project",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Session B has no PID yet (new transcript after /clear).
	repo.Save(&session.SessionState{
		SessionID:      "sess-b",
		Adapter:        "claude-code",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-b.jsonl",
		CWD:            "/Users/test/project",
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

	time.Sleep(200 * time.Millisecond)
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

func TestIsAgentDone(t *testing.T) {
	tests := []struct {
		name    string
		metrics *session.SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"TurnDone flag set", &session.SessionMetrics{TurnDone: true}, true},
		{"TurnDone with open tools (subagent running)", &session.SessionMetrics{TurnDone: true, HasOpenToolCall: true}, false},
		{"turn_done event without TurnDone flag", &session.SessionMetrics{LastEventType: "turn_done"}, false},
		{"assistant_message, no open tools (Codex fallback)", &session.SessionMetrics{LastEventType: "assistant_message", HasOpenToolCall: false}, true},
		{"assistant_output, no open tools (Codex fallback)", &session.SessionMetrics{LastEventType: "assistant_output", HasOpenToolCall: false}, true},
		{"assistant, no open tools (mid-turn — NOT done)", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: false}, false},
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

// --- stale tool call timer tests ---------------------------------------------

func TestSessionDetector_StaleToolCall_TransitionsToWaiting(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetectorWithStaleTimeout(tw, pw, repo, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond) // wait for seedFromDisk

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "stale1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/stale1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			HasOpenToolCall:   true,
			OpenToolCallCount: 1,
			LastOpenToolNames: []string{"Read"},
			LastEventType:     "assistant",
			PermissionMode:    "default",
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "stale1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/stale1.jsonl",
	}

	// Wait for stale tool timeout + buffer.
	time.Sleep(250 * time.Millisecond)

	state, _ := repo.Load("stale1")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting (stale tool call after timeout)", state.State)
	}
	if state.LastEvent != "stale_tool_timeout" {
		t.Errorf("last_event: got %q, want stale_tool_timeout", state.LastEvent)
	}

	cancel()
	<-done
}

func TestSessionDetector_StaleToolCall_CancelledByActivity(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetectorWithStaleTimeout(tw, pw, repo, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "cancel1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/cancel1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			HasOpenToolCall:   true,
			OpenToolCallCount: 1,
			LastOpenToolNames: []string{"Bash"},
			LastEventType:     "assistant",
			PermissionMode:    "default",
		},
	})

	// First activity starts the timer.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "cancel1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/cancel1.jsonl",
	}

	// Send new activity before timer fires (simulating tool_result arrival).
	time.Sleep(50 * time.Millisecond)
	// Update metrics to simulate tool completion.
	state, _ := repo.Load("cancel1")
	state.Metrics.HasOpenToolCall = false
	state.Metrics.OpenToolCallCount = 0
	state.Metrics.LastOpenToolNames = nil
	state.Metrics.LastEventType = "turn_done"
	state.Metrics.TurnDone = true
	repo.Save(state)

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "cancel1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/cancel1.jsonl",
	}

	// Wait past original timeout.
	time.Sleep(300 * time.Millisecond)

	state, _ = repo.Load("cancel1")
	if state.State == session.StateWaiting {
		t.Errorf("state: got waiting, want ready or working (activity cancelled stale timer)")
	}

	cancel()
	<-done
}

func TestSessionDetector_StaleToolCall_SkipsAgentOnlyTools(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetectorWithStaleTimeout(tw, pw, repo, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "agent1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/agent1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			HasOpenToolCall:   true,
			OpenToolCallCount: 2,
			LastOpenToolNames: []string{"Agent", "Agent"},
			LastEventType:     "assistant",
			PermissionMode:    "default",
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "agent1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/agent1.jsonl",
	}

	// Wait past timeout.
	time.Sleep(250 * time.Millisecond)

	state, _ := repo.Load("agent1")
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (Agent-only tools should not trigger stale timer)", state.State)
	}

	cancel()
	<-done
}

func TestSessionDetector_StaleToolCall_SkipsBypassPermissions(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetectorWithStaleTimeout(tw, pw, repo, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "bypass1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/bypass1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			HasOpenToolCall:   true,
			OpenToolCallCount: 1,
			LastOpenToolNames: []string{"Bash"},
			LastEventType:     "assistant",
			PermissionMode:    "bypassPermissions",
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "bypass1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/bypass1.jsonl",
	}

	// Wait past timeout.
	time.Sleep(250 * time.Millisecond)

	state, _ := repo.Load("bypass1")
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (bypassPermissions should not trigger stale timer)", state.State)
	}

	cancel()
	<-done
}

func TestSessionDetector_StaleToolCall_GuardChecksState(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetectorWithStaleTimeout(tw, pw, repo, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "guard1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/guard1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			HasOpenToolCall:   true,
			OpenToolCallCount: 1,
			LastOpenToolNames: []string{"Read"},
			LastEventType:     "assistant",
			PermissionMode:    "default",
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "guard1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/guard1.jsonl",
	}

	// Manually change session state to "ready" before timer fires.
	time.Sleep(30 * time.Millisecond)
	state, _ := repo.Load("guard1")
	state.State = session.StateReady
	state.UpdatedAt = time.Now().Unix() // different from expectedUpdatedAt
	repo.Save(state)

	// Wait for timer to fire.
	time.Sleep(200 * time.Millisecond)

	state, _ = repo.Load("guard1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (guard should prevent overwrite)", state.State)
	}

	cancel()
	<-done
}

// --- parent-child state propagation tests ------------------------------------

func TestSessionDetector_ParentHeldWorking_WhenChildrenActive(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Parent session: turn is done (TurnDone=true), no open tool calls.
	// Without children this would transition to ready.
	repo.Save(&session.SessionState{
		SessionID:      "parent1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			TurnDone:          true,
			LastEventType:     "turn_done",
			HasOpenToolCall:   false,
			LastAssistantText: "Done.",
		},
	})

	// Child session: still working.
	repo.Save(&session.SessionState{
		SessionID:       "child1",
		State:           session.StateWorking,
		ParentSessionID: "parent1",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent1/subagents/child1.jsonl",
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"Bash"},
		},
	})

	// Trigger parent activity — should be held in working because child is active.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	state, _ := repo.Load("parent1")
	if state.State != session.StateWorking {
		t.Errorf("parent state: got %q, want working (child still active)", state.State)
	}

	cancel()
	<-done
}

func TestSessionDetector_ParentReleasedToReady_WhenChildFinishes(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Parent: turn done, held in working because of child.
	repo.Save(&session.SessionState{
		SessionID:      "parent2",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			TurnDone:          true,
			LastEventType:     "turn_done",
			HasOpenToolCall:   false,
			LastAssistantText: "Done.",
		},
	})

	// Child: still working.
	repo.Save(&session.SessionState{
		SessionID:       "child2",
		State:           session.StateWorking,
		ParentSessionID: "parent2",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent2/subagents/child2.jsonl",
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			TurnDone:        true,
			LastEventType:   "turn_done",
			HasOpenToolCall: false,
		},
	})

	// Trigger child activity — child now has turn_done, transitions to ready.
	// This should trigger parent re-evaluation → parent also goes ready.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "child2",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2/subagents/child2.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	parent, _ := repo.Load("parent2")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateReady {
		t.Errorf("parent state: got %q, want ready (child finished, parent turn was done)", parent.State)
	}

	cancel()
	<-done
}

func TestSessionDetector_ParentNotAffected_WhenNoChildren(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Session with no children, turn done → should transition to ready normally.
	repo.Save(&session.SessionState{
		SessionID:      "solo1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/solo1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			TurnDone:          true,
			LastEventType:     "turn_done",
			HasOpenToolCall:   false,
			LastAssistantText: "All done.",
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "solo1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/solo1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	state, _ := repo.Load("solo1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (no children, turn done)", state.State)
	}

	cancel()
	<-done
}
