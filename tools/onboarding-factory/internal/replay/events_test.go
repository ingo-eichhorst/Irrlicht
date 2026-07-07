package replay

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/lifecycle"
)

func TestLoadEvents_orderedBySeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// Write out of seq order — LoadEvents must sort.
	content := `{"seq":3,"ts":"2026-05-01T13:11:54Z","kind":"state_transition","session_id":"s","new_state":"ready"}
{"seq":1,"ts":"2026-05-01T13:11:31Z","kind":"transcript_new","session_id":"s"}
{"seq":2,"ts":"2026-05-01T13:11:47Z","kind":"state_transition","session_id":"s","new_state":"working"}
`
	os.WriteFile(path, []byte(content), 0o644)

	events, err := LoadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("want 3, got %d", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 || events[2].Seq != 3 {
		t.Errorf("not sorted by seq: %+v", events)
	}
	if events[0].Kind != lifecycle.KindTranscriptNew {
		t.Errorf("kind wrong: %s", events[0].Kind)
	}
}

func TestLoadEvents_tolerantOfMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"seq":1,"ts":"2026-05-01T13:11:31Z","kind":"transcript_new","session_id":"s"}
this is not json
{"seq":2,"ts":"2026-05-01T13:11:47Z","kind":"state_transition","session_id":"s","new_state":"ready"}
`
	os.WriteFile(path, []byte(content), 0o644)
	events, err := LoadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Errorf("want 2 valid events after skipping malformed, got %d", len(events))
	}
}

func TestLoadEvents_emptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	os.WriteFile(path, []byte(""), 0o644)
	events, err := LoadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("want 0, got %d", len(events))
	}
}

// TestLoadEvents_rejectsPathTraversal proves LoadEvents refuses a path
// containing a literal ".." component even when it resolves to a real,
// readable file outside the intended directory — i.e. the guard runs
// before os.Open, not just when the target happens to be missing.
func TestLoadEvents_rejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(filepath.Dir(base), "irrlicht-traversal-events.jsonl")
	content := `{"seq":1,"ts":"2026-05-01T13:11:31Z","kind":"transcript_new","session_id":"s"}` + "\n"
	if err := os.WriteFile(outside, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)

	// Deliberately NOT filepath.Join (which would Clean away the ".."), so
	// the literal traversal segment survives into the string LoadEvents sees.
	traversal := base + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(outside)
	if _, err := os.Stat(traversal); err != nil {
		t.Fatalf("setup: traversal path should resolve to the real file: %v", err)
	}

	if events, err := LoadEvents(traversal); err == nil {
		t.Errorf("LoadEvents(%q) = %d events, nil error; want rejection", traversal, len(events))
	}
}

func TestLoadEvents_realSeedScenario(t *testing.T) {
	// Smoke test against the real committed seed corpus to confirm the
	// loader handles the actual on-disk shape produced by irrlichd.
	path := filepath.Join("..", "..", "..", "..", "replaydata", "agents",
		"claudecode", "regressions", "multi-turn-conversation", "events.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("seed corpus not present: %v", err)
	}
	events, err := LoadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 5 {
		t.Errorf("seed scenario should have many events, got %d", len(events))
	}
	// At least one state_transition.
	found := false
	for _, e := range events {
		if e.Kind == lifecycle.KindStateTransition {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one state_transition in seed events")
	}
}
