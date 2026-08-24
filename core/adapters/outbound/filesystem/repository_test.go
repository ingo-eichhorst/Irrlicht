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

// TestRepository_Load_RejectsPathTraversal covers Load/Delete being
// reachable from the daemon's loopback control API (POST
// /api/v1/sessions/{id}/input, .../interrupt) with sessionID taken straight
// from the URL path segment and only an empty-string check applied before
// it reaches the repository. A "../"-shaped id must not escape instancesDir.
func TestRepository_Load_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWithDir(filepath.Join(dir, "instances"))

	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"session_id":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, evil := range []string{"../secret", "..", "."} {
		if _, err := repo.Load(evil); err == nil {
			t.Errorf("Load(%q) = nil error; want the traversal rejected", evil)
		}
		// Delete returns nil for a missing file by design (see its doc
		// comment) — the security property to check here is that it never
		// removes the escaped-to file, not that it returns an error.
		_ = repo.Delete(evil)
	}

	if data, err := os.ReadFile(secret); err != nil || string(data) != `{"session_id":"secret"}` {
		t.Errorf("secret file was modified or removed: data=%q err=%v", data, err)
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

// TestRepository_ListAll_KeepsUnknownState covers #1797. ListAll used to
// os.Remove any session file whose state fell outside a hardcoded three-entry
// allowlist, so a daemon that predates a newly-introduced state DESTROYS every
// session file carrying it — irrecoverably, on the first sweep after a
// downgrade or a mixed-version install. Forward compatibility is the point:
// an unrecognised state is a value this build does not understand yet, never a
// licence to delete the user's data.
//
// The surviving-file assertion is the defect test (seen red on the
// pre-#1797 tree, where the file is gone by the time ListAll returns). The
// "not in the returned slice" assertion is a LOCK, not red-first evidence: a
// three-state build already skipped the state and must keep skipping it, since
// every downstream consumer (grouping, counts, the state machine) is written
// against exactly three values.
func TestRepository_ListAll_KeepsUnknownState(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWithDir(dir)

	known := &session.SessionState{SessionID: "known", State: session.StateWorking, UpdatedAt: time.Now().Unix()}
	if err := repo.Save(known); err != nil {
		t.Fatalf("Save: %v", err)
	}

	unknownPath := filepath.Join(dir, "unknown.json")
	payload := []byte(`{"version":1,"session_id":"unknown","state":"zzz-unknown","first_seen":1,"updated_at":2}`)
	if err := os.WriteFile(unknownPath, payload, 0o600); err != nil {
		t.Fatalf("write unknown-state file: %v", err)
	}

	states, err := repo.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}

	// The defect: the file must survive the sweep, byte for byte.
	got, err := os.ReadFile(unknownPath)
	if err != nil {
		t.Fatalf("unknown-state file was deleted by ListAll: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("unknown-state file was rewritten: got %q, want %q", got, payload)
	}

	// The lock: it must not reach a three-state consumer.
	for _, s := range states {
		if s.SessionID == "unknown" {
			t.Errorf("ListAll returned the unknown-state session %q; want it skipped", s.State)
		}
	}
	if len(states) != 1 {
		t.Errorf("ListAll: got %d states, want 1 (the known session)", len(states))
	}
}

// TestRepository_ListAll_KeepsUnparseableFile pins the other half of #1797's
// data-safety property: a file this build cannot decode at all is skipped, not
// deleted. ListAll's `continue` on a json.Unmarshal error already behaved this
// way, so this is a LOCK, not a defect test — it exists so a future "tidy up
// junk on load" change has to break a named test rather than a code comment.
func TestRepository_ListAll_KeepsUnparseableFile(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWithDir(dir)

	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("not{json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := repo.ListAll(); err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if _, err := os.Stat(badPath); err != nil {
		t.Errorf("unparseable file was deleted by ListAll: %v", err)
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
