package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractCWDFromAntigravityHistory(t *testing.T) {
	home := t.TempDir()
	brainParent := filepath.Join(home, ".gemini", "antigravity-cli")
	conv := "9ea5200c-3d8a-4075-8f85-2609cef25e78"
	logs := filepath.Join(brainParent, "brain", conv, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(logs, "transcript.jsonl")

	// history.jsonl: noise lines, a workspace-less slash line, then two
	// workspace entries for our conv (the LAST must win — a session can move).
	history := `{"display":"ls","timestamp":1,"workspace":"/Users/x/other"}
{"display":"/model","timestamp":2,"workspace":"/Users/x/old","conversationId":"` + conv + `","type":"slash_command"}
{"display":"hi","timestamp":3,"conversationId":"other-conv","workspace":"/Users/x/nope"}
{"display":"go","timestamp":4,"workspace":"/Users/x/proj","conversationId":"` + conv + `"}
`
	if err := os.WriteFile(filepath.Join(brainParent, "history.jsonl"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ExtractCWDFromAntigravityHistory(transcriptPath); got != "/Users/x/proj" {
		t.Errorf("cwd = %q, want /Users/x/proj (last workspace for the conv-id)", got)
	}
}

func TestExtractCWDFromAntigravityHistory_NonAntigravityPath(t *testing.T) {
	// A non-antigravity transcript path must be a no-op (no panic, "").
	for _, p := range []string{
		"/Users/x/.codex/sessions/2026/06/19/rollout-abc.jsonl",
		"/Users/x/.claude/projects/foo/abc.jsonl",
		"/Users/x/.gemini/antigravity-cli/brain/conv/.system_generated/logs/transcript_full.jsonl", // unfiltered sibling
		"/Users/x/.gemini/antigravity-cli/brain/conv/transcript.jsonl",                             // wrong depth
		"",
	} {
		if got := ExtractCWDFromAntigravityHistory(p); got != "" {
			t.Errorf("non-antigravity path %q: got %q, want \"\"", p, got)
		}
	}
}

func TestExtractCWDFromAntigravityHistory_NoEntryYet(t *testing.T) {
	// history.jsonl exists but has no line for this conv-id yet (written
	// lazily) → "" so the caller retries on the next activity refresh.
	home := t.TempDir()
	brainParent := filepath.Join(home, ".gemini", "antigravity-cli")
	conv := "11111111-2222-3333-4444-555555555555"
	logs := filepath.Join(brainParent, "brain", conv, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brainParent, "history.jsonl"),
		[]byte(`{"display":"x","workspace":"/a","conversationId":"someone-else"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractCWDFromAntigravityHistory(filepath.Join(logs, "transcript.jsonl")); got != "" {
		t.Errorf("got %q, want \"\" (no entry for this conv-id yet)", got)
	}
}
