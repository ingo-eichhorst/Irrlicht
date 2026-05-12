package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/internal/contracttesting"
)

// TestContract_SessionStateOnDisk locks the on-disk JSON shape produced
// by SessionRepository.Save() for a SessionState with every persisted
// field populated. The macOS Swift app and the dashboard read these
// files; a silent change to the JSON shape is a wire-protocol regression.
//
// Refresh with UPDATE_CONTRACT_GOLDENS=1.
//
// The Subagents field uses an unexported type from the session package
// and is intentionally left nil — covering it would require an exported
// constructor in domain/session.
func TestContract_SessionStateOnDisk(t *testing.T) {
	state := contracttesting.BuildFullSessionState()
	repo := filesystem.NewWithDir(t.TempDir())
	if err := repo.Save(state); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo.InstancesDir(), state.SessionID+".json"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	contracttesting.CompareGolden(t, got, filepath.Join("testdata", "session_state.golden.json"))
}
