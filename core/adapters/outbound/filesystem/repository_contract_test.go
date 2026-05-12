package filesystem_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/internal/contracttesting"
)

const updateContractGoldensEnv = "UPDATE_CONTRACT_GOLDENS"

// TestContract_SessionStateOnDisk locks the on-disk JSON shape produced by
// SessionRepository.Save() for a SessionState with every persisted field
// populated. The macOS Swift app and the dashboard read these files; a silent
// change to the JSON shape is a wire-protocol regression.
//
// Refresh the golden with:
//
//	UPDATE_CONTRACT_GOLDENS=1 go test ./core/adapters/outbound/filesystem/...
//
// The Subagents field uses an unexported type from the session package and is
// intentionally left nil — covering it would require an exported constructor
// in domain/session, which is out of scope for this safety-net test.
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

	goldenPath := filepath.Join("testdata", "session_state.golden.json")
	compareContractGolden(t, got, goldenPath)
}

// compareContractGolden is the byte-identity comparator shared by every
// contract test in this package. Mirrors core/cmd/replay/fixtures_test.go's
// pattern with a different env var so contract goldens and replay goldens
// refresh independently.
func compareContractGolden(t *testing.T, got []byte, goldenPath string) {
	t.Helper()
	if os.Getenv(updateContractGoldensEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", goldenPath, err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with %s=1 to create)", goldenPath, err, updateContractGoldensEnv)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contract drift in %s; run %s=1 go test ./... to refresh\n--- want (%d bytes) ---\n%s\n--- got (%d bytes) ---\n%s",
			goldenPath, updateContractGoldensEnv, len(want), want, len(got), got)
	}
}
