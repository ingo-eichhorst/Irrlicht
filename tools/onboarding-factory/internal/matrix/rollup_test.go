package matrix

import (
	"os"
	"path/filepath"
	"testing"
)

// NOTE: TestRollupInSync was removed in the P3 schema cutover (#524). The
// committed agent-scenarios-coverage.json it guarded is deleted — coverage is
// derived on demand (rollup stays as an in-memory builder reused by the
// forthcoming `of coverage`). BuildRollup itself is still exercised below.

// TestReadOverlayCarriesGeneratedAt pins the #510-P2 stable-fixpoint mechanism:
// ReadOverlay MUST parse `generated_at` from the committed rollup so BuildRollup
// can carry it forward (a cell re-pointed to a folder with an earlier
// assessed_at must not lower the emitted timestamp and spuriously fail
// `rollup --check`). Regression guard: the carry-forward branch was once dead
// because ReadOverlay didn't parse the field, so it silently fell back to the
// derived max every time.
func TestReadOverlayCarriesGeneratedAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-scenarios-coverage.json")
	const stamp = "2099-01-02T03:04:05Z"
	if err := os.WriteFile(path, []byte(`{
  "generated_at": "`+stamp+`",
  "legend": {"k": "v"},
  "source_catalog": "x",
  "scenarios": [{"id": "s", "coverage": {"ag": {"notes": "n"}}}]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ov := ReadOverlay(path)
	if ov.GeneratedAt != stamp {
		t.Fatalf("ReadOverlay did not parse generated_at: got %q want %q", ov.GeneratedAt, stamp)
	}

	// An empty Matrix derives maxAssessedAt == "" (no assessments), so without
	// the carry-forward BuildRollup would emit "". With it, the overlay stamp wins.
	doc := (&Matrix{}).BuildRollup(ov)
	if doc.GeneratedAt != stamp {
		t.Fatalf("BuildRollup did not carry forward generated_at: got %q want %q (carry-forward is dead)", doc.GeneratedAt, stamp)
	}
}
