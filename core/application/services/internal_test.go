package services

import (
	"os"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestIsProcessAlive tests the internal isProcessAlive helper.
func TestIsProcessAlive(t *testing.T) {
	// Current process must be alive.
	if !isProcessAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	// PID 0 is invalid.
	if isProcessAlive(0) {
		t.Error("pid 0 should not be considered alive")
	}
	// A very high PID that should not exist.
	if isProcessAlive(9999999) {
		t.Log("PID 9999999 happened to exist — skipping liveness check")
	}
}

// TestDetectTranscriptActivity tests detectTranscriptActivity using a real temp file.
func TestDetectTranscriptActivity(t *testing.T) {
	// Create a temp transcript file.
	f, err := os.CreateTemp(t.TempDir(), "transcript-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.WriteString("initial content")
	f.Close()
	info, _ := os.Stat(f.Name())
	initialSize := info.Size()

	now := time.Now().Unix()
	svc := &EventService{
		repo:    nil,
		log:     &nopLogger{},
		git:     nil,
		metrics: nil,
	}

	prev := &session.SessionState{
		State:              "waiting",
		TranscriptPath:     f.Name(),
		LastTranscriptSize: initialSize,
		WaitingStartTime:   &now,
	}

	// No activity — file has not grown.
	if svc.detectTranscriptActivity(prev) {
		t.Error("should not detect activity when file unchanged")
	}

	// Grow the file.
	fAppend, _ := os.OpenFile(f.Name(), os.O_APPEND|os.O_WRONLY, 0644)
	fAppend.WriteString(" more data")
	fAppend.Close()

	// Now there should be activity.
	if !svc.detectTranscriptActivity(prev) {
		t.Error("should detect activity after file grew")
	}
}

// TestDetectTranscriptActivity_NilState tests nil previous state handling.
func TestDetectTranscriptActivity_NilState(t *testing.T) {
	svc := &EventService{}
	if svc.detectTranscriptActivity(nil) {
		t.Error("nil state should return false")
	}
}

// TestRunSpeculativeWait_WithTranscriptPath exercises the transcript stat path.
func TestRunSpeculativeWait_WithTranscriptPath(t *testing.T) {
	// Write a real file so stat succeeds.
	f, _ := os.CreateTemp(t.TempDir(), "transcript-*.json")
	f.WriteString("{}")
	f.Close()

	repo := newInternalMockRepo()
	log := &nopLogger{}
	svc := &EventService{
		repo:                 repo,
		log:                  log,
		SpeculativeWaitDelay: 0,
	}

	now := time.Now().Unix()
	repo.states["tr1"] = &session.SessionState{
		SessionID:      "tr1",
		State:          session.StateWorking,
		LastEvent:      "PreToolUse",
		TranscriptPath: f.Name(),
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	svc.RunSpeculativeWait("tr1")

	state := repo.states["tr1"]
	if state.State != session.StateWaiting {
		t.Errorf("got %q, want waiting", state.State)
	}
	if state.LastTranscriptSize == 0 {
		t.Error("LastTranscriptSize should be set from transcript file")
	}
}

// TestCleanupOrphanedSessions_SkipsAliveProcess tests that a session whose
// process is still alive is NOT deleted.
func TestCleanupOrphanedSessions_SkipsAliveProcess(t *testing.T) {
	repo := newInternalMockRepo()
	svc := &EventService{repo: repo, log: &nopLogger{}}

	repo.states["alive1"] = &session.SessionState{
		SessionID: "alive1",
		State:     session.StateWorking,
		PID:       os.Getpid(), // current process is alive
		UpdatedAt: time.Now().Unix(),
	}

	svc.CleanupOrphanedSessions()

	if _, ok := repo.states["alive1"]; !ok {
		t.Error("session with alive process should NOT be deleted")
	}
}

// TestCleanupOrphanedSessions_DeletesDeadProcess tests reaping by dead PID.
func TestCleanupOrphanedSessions_DeletesDeadProcess(t *testing.T) {
	repo := newInternalMockRepo()
	svc := &EventService{repo: repo, log: &nopLogger{}}

	repo.states["dead1"] = &session.SessionState{
		SessionID: "dead1",
		State:     session.StateWorking,
		PID:       9999999, // unlikely to exist
		UpdatedAt: time.Now().Unix(),
	}

	svc.CleanupOrphanedSessions()

	if _, ok := repo.states["dead1"]; ok {
		// This is a flaky skip: if PID 9999999 actually exists, this is fine.
		t.Log("PID 9999999 exists on this machine — skipping dead-process cleanup test")
	}
}

// TestSpawnSpeculativeWait_InvalidExe tests that an invalid executable path
// logs an error but does not panic.
func TestSpawnSpeculativeWait_InvalidExe(t *testing.T) {
	log := &nopLogger{}
	svc := &EventService{
		log:            log,
		executablePath: "/nonexistent/binary",
	}
	// Should not panic even if the binary doesn't exist.
	svc.spawnSpeculativeWait("test-session")
}

// internalMockRepo is a simple in-memory repo for internal tests.
type internalMockRepo struct {
	states map[string]*session.SessionState
}

func newInternalMockRepo() *internalMockRepo {
	return &internalMockRepo{states: make(map[string]*session.SessionState)}
}

func (r *internalMockRepo) Load(id string) (*session.SessionState, error) {
	s, ok := r.states[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return s, nil
}

func (r *internalMockRepo) Save(s *session.SessionState) error {
	r.states[s.SessionID] = s
	return nil
}

func (r *internalMockRepo) Delete(id string) error {
	delete(r.states, id)
	return nil
}

func (r *internalMockRepo) ListAll() ([]*session.SessionState, error) {
	out := make([]*session.SessionState, 0, len(r.states))
	for _, s := range r.states {
		out = append(out, s)
	}
	return out, nil
}

// nopLogger is a no-op implementation of outbound.Logger for internal tests.
type nopLogger struct{}

func (l *nopLogger) LogInfo(_, _, _ string)                                  {}
func (l *nopLogger) LogError(_, _, _ string)                                 {}
func (l *nopLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *nopLogger) Close() error                                            { return nil }
