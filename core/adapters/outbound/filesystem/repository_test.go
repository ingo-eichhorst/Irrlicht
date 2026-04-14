package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

func TestRepository_SaveAndLoad(t *testing.T) {
	repo := filesystem.NewWithDir(t.TempDir())

	state := &session.SessionState{
		Version:   1,
		SessionID: "test-session",
		State:     session.StateWorking,
		Model:     "claude-3",
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	if err := repo.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.Load("test-session")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != state.State {
		t.Errorf("state: got %q, want %q", got.State, state.State)
	}
	if got.Model != state.Model {
		t.Errorf("model: got %q, want %q", got.Model, state.Model)
	}
}

func TestRepository_Load_NotFound(t *testing.T) {
	repo := filesystem.NewWithDir(t.TempDir())
	_, err := repo.Load("nonexistent")
	if err == nil {
		t.Error("expected error for missing session")
	}
}

func TestRepository_Delete(t *testing.T) {
	repo := filesystem.NewWithDir(t.TempDir())

	state := &session.SessionState{SessionID: "del-me", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	if err := repo.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := repo.Delete("del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Load("del-me"); err == nil {
		t.Error("session should be gone after delete")
	}
}

func TestRepository_Delete_NonExistent_NoError(t *testing.T) {
	repo := filesystem.NewWithDir(t.TempDir())
	if err := repo.Delete("ghost"); err != nil {
		t.Errorf("deleting non-existent session should not error: %v", err)
	}
}

func TestRepository_ListAll(t *testing.T) {
	repo := filesystem.NewWithDir(t.TempDir())

	for _, sid := range []string{"s1", "s2", "s3"} {
		state := &session.SessionState{SessionID: sid, State: session.StateReady, UpdatedAt: time.Now().Unix()}
		if err := repo.Save(state); err != nil {
			t.Fatalf("Save %s: %v", sid, err)
		}
	}

	states, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(states) != 3 {
		t.Errorf("ListAll: got %d states, want 3", len(states))
	}
}

func TestRepository_InstancesDir(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWithDir(dir)
	if repo.InstancesDir() != dir {
		t.Errorf("InstancesDir: got %q, want %q", repo.InstancesDir(), dir)
	}
}

func TestRepository_ListAll_EmptyDir(t *testing.T) {
	repo := filesystem.NewWithDir(t.TempDir())
	states, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll on empty dir: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("expected 0 states, got %d", len(states))
	}
}

func TestRepository_ListAll_NonExistentDir(t *testing.T) {
	// A directory that has never existed should return nil, nil (not an error).
	repo := filesystem.NewWithDir(t.TempDir() + "/does-not-exist/subdir")
	states, err := repo.ListAll()
	if err != nil {
		t.Fatalf("expected nil error for non-existent dir, got: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("expected 0 states, got %d", len(states))
	}
}

func TestRepository_Save_ErrorWhenDirIsFile(t *testing.T) {
	// Create a FILE where the instances dir should be — Save must fail gracefully.
	dir := t.TempDir()
	blockPath := dir + "/blocked"
	if err := os.WriteFile(blockPath, []byte("x"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Point the repo at a sub-path under the file (MkdirAll will fail).
	repo := filesystem.NewWithDir(blockPath + "/instances")
	state := &session.SessionState{SessionID: "s", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	if err := repo.Save(state); err == nil {
		t.Error("expected error when instances dir cannot be created")
	}
}

func TestNew_UsesRealHomeDir(t *testing.T) {
	repo, err := filesystem.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if repo.InstancesDir() == "" {
		t.Error("InstancesDir should not be empty")
	}
}

func TestRepository_ListAll_SkipsNonJSON(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWithDir(dir)

	// Write a valid session.
	state := &session.SessionState{SessionID: "valid", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	if err := repo.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Write a non-JSON file that should be skipped.
	os.WriteFile(dir+"/ignore.txt", []byte("not json"), 0644)
	// Write an invalid JSON file that should be skipped.
	os.WriteFile(dir+"/bad.json", []byte("not{json"), 0644)

	states, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(states) != 1 {
		t.Errorf("expected 1 valid state, got %d", len(states))
	}
}

func TestRepository_FilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "instances")
	repo := filesystem.NewWithDir(dir)
	s := &session.SessionState{SessionID: "perm", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	if err := repo.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Errorf("dir perm: got %o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "perm.json"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Errorf("file perm: got %o, want 0600", got)
	}
}

func TestRepository_AtomicWrite(t *testing.T) {
	// Save twice to the same session — should overwrite without leaving tmp files.
	repo := filesystem.NewWithDir(t.TempDir())
	s := &session.SessionState{SessionID: "atomic", State: session.StateWorking, UpdatedAt: time.Now().Unix()}
	if err := repo.Save(s); err != nil {
		t.Fatalf("first save: %v", err)
	}
	s.State = session.StateWaiting
	if err := repo.Save(s); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := repo.Load("atomic")
	if err != nil {
		t.Fatalf("load after overwrite: %v", err)
	}
	if got.State != session.StateWaiting {
		t.Errorf("got %q, want waiting", got.State)
	}
}
