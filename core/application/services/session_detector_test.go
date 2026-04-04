package services_test

import (
	"context"
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
			LastEventType:   "tool_use",
			HasOpenToolCall: true,
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
			LastEventType:   "assistant",
			HasOpenToolCall: true,
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
	// returns ESRCH. Sessions with transcripts should be kept as ready;
	// pre-sessions (no transcript) should be deleted.
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

	// seed1 has a transcript — should be kept as ready with PID cleared.
	if state, _ := repo.Load("seed1"); state == nil {
		t.Error("seed1 should be kept (has transcript)")
	} else {
		if state.State != session.StateReady {
			t.Errorf("seed1 state: got %q, want ready", state.State)
		}
		if state.PID != 0 {
			t.Errorf("seed1 PID: got %d, want 0 (cleared)", state.PID)
		}
	}
	// seed2 has no transcript (pre-session) — should be deleted.
	if state, _ := repo.Load("seed2"); state != nil {
		t.Error("seed2 should be deleted (pre-session, PID 99 is dead)")
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
		{"open tool call (Bash)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}, true},
		{"open tool call (Write)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Write"}}, true},
		{"open tool call (AskUserQuestion)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"AskUserQuestion"}}, true},
		{"open tool call, no names", &session.SessionMetrics{HasOpenToolCall: true}, true},
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
