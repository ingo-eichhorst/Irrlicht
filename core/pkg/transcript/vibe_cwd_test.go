package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractCWDFromVibeMetaJSON(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "messages.jsonl")

	// No sidecar at all.
	if got := ExtractCWDFromVibeMetaJSON(transcriptPath); got != "" {
		t.Errorf("no sidecar: got %q, want empty", got)
	}

	// Real meta.json shape: cwd nested under environment.working_directory,
	// alongside unrelated top-level fields the decoder must ignore.
	sidecar := filepath.Join(dir, "meta.json")
	body := `{"session_id":"abc","git_branch":"main","environment":{"working_directory":"/Users/test/project"},"loops":[]}`
	if err := os.WriteFile(sidecar, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractCWDFromVibeMetaJSON(transcriptPath); got != "/Users/test/project" {
		t.Errorf("got %q, want /Users/test/project", got)
	}

	// Malformed sidecar.
	if err := os.WriteFile(sidecar, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractCWDFromVibeMetaJSON(transcriptPath); got != "" {
		t.Errorf("malformed: got %q, want empty", got)
	}

	// Non-vibe transcript filenames never have this sidecar convention.
	if got := ExtractCWDFromVibeMetaJSON(filepath.Join(dir, "chat.jsonl")); got != "" {
		t.Errorf("non-vibe filename: got %q, want empty", got)
	}
}
