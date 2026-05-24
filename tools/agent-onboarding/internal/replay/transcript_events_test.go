package replay

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"irrlicht/core/domain/lifecycle"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// tools/agent-onboarding/internal/replay/<file> → repo root is four up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
}

const ublockingQuestion = "replaydata/agents/claudecode/scenarios/user-blocking-question"

// TestLoadEventsOrSynthesize_prefersSidecar: when events.jsonl exists the
// loader consumes the daemon-recorded sidecar verbatim and reports the
// timeline as NOT degraded.
func TestLoadEventsOrSynthesize_prefersSidecar(t *testing.T) {
	dir := filepath.Join(repoRoot(t), ublockingQuestion)
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Skipf("fixture has no events.jsonl: %v", err)
	}

	events, degraded, err := LoadEventsOrSynthesize(dir, "claudecode")
	if err != nil {
		t.Fatalf("LoadEventsOrSynthesize: %v", err)
	}
	if degraded {
		t.Error("degraded should be false when events.jsonl is present")
	}
	if len(events) == 0 {
		t.Error("expected recorded events from the sidecar")
	}
}

// TestLoadEventsOrSynthesize_degradedUsesClassifier is the regression guard
// for issue #461 finding #1. With no events.jsonl sidecar, the synthesized
// timeline must (a) be flagged degraded and (b) route through `waiting`,
// proving it ran the shared classifier rather than the old fabricated
// ready↔working arc that had no waiting/permission semantics.
func TestLoadEventsOrSynthesize_degradedUsesClassifier(t *testing.T) {
	srcTranscript := filepath.Join(repoRoot(t), ublockingQuestion, "transcript.jsonl")
	data, err := os.ReadFile(srcTranscript)
	if err != nil {
		t.Skipf("fixture transcript unavailable: %v", err)
	}

	// A scenario dir with ONLY the transcript — no events.jsonl sidecar.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	events, degraded, err := LoadEventsOrSynthesize(dir, "claudecode")
	if err != nil {
		t.Fatalf("LoadEventsOrSynthesize: %v", err)
	}
	if !degraded {
		t.Error("degraded should be true when the timeline is synthesized")
	}
	if len(events) == 0 {
		t.Fatal("expected synthesized events")
	}

	if events[0].Kind != lifecycle.KindTranscriptNew {
		t.Errorf("first event = %q; want transcript_new", events[0].Kind)
	}
	if last := events[len(events)-1]; last.Kind != lifecycle.KindTranscriptRemoved {
		t.Errorf("last event = %q; want transcript_removed", last.Kind)
	}

	var sawWaiting, sawWorking bool
	for _, e := range events {
		if e.Kind != lifecycle.KindStateTransition {
			continue
		}
		switch e.NewState {
		case "waiting":
			sawWaiting = true
		case "working":
			sawWorking = true
		}
	}
	if !sawWorking {
		t.Error("expected a working transition in the synthesized arc")
	}
	if !sawWaiting {
		t.Error("expected a waiting transition — the synthesized arc must carry classifier semantics, not a naive ready↔working arc")
	}
}

// TestResolveParser_mapsSlugToCanonical covers the dir-slug → canonical
// adapter mapping the synthesis path relies on to pick the right parser.
func TestResolveParser_mapsSlugToCanonical(t *testing.T) {
	cases := map[string]string{
		"claudecode": "claude-code",
		"":           "claude-code",
		"codex":      "codex",
		"pi":         "pi",
		"aider":      "aider",
		"opencode":   "opencode",
		"unknown":    "claude-code", // fallback
	}
	for slug, wantCanonical := range cases {
		got, parser := resolveParser(slug)
		if got != wantCanonical {
			t.Errorf("resolveParser(%q) canonical = %q; want %q", slug, got, wantCanonical)
		}
		if parser == nil {
			t.Errorf("resolveParser(%q) returned nil parser", slug)
		}
	}
}
