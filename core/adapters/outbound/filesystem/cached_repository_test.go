package filesystem_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

func TestCachedRepo_ListAll_CachesResults(t *testing.T) {
	dir := t.TempDir()
	inner := filesystem.NewWithDir(dir)
	cached := filesystem.NewCachedSessionRepository(inner, 100*time.Millisecond)

	state := &session.SessionState{
		SessionID: "cache-hit",
		State:     session.StateWorking,
		UpdatedAt: time.Now().Unix(),
	}
	if err := cached.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	first, err := cached.ListAll()
	if err != nil {
		t.Fatalf("first ListAll: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first ListAll: got %d, want 1", len(first))
	}

	// Second call within TTL should return cached data.
	second, err := cached.ListAll()
	if err != nil {
		t.Fatalf("second ListAll: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second ListAll: got %d, want 1", len(second))
	}
	if second[0].SessionID != "cache-hit" {
		t.Errorf("session ID: got %q, want %q", second[0].SessionID, "cache-hit")
	}
}

func TestCachedRepo_ListAll_DeepCopies(t *testing.T) {
	dir := t.TempDir()
	inner := filesystem.NewWithDir(dir)
	cached := filesystem.NewCachedSessionRepository(inner, 5*time.Second)

	state := &session.SessionState{
		SessionID: "deep-copy",
		State:     session.StateWorking,
		UpdatedAt: time.Now().Unix(),
	}
	if err := cached.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	first, err := cached.ListAll()
	if err != nil {
		t.Fatalf("first ListAll: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 state, got %d", len(first))
	}

	// Mutate the returned state.
	first[0].State = session.StateReady

	// Second call should return the original unmutated value.
	second, err := cached.ListAll()
	if err != nil {
		t.Fatalf("second ListAll: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("expected 1 state, got %d", len(second))
	}
	if second[0].State != session.StateWorking {
		t.Errorf("deep copy broken: got %q, want %q", second[0].State, session.StateWorking)
	}
}

func TestCachedRepo_Save_InvalidatesCache(t *testing.T) {
	dir := t.TempDir()
	inner := filesystem.NewWithDir(dir)
	cached := filesystem.NewCachedSessionRepository(inner, 5*time.Second)

	s1 := &session.SessionState{SessionID: "s1", State: session.StateWorking, UpdatedAt: time.Now().Unix()}
	if err := cached.Save(s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}

	// Populate cache.
	first, err := cached.ListAll()
	if err != nil {
		t.Fatalf("first ListAll: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first ListAll: got %d, want 1", len(first))
	}

	// Save a new session — should invalidate cache.
	s2 := &session.SessionState{SessionID: "s2", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	if err := cached.Save(s2); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	// ListAll should now include both sessions.
	second, err := cached.ListAll()
	if err != nil {
		t.Fatalf("second ListAll: %v", err)
	}
	if len(second) != 2 {
		t.Errorf("second ListAll: got %d, want 2", len(second))
	}
}

func TestCachedRepo_Delete_InvalidatesCache(t *testing.T) {
	dir := t.TempDir()
	inner := filesystem.NewWithDir(dir)
	cached := filesystem.NewCachedSessionRepository(inner, 5*time.Second)

	s1 := &session.SessionState{SessionID: "keep", State: session.StateWorking, UpdatedAt: time.Now().Unix()}
	s2 := &session.SessionState{SessionID: "remove", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	if err := cached.Save(s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}
	if err := cached.Save(s2); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	// Populate cache.
	first, err := cached.ListAll()
	if err != nil {
		t.Fatalf("first ListAll: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first ListAll: got %d, want 2", len(first))
	}

	// Delete one session — should invalidate cache.
	if err := cached.Delete("remove"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// ListAll should return only the remaining session.
	second, err := cached.ListAll()
	if err != nil {
		t.Fatalf("second ListAll: %v", err)
	}
	if len(second) != 1 {
		t.Errorf("second ListAll: got %d, want 1", len(second))
	}
	if len(second) == 1 && second[0].SessionID != "keep" {
		t.Errorf("remaining session: got %q, want %q", second[0].SessionID, "keep")
	}
}

func TestCachedRepo_TTL_Expires(t *testing.T) {
	dir := t.TempDir()
	inner := filesystem.NewWithDir(dir)
	cached := filesystem.NewCachedSessionRepository(inner, 50*time.Millisecond)

	s1 := &session.SessionState{SessionID: "original", State: session.StateWorking, UpdatedAt: time.Now().Unix()}
	if err := cached.Save(s1); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Populate cache.
	first, err := cached.ListAll()
	if err != nil {
		t.Fatalf("first ListAll: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first ListAll: got %d, want 1", len(first))
	}

	// Wait for TTL to expire.
	time.Sleep(80 * time.Millisecond)

	// Write a new session file directly to the filesystem, bypassing Save
	// so the cache is not explicitly invalidated.
	sneaky := &session.SessionState{SessionID: "sneaky", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	data, err := json.Marshal(sneaky)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(dir+"/sneaky.json", data, 0644); err != nil {
		t.Fatalf("write sneaky file: %v", err)
	}

	// ListAll should pick up the new file because the cache has expired.
	second, err := cached.ListAll()
	if err != nil {
		t.Fatalf("second ListAll: %v", err)
	}
	if len(second) != 2 {
		t.Errorf("second ListAll after TTL expiry: got %d, want 2", len(second))
	}
}
