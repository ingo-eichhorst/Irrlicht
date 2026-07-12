package e2e_test

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestMemRepo_LoadReturnsIndependentCopy guards the #975 fix: Load must
// never hand back the same pointer (or the same nested Metrics pointer)
// twice. concurrent_test.go and crash_test.go run the real
// SessionDetector.Run() in a goroutine against this repo, so an aliased
// Load() result would race the detector's own in-place mutations under
// PIDManager.WithSessionStateLock — the same bug core/application/services'
// mockRepo had before #973. A future edit that "simplifies" memRepo back to
// returning the stored pointer would silently reintroduce it.
func TestMemRepo_LoadReturnsIndependentCopy(t *testing.T) {
	repo := newMemRepo()
	repo.states["x"] = &session.SessionState{
		SessionID: "x",
		State:     session.StateWorking,
		Metrics:   &session.SessionMetrics{TotalTokens: 10},
	}

	a, err := repo.Load("x")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := repo.Load("x")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if a == b {
		t.Fatal("Load returned the same *SessionState pointer twice — memRepo is aliasing again")
	}
	if a.Metrics == b.Metrics {
		t.Fatal("Load returned the same *SessionMetrics pointer twice — nested field aliasing")
	}

	a.State = session.StateReady
	a.Metrics.TotalTokens = 999

	if b.State != session.StateWorking {
		t.Errorf("mutating one Load() result changed another's State: got %q, want %q", b.State, session.StateWorking)
	}
	if b.Metrics.TotalTokens != 10 {
		t.Errorf("mutating one Load() result's Metrics changed another's: got %d, want 10", b.Metrics.TotalTokens)
	}
}

// TestMemRepo_SaveStoresIndependentCopy guards the other half of #975: Save
// must copy the caller's struct in, so a caller that keeps mutating its own
// pointer after Save() can't reach back into what's stored.
func TestMemRepo_SaveStoresIndependentCopy(t *testing.T) {
	repo := newMemRepo()
	s := &session.SessionState{SessionID: "y", State: session.StateWorking}

	if err := repo.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s.State = session.StateReady // mutate the caller's copy after Save

	got, err := repo.Load("y")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != session.StateWorking {
		t.Errorf("Save aliased the caller's pointer: stored state changed to %q after the caller mutated its own copy", got.State)
	}
}

// TestMemRepo_ListAllReturnsIndependentCopies guards ListAll the same way.
func TestMemRepo_ListAllReturnsIndependentCopies(t *testing.T) {
	repo := newMemRepo()
	repo.states["z"] = &session.SessionState{SessionID: "z", State: session.StateWorking}

	all, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 session, got %d", len(all))
	}
	all[0].State = session.StateReady

	got, err := repo.Load("z")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != session.StateWorking {
		t.Errorf("ListAll aliased stored state: mutating the returned slice's element changed Load's result to %q", got.State)
	}
}
