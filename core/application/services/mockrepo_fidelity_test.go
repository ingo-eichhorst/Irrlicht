package services_test

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestMockRepo_LoadReturnsIndependentCopy guards the #973 fix: Load must
// never hand back the same pointer (or the same nested Metrics pointer)
// twice, or a future edit that "simplifies" mockRepo back to returning the
// stored pointer would silently reintroduce the aliasing race that made
// core/application/services tests flake under -race for weeks (#578,
// #606, #942/#944, #956/#964) before #973 closed it at the source.
func TestMockRepo_LoadReturnsIndependentCopy(t *testing.T) {
	repo := newMockRepo()
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
		t.Fatal("Load returned the same *SessionState pointer twice — mockRepo is aliasing again")
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

// TestMockRepo_SaveStoresIndependentCopy guards the other half of #973:
// Save must copy the caller's struct in, so a caller that keeps mutating its
// own pointer after Save() (as processActivityLocked's helpers do across a
// single locked pass) can't reach back into what's stored.
func TestMockRepo_SaveStoresIndependentCopy(t *testing.T) {
	repo := newMockRepo()
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

// TestMockRepo_ListAllReturnsIndependentCopies guards ListAll the same way.
func TestMockRepo_ListAllReturnsIndependentCopies(t *testing.T) {
	repo := newMockRepo()
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
