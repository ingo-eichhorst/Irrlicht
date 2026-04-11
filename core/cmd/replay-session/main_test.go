package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
)

// fixturePath returns an absolute path to a fixture under the repo-root
// testdata/replay/<adapter>/ tree. The test binary runs from the package
// directory (core/cmd/replay-session), so we walk up three parents.
func fixturePath(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "replay", rel))
	if err != nil {
		t.Fatalf("abs fixture path: %v", err)
	}
	return abs
}

// TestReplayWithSidecar_GoldenFixture locks in the regression oracle: the
// committed 10-full-lifecycle-839f0678 fixture must replay to the exact
// set of state transitions the daemon recorded in the sidecar, with no
// ordered-diff mismatches. Any drift here indicates a real change in
// classifier, tailer, or debounce behavior.
func TestReplayWithSidecar_GoldenFixture(t *testing.T) {
	transcript := fixturePath(t, "claudecode/10-full-lifecycle-839f0678.jsonl")
	sidecar := fixturePath(t, "claudecode/10-full-lifecycle-839f0678.events.jsonl")

	report, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ReplayWithSidecar: %v", err)
	}

	check, err := runExtendedCheck(sidecar, report.Transitions)
	if err != nil {
		t.Fatalf("runExtendedCheck: %v", err)
	}

	// Lock in the expected shape. Numbers come from the committed fixture;
	// if they drift, investigate whether the detector changed behavior.
	const (
		wantRecorded = 10
		wantMatches  = 10
	)
	if check.RecordedCount != wantRecorded {
		t.Errorf("recorded transitions: got %d, want %d", check.RecordedCount, wantRecorded)
	}
	if check.OrderedMatches != wantMatches {
		t.Errorf("ordered matches: got %d, want %d", check.OrderedMatches, wantMatches)
	}
	if len(check.OrderedMismatches) != 0 {
		t.Errorf("ordered mismatches: got %d, want 0 — %+v", len(check.OrderedMismatches), check.OrderedMismatches)
	}
	if len(check.MissingKinds) != 0 {
		t.Errorf("missing kinds: got %v, want none", check.MissingKinds)
	}
	if len(check.ExtraKinds) != 0 {
		t.Errorf("extra kinds: got %v, want none", check.ExtraKinds)
	}
}

// TestReplayWithSidecar_RejectsMultiSessionSidecar ensures the fail-fast
// guard against sidecars containing more than one session_id works —
// curated fixtures are required to be single-session.
func TestReplayWithSidecar_RejectsMultiSessionSidecar(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "fake.jsonl")
	sidecar := filepath.Join(dir, "fake.events.jsonl")

	// Minimal transcript with a single line so ReadFile succeeds.
	if err := os.WriteFile(transcript, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	// Sidecar with two different session_ids.
	sidecarBody := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"transcript_activity","session_id":"sess-A","file_size":10}
{"seq":2,"ts":"2026-04-11T10:00:01Z","kind":"transcript_activity","session_id":"sess-B","file_size":20}
`
	if err := os.WriteFile(sidecar, []byte(sidecarBody), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	_, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error for multi-session sidecar, got nil")
	}
}
