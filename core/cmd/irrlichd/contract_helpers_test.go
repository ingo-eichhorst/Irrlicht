package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Contract-test shared helpers. Files in this package whose names contain
// "_contract_test.go" use these to lock down wire and persistence shapes.
//
// Refresh every golden via:
//
//	UPDATE_CONTRACT_GOLDENS=1 go test ./core/...

const updateContractGoldensEnv = "UPDATE_CONTRACT_GOLDENS"

// compareContractGolden is the byte-identity comparator used by every
// contract test in this package.
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
