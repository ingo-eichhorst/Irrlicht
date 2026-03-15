package services_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"irrlicht/hook/application/services"
	"irrlicht/hook/domain/event"
	"irrlicht/hook/domain/session"
)

// --- mock implementations ----------------------------------------------------

type mockRepo struct {
	mu     sync.Mutex
	states map[string]*session.SessionState
	saves  int
}

func newMockRepo() *mockRepo {
	return &mockRepo{states: make(map[string]*session.SessionState)}
}

func (r *mockRepo) Load(sessionID string) (*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[sessionID]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

func (r *mockRepo) Save(s *session.SessionState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[s.SessionID] = s
	r.saves++
	return nil
}

func (r *mockRepo) Delete(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, sessionID)
	return nil
}

func (r *mockRepo) ListAll() ([]*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*session.SessionState, 0, len(r.states))
	for _, s := range r.states {
		out = append(out, s)
	}
	return out, nil
}

type mockLogger struct {
	mu      sync.Mutex
	infos   []string
	errors  []string
}

func (l *mockLogger) LogInfo(_, _, msg string) {
	l.mu.Lock()
	l.infos = append(l.infos, msg)
	l.mu.Unlock()
}
func (l *mockLogger) LogError(_, _, msg string) {
	l.mu.Lock()
	l.errors = append(l.errors, msg)
	l.mu.Unlock()
}
func (l *mockLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *mockLogger) Close() error                                              { return nil }

type mockGit struct{}

func (g *mockGit) GetBranch(dir string) string { return "main" }
func (g *mockGit) GetProjectName(dir string) string {
	if dir == "" {
		return ""
	}
	return "project"
}
func (g *mockGit) GetBranchFromTranscript(path string) string { return "" }

type mockMetrics struct{}

func (m *mockMetrics) ComputeMetrics(path string) (*session.SessionMetrics, error) {
	return nil, nil
}

type mockPathValidator struct{ err error }

func (v *mockPathValidator) Validate(path string) error { return v.err }

// --- helpers -----------------------------------------------------------------

func newSvc(repo *mockRepo) *services.EventService {
	return services.NewEventService(
		repo,
		&mockLogger{},
		&mockGit{},
		&mockMetrics{},
		&mockPathValidator{},
	)
}

func sessionStartEvent(sid string) *event.HookEvent {
	return &event.HookEvent{
		HookEventName: "SessionStart",
		SessionID:     sid,
		Matcher:       "startup",
	}
}

// --- tests -------------------------------------------------------------------

func TestHandleEvent_SessionStart_CreatesState(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	if err := svc.HandleEvent(sessionStartEvent("sid1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, err := repo.Load("sid1")
	if err != nil {
		t.Fatalf("state not saved: %v", err)
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
	if state.EventCount != 1 {
		t.Errorf("event count: got %d, want 1", state.EventCount)
	}
}

func TestHandleEvent_SessionEnd_DeletesState(t *testing.T) {
	repo := newMockRepo()
	repo.states["sid2"] = &session.SessionState{
		SessionID: "sid2",
		State:     session.StateWorking,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	svc := newSvc(repo)

	evt := &event.HookEvent{
		HookEventName: "SessionEnd",
		SessionID:     "sid2",
		Reason:        "clear",
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Load("sid2"); err == nil {
		t.Error("expected session to be deleted, but it still exists")
	}
}

func TestHandleEvent_Notification_SetsWaiting(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{HookEventName: "Notification", SessionID: "sid3"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("sid3")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting", state.State)
	}
}

func TestHandleEvent_InvalidEvent_ReturnsError(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{HookEventName: "BadEvent", SessionID: "sid"}
	if err := svc.HandleEvent(evt); err == nil {
		t.Error("expected validation error, got nil")
	}
}

func TestHandleEvent_PathValidationError(t *testing.T) {
	repo := newMockRepo()
	log := &mockLogger{}
	svc := services.NewEventService(
		repo, log, &mockGit{}, &mockMetrics{},
		&mockPathValidator{err: fmt.Errorf("bad path")},
	)
	evt := &event.HookEvent{
		HookEventName:  "SessionStart",
		SessionID:      "sid",
		Matcher:        "startup",
		TranscriptPath: "/bad/path",
	}
	if err := svc.HandleEvent(evt); err == nil {
		t.Error("expected error for bad path")
	}
}

func TestHandleEvent_PreservesFieldsFromExisting(t *testing.T) {
	repo := newMockRepo()
	existing := &session.SessionState{
		SessionID:  "sid4",
		State:      session.StateWorking,
		Model:      "claude-3",
		CWD:        "/home/user/project",
		GitBranch:  "feature/foo",
		FirstSeen:  100,
		UpdatedAt:  100,
		EventCount: 3,
	}
	repo.states["sid4"] = existing
	svc := newSvc(repo)

	evt := &event.HookEvent{HookEventName: "PostToolUse", SessionID: "sid4"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("sid4")
	if state.Model != "claude-3" {
		t.Errorf("model not preserved: got %q", state.Model)
	}
	if state.FirstSeen != 100 {
		t.Errorf("first_seen not preserved: got %d", state.FirstSeen)
	}
	if state.EventCount != 4 {
		t.Errorf("event count: got %d, want 4", state.EventCount)
	}
}

func TestRunSpeculativeWait_TransitionsToWaiting(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)
	svc.SpeculativeWaitDelay = 0

	now := time.Now().Unix()
	repo.states["sw1"] = &session.SessionState{
		SessionID: "sw1",
		State:     session.StateWorking,
		LastEvent: "PreToolUse",
		FirstSeen: now,
		UpdatedAt: now,
	}

	svc.RunSpeculativeWait("sw1")

	state, _ := repo.Load("sw1")
	if state.State != session.StateWaiting {
		t.Errorf("got %q, want waiting", state.State)
	}
	if state.WaitingStartTime == nil {
		t.Error("WaitingStartTime should be set")
	}
}

func TestRunSpeculativeWait_NoOpWhenPostToolUseArrived(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)
	svc.SpeculativeWaitDelay = 0

	now := time.Now().Unix()
	repo.states["sw2"] = &session.SessionState{
		SessionID: "sw2",
		State:     session.StateWorking,
		LastEvent: "PostToolUse",
		FirstSeen: now,
		UpdatedAt: now,
	}

	svc.RunSpeculativeWait("sw2")

	state, _ := repo.Load("sw2")
	if state.State != session.StateWorking {
		t.Errorf("got %q, want working (unchanged)", state.State)
	}
}

func TestRunSpeculativeWait_NoOpWhenSessionGone(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)
	svc.SpeculativeWaitDelay = 0
	// No state in repo — should not panic
	svc.RunSpeculativeWait("nonexistent")
}

func TestRunSpeculativeWait_NoOpWhenAlreadyWaiting(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)
	svc.SpeculativeWaitDelay = 0

	now := time.Now().Unix()
	repo.states["sw3"] = &session.SessionState{
		SessionID: "sw3",
		State:     session.StateWaiting,
		LastEvent: "Notification",
		FirstSeen: now,
		UpdatedAt: now,
	}

	svc.RunSpeculativeWait("sw3")

	state, _ := repo.Load("sw3")
	if state.LastEvent != "Notification" {
		t.Errorf("LastEvent mutated: got %q", state.LastEvent)
	}
}

func TestCleanupOrphanedSessions_RemovesStaleSession(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	staleTime := time.Now().Unix() - 7200 // 2 hours ago
	repo.states["stale1"] = &session.SessionState{
		SessionID: "stale1",
		State:     session.StateWorking,
		PID:       0,          // no PID — TTL-based cleanup
		UpdatedAt: staleTime,
	}
	repo.states["fresh1"] = &session.SessionState{
		SessionID: "fresh1",
		State:     session.StateWorking,
		PID:       0,
		UpdatedAt: time.Now().Unix(),
	}

	svc.CleanupOrphanedSessions()

	if _, err := repo.Load("stale1"); err == nil {
		t.Error("stale session should have been deleted")
	}
	if _, err := repo.Load("fresh1"); err != nil {
		t.Error("fresh session should NOT have been deleted")
	}
}

func TestHandleEvent_PreCompact_SetsCompacting(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{HookEventName: "PreCompact", SessionID: "pc1", Matcher: "auto"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("pc1")
	if state.CompactionState != session.CompactionStateCompacting {
		t.Errorf("compaction: got %q, want compacting", state.CompactionState)
	}
}

func TestHandleEvent_SessionEnd_CancelledByUser(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{
		HookEventName: "SessionEnd",
		SessionID:     "cu1",
		Reason:        "prompt_input_exit",
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, err := repo.Load("cu1")
	if err != nil {
		t.Fatal("expected state to be saved (not deleted)")
	}
	if state.State != session.StateCancelledByUser {
		t.Errorf("got %q, want cancelled_by_user", state.State)
	}
}

func TestHandleEvent_Stop_SetsReady(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{HookEventName: "Stop", SessionID: "stop1"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("stop1")
	if state.State != session.StateReady {
		t.Errorf("got %q, want ready", state.State)
	}
}

func TestHandleEvent_PopulatesFromDataMap(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	// Use PostToolUse (not SessionStart) so model from data map is not overridden.
	evt := &event.HookEvent{
		HookEventName: "PostToolUse",
		SessionID:     "data1",
		Data: map[string]interface{}{
			"model": "claude-3-opus",
			"cwd":   "/home/user",
		},
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("data1")
	if state.Model != "claude-3-opus" {
		t.Errorf("model from data map: got %q", state.Model)
	}
	if state.CWD != "/home/user" {
		t.Errorf("cwd from data map: got %q", state.CWD)
	}
}

func TestHandleEvent_DirectFieldsOverrideDataMap(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	// Use PostToolUse so model field is not reset to "New Session".
	evt := &event.HookEvent{
		HookEventName: "PostToolUse",
		SessionID:     "data2",
		Model:         "claude-direct",
		Data: map[string]interface{}{
			"model": "claude-from-data",
		},
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("data2")
	if state.Model != "claude-direct" {
		t.Errorf("direct field should override data map, got %q", state.Model)
	}
}

func TestHandleEvent_EnteringWaiting_SetsTranscriptMonitoring(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{
		HookEventName:  "Notification",
		SessionID:      "wait1",
		TranscriptPath: "/nonexistent/transcript.json",
	}
	// Even if stat fails, the session should be saved as waiting.
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("wait1")
	if state.State != session.StateWaiting {
		t.Errorf("got %q, want waiting", state.State)
	}
}

func TestHandleEvent_PopulatesTranscriptPathFromDataMap(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{
		HookEventName: "PostToolUse",
		SessionID:     "tp1",
		Data: map[string]interface{}{
			"transcript_path": "/home/user/transcript.json",
		},
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("tp1")
	if state.TranscriptPath != "/home/user/transcript.json" {
		t.Errorf("transcript_path: got %q", state.TranscriptPath)
	}
}

func TestHandleEvent_InheritsTranscriptFromExisting(t *testing.T) {
	repo := newMockRepo()
	repo.states["inh1"] = &session.SessionState{
		SessionID:      "inh1",
		State:          session.StateWorking,
		TranscriptPath: "/home/user/old-transcript.json",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}
	svc := newSvc(repo)

	// New event does not carry transcript path — should be preserved from existing.
	evt := &event.HookEvent{HookEventName: "PostToolUse", SessionID: "inh1"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("inh1")
	if state.TranscriptPath != "/home/user/old-transcript.json" {
		t.Errorf("transcript path not inherited: got %q", state.TranscriptPath)
	}
}

func TestHandleEvent_InheritsGitBranchFromExisting(t *testing.T) {
	repo := newMockRepo()
	repo.states["inh2"] = &session.SessionState{
		SessionID: "inh2",
		State:     session.StateWorking,
		GitBranch: "feature/inherited",
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	svc := newSvc(repo)

	evt := &event.HookEvent{HookEventName: "PostToolUse", SessionID: "inh2"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("inh2")
	if state.GitBranch != "feature/inherited" {
		t.Errorf("git branch not inherited: got %q", state.GitBranch)
	}
}

func TestHandleEvent_LeavingWaiting_ClearsMonitoring(t *testing.T) {
	repo := newMockRepo()
	waitingStart := time.Now().Unix()
	repo.states["wait2"] = &session.SessionState{
		SessionID:          "wait2",
		State:              session.StateWaiting,
		LastTranscriptSize: 1000,
		WaitingStartTime:   &waitingStart,
		FirstSeen:          time.Now().Unix(),
		UpdatedAt:          time.Now().Unix(),
	}
	svc := newSvc(repo)

	evt := &event.HookEvent{HookEventName: "UserPromptSubmit", SessionID: "wait2"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("wait2")
	if state.LastTranscriptSize != 0 {
		t.Errorf("LastTranscriptSize should be cleared, got %d", state.LastTranscriptSize)
	}
	if state.WaitingStartTime != nil {
		t.Error("WaitingStartTime should be cleared")
	}
}

func TestHandleEvent_SubagentStop_SetsParentSessionIDFromDirectField(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{
		HookEventName:   "SubagentStop",
		SessionID:       "sub1",
		ParentSessionID: "parent1",
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("sub1")
	if state.ParentSessionID != "parent1" {
		t.Errorf("parent_session_id: got %q, want parent1", state.ParentSessionID)
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
}

func TestHandleEvent_SubagentStop_SetsParentSessionIDFromDataMap(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{
		HookEventName: "SubagentStop",
		SessionID:     "sub2",
		Data: map[string]interface{}{
			"parent_session_id": "parent2",
		},
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("sub2")
	if state.ParentSessionID != "parent2" {
		t.Errorf("parent_session_id from data map: got %q, want parent2", state.ParentSessionID)
	}
}

func TestHandleEvent_SubagentStop_DirectFieldOverridesDataMap(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	evt := &event.HookEvent{
		HookEventName:   "SubagentStop",
		SessionID:       "sub3",
		ParentSessionID: "parent-direct",
		Data: map[string]interface{}{
			"parent_session_id": "parent-from-data",
		},
	}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("sub3")
	if state.ParentSessionID != "parent-direct" {
		t.Errorf("direct field should override data map: got %q, want parent-direct", state.ParentSessionID)
	}
}

func TestHandleEvent_ParentSessionID_InheritedFromExisting(t *testing.T) {
	repo := newMockRepo()
	repo.states["sub4"] = &session.SessionState{
		SessionID:       "sub4",
		State:           session.StateReady,
		ParentSessionID: "parent4",
		FirstSeen:       100,
		UpdatedAt:       100,
	}
	svc := newSvc(repo)

	// Subsequent event without parent_session_id should preserve it
	evt := &event.HookEvent{HookEventName: "PostToolUse", SessionID: "sub4"}
	if err := svc.HandleEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, _ := repo.Load("sub4")
	if state.ParentSessionID != "parent4" {
		t.Errorf("parent_session_id not inherited: got %q, want parent4", state.ParentSessionID)
	}
}

func TestCleanupOrphanedSessions_SkipsCancelledByUser(t *testing.T) {
	repo := newMockRepo()
	svc := newSvc(repo)

	staleTime := time.Now().Unix() - 7200
	repo.states["cancelled1"] = &session.SessionState{
		SessionID: "cancelled1",
		State:     session.StateCancelledByUser,
		PID:       0,
		UpdatedAt: staleTime,
	}

	svc.CleanupOrphanedSessions()

	if _, err := repo.Load("cancelled1"); err != nil {
		t.Error("cancelled_by_user session should NOT be deleted by cleanup")
	}
}
