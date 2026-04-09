package services_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

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

// newPIDManagerForTest builds a PIDManager wired to the shared mockRepo and
// mockLogger from testhelpers_test.go. readyTTL is set large so the normal
// idle sweep doesn't interfere with the fast-delete path under test.
func newPIDManagerForTest(repo *mockRepo) *services.PIDManager {
	return services.NewPIDManager(
		nil, // no ProcessWatcher
		repo,
		&mockLogger{},
		nil, // no broadcaster
		10*time.Minute,
		nil, // no pid discovers
		func(string) {},
	)
}

// TestCheckPIDLiveness_FreshTranscript_NotDeleted verifies the Layer 2 fix for
// issue #109: a ready session with PID=0 and a freshly-written transcript must
// NOT be fast-deleted after 30s. PID discovery may still be catching up (e.g.
// Claude hasn't written ~/.claude/sessions/<pid>.json yet).
func TestCheckPIDLiveness_FreshTranscript_NotDeleted(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "fresh.jsonl")
	writeTranscript(t, transcript, time.Now())

	repo := newMockRepo()
	// Updated 60s ago → past the 30s threshold, but transcript is fresh.
	repo.states["fresh"] = &session.SessionState{
		SessionID:      "fresh",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

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
	writeTranscript(t, transcript, time.Now().Add(-10*time.Minute))

	repo := newMockRepo()
	repo.states["stale"] = &session.SessionState{
		SessionID:      "stale",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["stale"] != nil {
		t.Fatal("session should be deleted (stale transcript + ready + pid=0 + >30s)")
	}
}
