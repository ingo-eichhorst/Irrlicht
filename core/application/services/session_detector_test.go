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
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working", state.State)
	}
	if state.TranscriptPath != "/home/.claude/projects/-Users-test-project/new1.jsonl" {
		t.Errorf("transcript_path: got %q", state.TranscriptPath)
	}
	if state.Confidence != "medium" {
		t.Errorf("confidence: got %q, want medium", state.Confidence)
	}
}

func TestSessionDetector_Activity_StaysWorking_WhenToolUse(t *testing.T) {
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
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (tool_use is still working)", state.State)
	}
}

func TestSessionDetector_Activity_TransitionsToWaiting_WhenAssistantDone(t *testing.T) {
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
		SessionID:      "wait1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/wait1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("wait1")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting (assistant done, no open tools)", state.State)
	}
	if state.WaitingStartTime == nil {
		t.Error("WaitingStartTime should be set")
	}
}

func TestSessionDetector_Activity_StaysWorking_WhenAssistantButOpenTools(t *testing.T) {
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
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (open tool call)", state.State)
	}
}

func TestSessionDetector_Activity_WakesFromWaiting(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()
	repo.states["wake1"] = &session.SessionState{
		SessionID:        "wake1",
		State:            session.StateWaiting,
		TranscriptPath:   "/home/.claude/projects/-Users-test/wake1.jsonl",
		FirstSeen:        now,
		UpdatedAt:        now,
		WaitingStartTime: &now,
		EventCount:       3,
		Metrics: &session.SessionMetrics{
			LastEventType:   "user",
			HasOpenToolCall: false,
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

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
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working", state.State)
	}
	if state.WaitingStartTime != nil {
		t.Error("WaitingStartTime should be cleared")
	}
}

func TestSessionDetector_Removed_TransitionsToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["rm1"] = &session.SessionState{
		SessionID: "rm1",
		State:     session.StateWorking,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
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
		SessionID: "rm2",
		State:     session.StateReady,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
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

func TestSessionDetector_DeriveParentSessionID_OpenToolCall(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["parent1"] = &session.SessionState{
		SessionID:      "parent1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{HasOpenToolCall: true},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
	}
	time.Sleep(30 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "child1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/child1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, err := repo.Load("child1")
	if err != nil {
		t.Fatalf("child session not found: %v", err)
	}
	if state.ParentSessionID != "parent1" {
		t.Errorf("parent_session_id: got %q, want parent1", state.ParentSessionID)
	}
}

func TestSessionDetector_DeriveParentSessionID_SingleWorking(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["parent2"] = &session.SessionState{
		SessionID:      "parent2",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent2",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2.jsonl",
	}
	time.Sleep(30 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "child2",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/child2.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("child2")
	if state.ParentSessionID != "parent2" {
		t.Errorf("parent_session_id: got %q, want parent2", state.ParentSessionID)
	}
}

func TestSessionDetector_NoParent_DifferentProjectDir(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["other1"] = &session.SessionState{
		SessionID:      "other1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-other/other1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{HasOpenToolCall: true},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "other1",
		ProjectDir:     "-Users-other",
		TranscriptPath: "/home/.claude/projects/-Users-other/other1.jsonl",
	}
	time.Sleep(30 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "lone1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/lone1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("lone1")
	if state.ParentSessionID != "" {
		t.Errorf("should have no parent (different dir), got %q", state.ParentSessionID)
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
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working", state.State)
	}
}

func TestSessionDetector_HandleProcessExit_TransitionsToReady(t *testing.T) {
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
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
	if state.Confidence != "high" {
		t.Errorf("confidence: got %q, want high", state.Confidence)
	}
	if state.LastEvent != "process_exit" {
		t.Errorf("last_event: got %q, want process_exit", state.LastEvent)
	}
}

func TestSessionDetector_HandleProcessExit_SkipsTerminalState(t *testing.T) {
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
	if state.State != session.StateReady {
		t.Errorf("state should remain ready, got %q", state.State)
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

func TestSessionDetector_SeedFromDisk_RegistersKnownPIDs(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

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

	if sid, ok := pw.watched[42]; !ok {
		t.Error("PID 42 should be watched")
	} else if sid != "seed1" {
		t.Errorf("PID 42 session: got %q, want seed1", sid)
	}

	if _, ok := pw.watched[99]; ok {
		t.Error("PID 99 should not be watched (ready session)")
	}
}

func TestIsWaitingForInput(t *testing.T) {
	tests := []struct {
		name    string
		metrics *session.SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"assistant, no open tools", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: false}, true},
		{"assistant_message, no open tools", &session.SessionMetrics{LastEventType: "assistant_message", HasOpenToolCall: false}, true},
		{"assistant_output, no open tools", &session.SessionMetrics{LastEventType: "assistant_output", HasOpenToolCall: false}, true},
		{"assistant, open tools", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: true}, false},
		{"tool_use", &session.SessionMetrics{LastEventType: "tool_use", HasOpenToolCall: true}, false},
		{"tool_result", &session.SessionMetrics{LastEventType: "tool_result", HasOpenToolCall: false}, false},
		{"user", &session.SessionMetrics{LastEventType: "user", HasOpenToolCall: false}, false},
		{"empty", &session.SessionMetrics{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metrics.IsWaitingForInput()
			if got != tt.want {
				t.Errorf("IsWaitingForInput() = %v, want %v", got, tt.want)
			}
		})
	}
}
