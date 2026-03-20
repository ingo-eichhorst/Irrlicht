package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/domain/transcript"
)

// --- SessionDetector mock dependencies ---------------------------------------

// mockTranscriptWatcher implements outbound.TranscriptWatcher for tests.
type mockTranscriptWatcher struct {
	ch     chan transcript.TranscriptEvent
	unsubs int
}

func newMockTranscriptWatcher() *mockTranscriptWatcher {
	return &mockTranscriptWatcher{
		ch: make(chan transcript.TranscriptEvent, 16),
	}
}

func (w *mockTranscriptWatcher) Watch(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (w *mockTranscriptWatcher) Subscribe() <-chan transcript.TranscriptEvent {
	return w.ch
}

func (w *mockTranscriptWatcher) Unsubscribe(ch <-chan transcript.TranscriptEvent) {
	w.unsubs++
}

// mockProcessWatcher implements outbound.ProcessWatcher for tests.
type mockProcessWatcher struct {
	watched map[int]string
}

func newMockProcessWatcher() *mockProcessWatcher {
	return &mockProcessWatcher{watched: make(map[int]string)}
}

func (w *mockProcessWatcher) Watch(pid int, sessionID string) error {
	w.watched[pid] = sessionID
	return nil
}

func (w *mockProcessWatcher) Unwatch(pid int) {
	delete(w.watched, pid)
}

func (w *mockProcessWatcher) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (w *mockProcessWatcher) Close() error { return nil }

// mockGraceTimer implements outbound.GracePeriodTimer for tests.
type mockGraceTimer struct {
	resets   map[string]string // sessionID → transcriptPath
	stops    map[string]bool
	stopAll  int
}

func newMockGraceTimer() *mockGraceTimer {
	return &mockGraceTimer{
		resets: make(map[string]string),
		stops:  make(map[string]bool),
	}
}

func (t *mockGraceTimer) Reset(sessionID, transcriptPath string) {
	t.resets[sessionID] = transcriptPath
}

func (t *mockGraceTimer) Stop(sessionID string) {
	t.stops[sessionID] = true
}

func (t *mockGraceTimer) StopAll() {
	t.stopAll++
}

// --- helper to build SessionDetector for tests --------------------------------

func newDetector(
	tw *mockTranscriptWatcher,
	pw *mockProcessWatcher,
	gp *mockGraceTimer,
	repo *mockRepo,
) *services.SessionDetector {
	return services.NewSessionDetector(
		tw, pw, gp, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
	)
}

// --- tests -------------------------------------------------------------------

func TestSessionDetector_NewSession_CreatesState(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Send a new session event.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventNewSession,
		SessionID:      "new1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/new1.jsonl",
	}

	// Wait for processing.
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

	// Grace period timer should have been reset.
	if _, ok := gp.resets["new1"]; !ok {
		t.Error("grace period timer should have been reset for new session")
	}
}

func TestSessionDetector_Activity_ResetsGraceTimer(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	// Pre-create a session.
	repo.states["act1"] = &session.SessionState{
		SessionID:      "act1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/act1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     1,
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventActivity,
		SessionID:      "act1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/act1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if path, ok := gp.resets["act1"]; !ok {
		t.Error("grace period timer should have been reset")
	} else if path != "/home/.claude/projects/-Users-test/act1.jsonl" {
		t.Errorf("grace timer transcript path: got %q", path)
	}

	state, _ := repo.Load("act1")
	if state.EventCount != 2 {
		t.Errorf("event count: got %d, want 2", state.EventCount)
	}
}

func TestSessionDetector_Activity_WakesFromWaiting(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	now := time.Now().Unix()
	repo.states["wake1"] = &session.SessionState{
		SessionID:      "wake1",
		State:          session.StateWaiting,
		TranscriptPath: "/home/.claude/projects/-Users-test/wake1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		WaitingStartTime: &now,
		EventCount:     3,
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventActivity,
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
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["rm1"] = &session.SessionState{
		SessionID: "rm1",
		State:     session.StateWorking,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- transcript.TranscriptEvent{
		Type:      transcript.EventRemoved,
		SessionID: "rm1",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("rm1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}

	if !gp.stops["rm1"] {
		t.Error("grace period timer should have been stopped")
	}
}

func TestSessionDetector_Removed_SkipsTerminalState(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["rm2"] = &session.SessionState{
		SessionID: "rm2",
		State:     session.StateReady, // already terminal
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- transcript.TranscriptEvent{
		Type:      transcript.EventRemoved,
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

func TestSessionDetector_GracePeriodExpiry_TransitionsToWaiting(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["gp1"] = &session.SessionState{
		SessionID: "gp1",
		State:     session.StateWorking,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	det.HandleGracePeriodExpiry("gp1")

	state, _ := repo.Load("gp1")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting", state.State)
	}
	if state.WaitingStartTime == nil {
		t.Error("WaitingStartTime should be set")
	}
}

func TestSessionDetector_GracePeriodExpiry_SkipsNonWorking(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["gp2"] = &session.SessionState{
		SessionID: "gp2",
		State:     session.StateWaiting, // already waiting
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	det.HandleGracePeriodExpiry("gp2")

	state, _ := repo.Load("gp2")
	if state.State != session.StateWaiting {
		t.Errorf("state should remain waiting, got %q", state.State)
	}
}

func TestSessionDetector_DeriveParentSessionID_OpenToolCall(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	// Pre-existing parent session with open tool call.
	repo.states["parent1"] = &session.SessionState{
		SessionID:      "parent1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{HasOpenToolCall: true},
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// First, register the parent in projectSessions by sending an activity event.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventActivity,
		SessionID:      "parent1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
	}
	time.Sleep(30 * time.Millisecond)

	// Now a new session appears in the same project directory.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventNewSession,
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
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	// Parent working session without metrics (no HasOpenToolCall info).
	repo.states["parent2"] = &session.SessionState{
		SessionID:      "parent2",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Register parent.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventActivity,
		SessionID:      "parent2",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2.jsonl",
	}
	time.Sleep(30 * time.Millisecond)

	// New child session.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventNewSession,
		SessionID:      "child2",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/child2.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("child2")
	// Fallback: single working session in same project dir → parent.
	if state.ParentSessionID != "parent2" {
		t.Errorf("parent_session_id: got %q, want parent2", state.ParentSessionID)
	}
}

func TestSessionDetector_NoParent_DifferentProjectDir(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["other1"] = &session.SessionState{
		SessionID:      "other1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-other/other1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{HasOpenToolCall: true},
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Register "other1" in a different project dir.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventActivity,
		SessionID:      "other1",
		ProjectDir:     "-Users-other",
		TranscriptPath: "/home/.claude/projects/-Users-other/other1.jsonl",
	}
	time.Sleep(30 * time.Millisecond)

	// New session in a different project dir.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventNewSession,
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
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	// Session already exists (no transcript path yet).
	repo.states["hook1"] = &session.SessionState{
		SessionID: "hook1",
		State:     session.StateWorking,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventNewSession,
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
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	cancel()
	err := <-done

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if gp.stopAll != 1 {
		t.Errorf("StopAll should have been called once, got %d", gp.stopAll)
	}
	if tw.unsubs != 1 {
		t.Errorf("Unsubscribe should have been called once, got %d", tw.unsubs)
	}
}

func TestSessionDetector_Activity_UnknownSession_TreatedAsNew(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Activity event for an unknown session → should create it.
	tw.ch <- transcript.TranscriptEvent{
		Type:           transcript.EventActivity,
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
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["exit1"] = &session.SessionState{
		SessionID: "exit1",
		State:     session.StateWorking,
		PID:       12345,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

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

	// Grace period timer should have been stopped.
	if !gp.stops["exit1"] {
		t.Error("grace period timer should have been stopped")
	}
}

func TestSessionDetector_HandleProcessExit_SkipsTerminalState(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["exit2"] = &session.SessionState{
		SessionID: "exit2",
		State:     session.StateReady, // already terminal
		PID:       12345,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	det.HandleProcessExit(12345, "exit2")

	state, _ := repo.Load("exit2")
	if state.State != session.StateReady {
		t.Errorf("state should remain ready, got %q", state.State)
	}
}

func TestSessionDetector_HandleProcessExit_UnknownSession(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	det := newDetector(tw, pw, gp, repo)

	// Should not panic for unknown session.
	det.HandleProcessExit(99999, "nonexistent")

	// Grace period timer should still be stopped (defensive).
	if !gp.stops["nonexistent"] {
		t.Error("grace period timer should have been stopped even for unknown session")
	}
}

func TestSessionDetector_SeedFromDisk_RegistersKnownPIDs(t *testing.T) {
	tw := newMockTranscriptWatcher()
	pw := newMockProcessWatcher()
	gp := newMockGraceTimer()
	repo := newMockRepo()

	repo.states["seed1"] = &session.SessionState{
		SessionID:      "seed1",
		State:          session.StateWorking,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/seed1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}
	// Ready session should NOT be seeded.
	repo.states["seed2"] = &session.SessionState{
		SessionID: "seed2",
		State:     session.StateReady,
		PID:       99,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, gp, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Active session PID should be registered.
	if sid, ok := pw.watched[42]; !ok {
		t.Error("PID 42 should be watched")
	} else if sid != "seed1" {
		t.Errorf("PID 42 session: got %q, want seed1", sid)
	}

	// Ready session PID should NOT be registered.
	if _, ok := pw.watched[99]; ok {
		t.Error("PID 99 should not be watched (ready session)")
	}
}
