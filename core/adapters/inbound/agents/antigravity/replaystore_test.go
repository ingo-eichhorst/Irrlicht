package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/application/replayengine"
	"irrlicht/core/pkg/tailer"
)

// Compile-time proof the parser opts into replay store staging — the seam that
// lets replay resolve the sibling conversation store the daemon reads live (#766).
var _ tailer.ReplayStoreStager = (*Parser)(nil)

// turnLines is one trivial turn — prompt, a working tool step, then a terminal
// line that settles to turn_done (where the store is read). Shared by the
// staging and replay tests.
func turnLines() []map[string]any {
	return []map[string]any{
		line("USER_EXPLICIT", "USER_INPUT", 0,
			"<USER_REQUEST>\nhi\n</USER_REQUEST>\n<USER_SETTINGS_CHANGE>\nThe user changed setting `Model Selection` from None to Gemini 3.1 Pro (Low).\n</USER_SETTINGS_CHANGE>", nil),
		line("MODEL", "PLANNER_RESPONSE", 1, "I will run it.", runCommand("/repo")),
		line("MODEL", "PLANNER_RESPONSE", 2, "", nil),
	}
}

// writeRecording lays out a recording dir the way promote-recording.sh does: a
// flat transcript.jsonl plus, when rows != nil, the captured store under
// store/conversations/<conv>.db (the #766 capture). Returns the recording dir.
func writeRecording(t *testing.T, conv string, lines []map[string]any, rows map[int][]byte) string {
	t.Helper()
	recDir := t.TempDir()
	var buf []byte
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(append(buf, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(recDir, transcriptFilename), buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if rows != nil {
		// writeStore creates <root>/conversations/<conv>.db; rooting it at
		// recDir/store reproduces the rig's store/conversations/<conv>.db layout.
		writeStore(t, filepath.Join(recDir, "store"), conv, rows)
	}
	return recDir
}

// TestStageReplayStore proves the staging rebuilds the live layout so the
// unchanged storePathForTranscript resolves the captured store, and that the
// WAL sibling rides along.
func TestStageReplayStore(t *testing.T) {
	conv := "abc-123"
	recDir := writeRecording(t, conv, turnLines(), map[int][]byte{0: genBlob(16353, "gemini-3.1-pro-low")})
	// A -wal sibling must survive the round-trip (the reader is WAL-aware).
	walSrc := filepath.Join(recDir, "store", "conversations", conv+".db-wal")
	if err := os.WriteFile(walSrc, []byte("wal"), 0o644); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	got, err := (&Parser{}).StageReplayStore(tmpDir, recDir)
	if err != nil {
		t.Fatalf("StageReplayStore: %v", err)
	}

	want := filepath.Join(tmpDir, "brain", conv, ".system_generated", "logs", transcriptFilename)
	if got != want {
		t.Fatalf("transcript path = %q, want %q", got, want)
	}
	// Round-trip guard: the path the daemon's UNCHANGED resolver derives from the
	// reconstructed transcript must land on the staged store. If the brain layout
	// here and sessionIDFromPath/storePathForTranscript ever drift apart, this fails.
	stored := storePathForTranscript(got)
	if want := filepath.Join(tmpDir, "conversations", conv+".db"); stored != want {
		t.Fatalf("storePathForTranscript(%q) = %q, want %q", got, stored, want)
	}
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("staged db not found at %s: %v", stored, err)
	}
	if _, err := os.Stat(stored + "-wal"); err != nil {
		t.Errorf("WAL sibling not staged alongside the db: %v", err)
	}
}

// TestStageReplayStoreNoStore proves a recording without a captured store
// (every pre-#766 recording) makes the stager a no-op so replay falls back to
// the flat transcript path.
func TestStageReplayStoreNoStore(t *testing.T) {
	recDir := writeRecording(t, "abc-123", turnLines(), nil)
	got, err := (&Parser{}).StageReplayStore(t.TempDir(), recDir)
	if err != nil {
		t.Fatalf("StageReplayStore: %v", err)
	}
	if got != "" {
		t.Errorf("no captured store: want %q (flat-path fallback), got %q", "", got)
	}
}

// TestReplayResolvesStagedStore is the end-to-end proof: a recording carrying a
// captured store replays through the production engine with context tokens and
// a resolved context window — the signals #719 reads live, now observable from
// the recording alone.
func TestReplayResolvesStagedStore(t *testing.T) {
	recDir := writeRecording(t, "abc-123", turnLines(), map[int][]byte{0: genBlob(16353, "gemini-3.1-pro-low")})

	res, err := replayengine.ReplayTranscript(filepath.Join(recDir, transcriptFilename), replayengine.Options{
		Adapter:                    AdapterName,
		Parser:                     &Parser{},
		DebounceWindow:             2 * time.Second,
		DisableModelConfigFallback: true,
	})
	if err != nil {
		t.Fatalf("ReplayTranscript: %v", err)
	}
	m := res.LastMetrics
	if m == nil {
		t.Fatal("replay produced no LastMetrics")
	}
	if m.TotalTokens != 16353 {
		t.Errorf("TotalTokens = %d, want 16353 from the staged store", m.TotalTokens)
	}
	if m.ContextWindow <= 0 {
		t.Errorf("ContextWindow = %d, want > 0 (resolved via the staged store + capacity map)", m.ContextWindow)
	}
	if m.ContextUtilization <= 0 {
		t.Errorf("ContextUtilization = %.3f%%, want > 0", m.ContextUtilization)
	}
}

// TestReplayWithoutStoreIsStoreless is the negative control: the IDENTICAL
// transcript with no captured store replays storeless, proving the staging —
// not the transcript — is what surfaces the db-backed metrics.
func TestReplayWithoutStoreIsStoreless(t *testing.T) {
	recDir := writeRecording(t, "abc-123", turnLines(), nil)

	res, err := replayengine.ReplayTranscript(filepath.Join(recDir, transcriptFilename), replayengine.Options{
		Adapter:                    AdapterName,
		Parser:                     &Parser{},
		DebounceWindow:             2 * time.Second,
		DisableModelConfigFallback: true,
	})
	if err != nil {
		t.Fatalf("ReplayTranscript: %v", err)
	}
	if res.LastMetrics.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0 without a captured store", res.LastMetrics.TotalTokens)
	}
	if res.LastMetrics.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 without a captured store", res.LastMetrics.ContextWindow)
	}
}
