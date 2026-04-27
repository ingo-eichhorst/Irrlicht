package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadCapabilitiesTranscriptExtension locks in the contract that
// loadCapabilities returns the adapter's transcript_extension when present
// and falls back to "jsonl" when absent. Three call sites (run-cell.sh,
// curate-lifecycle-fixture.sh, deriveCell) depend on this default.
func TestLoadCapabilitiesTranscriptExtension(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantExt string
	}{
		{
			name:    "explicit md",
			json:    `{"schema_version":1,"agent":"aider","transcript_extension":"md","features":{}}`,
			wantExt: "md",
		},
		{
			name:    "missing field defaults to jsonl",
			json:    `{"schema_version":1,"agent":"claudecode","features":{}}`,
			wantExt: "jsonl",
		},
		{
			name:    "empty string defaults to jsonl",
			json:    `{"schema_version":1,"agent":"x","transcript_extension":"","features":{}}`,
			wantExt: "jsonl",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			adapter := "testadapter"
			adir := filepath.Join(tmp, replayAgentDir, adapter)
			if err := os.MkdirAll(adir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(adir, "capabilities.json"), []byte(tc.json), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			orig := *rootDir
			*rootDir = tmp
			t.Cleanup(func() { *rootDir = orig })

			_, ext, err := loadCapabilities(adapter)
			if err != nil {
				t.Fatalf("loadCapabilities: %v", err)
			}
			if ext != tc.wantExt {
				t.Fatalf("ext = %q, want %q", ext, tc.wantExt)
			}
		})
	}
}
