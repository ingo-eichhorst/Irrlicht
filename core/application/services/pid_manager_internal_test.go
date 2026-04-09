package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// --- tiny in-package test doubles --------------------------------------------

type stubRepo struct {
	states map[string]*session.SessionState
}

func newStubRepo() *stubRepo { return &stubRepo{states: make(map[string]*session.SessionState)} }

func (r *stubRepo) Load(sessionID string) (*session.SessionState, error) {
	s, ok := r.states[sessionID]
	if !ok {
		return nil, nil
	}
	return s, nil
}
func (r *stubRepo) Save(s *session.SessionState) error {
	r.states[s.SessionID] = s
	return nil
}
func (r *stubRepo) Delete(sessionID string) error {
	delete(r.states, sessionID)
	return nil
}
func (r *stubRepo) ListAll() ([]*session.SessionState, error) {
	out := make([]*session.SessionState, 0, len(r.states))
	for _, s := range r.states {
		out = append(out, s)
	}
	return out, nil
}

type stubLogger struct{}

func (stubLogger) LogInfo(_, _, _ string)                          {}
func (stubLogger) LogError(_, _, _ string)                         {}
func (stubLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (stubLogger) Close() error                                    { return nil }

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

// TestCheckPIDLiveness_FreshTranscript_NotDeleted verifies the Layer 2 fix for
// issue #109: a ready session with PID=0 and a freshly-written transcript must
// NOT be fast-deleted after 30s. PID discovery may still be catching up (e.g.
// Claude hasn't written ~/.claude/sessions/<pid>.json yet).
func TestCheckPIDLiveness_FreshTranscript_NotDeleted(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "fresh.jsonl")
	writeTranscript(t, transcript, time.Now()) // fresh mtime

	repo := newStubRepo()
	// Updated 60s ago → past the 30s threshold, but transcript is fresh.
	repo.states["fresh"] = &session.SessionState{
		SessionID:      "fresh",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	pm := NewPIDManager(
		nil, // no ProcessWatcher
		repo,
		stubLogger{},
		nil,                   // no broadcaster
		10*time.Minute,        // readyTTL (large, so normal idle cleanup doesn't fire)
		nil,                   // no pid discovers
		func(string) {},       // noop onSessionDeleted
	)

	pm.CheckPIDLiveness()

	if _, err := repo.Load("fresh"); err != nil {
		t.Fatalf("load: %v", err)
	}
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
	writeTranscript(t, transcript, time.Now().Add(-10*time.Minute)) // stale mtime

	repo := newStubRepo()
	repo.states["stale"] = &session.SessionState{
		SessionID:      "stale",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	pm := NewPIDManager(
		nil,
		repo,
		stubLogger{},
		nil,
		10*time.Minute,
		nil,
		func(string) {},
	)

	pm.CheckPIDLiveness()

	if repo.states["stale"] != nil {
		t.Fatal("session should be deleted (stale transcript + ready + pid=0 + >30s)")
	}
}

// Ensure stubRepo satisfies the outbound.SessionRepository interface.
var _ outbound.SessionRepository = (*stubRepo)(nil)
var _ outbound.Logger = stubLogger{}
