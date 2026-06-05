package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractCWDFromSidecar(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "abc-123.jsonl")

	// No sidecar at all.
	if got := ExtractCWDFromSidecar(transcriptPath); got != "" {
		t.Errorf("no sidecar: got %q, want empty", got)
	}

	// Kiro-shaped sidecar: cwd is an early top-level key, followed by a
	// deeply nested session_state blob the decoder must not need to parse.
	sidecar := filepath.Join(dir, "abc-123.json")
	body := `{"session_id":"abc-123","cwd":"/Users/test/project","created_at":"2026-06-05T10:00:00Z","session_state":{"conversation_metadata":{"user_turn_metadatas":[{"metering_usage":[{"value":0.03,"unit":"credit"}]}]}}}`
	if err := os.WriteFile(sidecar, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractCWDFromSidecar(transcriptPath); got != "/Users/test/project" {
		t.Errorf("got %q, want /Users/test/project", got)
	}

	// cwd after a nested object (key order is not guaranteed).
	if err := os.WriteFile(sidecar, []byte(`{"session_state":{"nested":[1,{"cwd":"/decoy"}]},"cwd":"/real"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractCWDFromSidecar(transcriptPath); got != "/real" {
		t.Errorf("nested-first: got %q, want /real", got)
	}

	// Malformed sidecar.
	if err := os.WriteFile(sidecar, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractCWDFromSidecar(transcriptPath); got != "" {
		t.Errorf("malformed: got %q, want empty", got)
	}

	// Non-jsonl transcript paths never have a sidecar convention.
	if got := ExtractCWDFromSidecar(filepath.Join(dir, "history.md")); got != "" {
		t.Errorf("non-jsonl: got %q, want empty", got)
	}
}
