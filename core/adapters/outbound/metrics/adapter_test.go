package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLedgerFilenameDeterministic(t *testing.T) {
	a := LedgerFilename("/path/to/transcript.jsonl")
	b := LedgerFilename("/path/to/transcript.jsonl")
	if a != b {
		t.Fatalf("LedgerFilename not deterministic: %q vs %q", a, b)
	}
	if LedgerFilename("/path/to/other.jsonl") == a {
		t.Fatalf("LedgerFilename collided across distinct paths")
	}
}

func TestPruneEntry_RemovesLedgerAndCacheEntry(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := filepath.Join(tmpHome, ".local", "share", "irrlicht", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	transcript := filepath.Join(tmpHome, "transcript.jsonl")
	lp := ledgerPath(transcript)
	if err := os.WriteFile(lp, []byte(`{"schemaVersion":2}`), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	a := New(nil)
	a.tailers[transcript] = &lockedTailer{}

	a.PruneEntry(transcript)

	if _, ok := a.tailers[transcript]; ok {
		t.Errorf("tailers map still contains %q after PruneEntry", transcript)
	}
	if _, err := os.Stat(lp); !os.IsNotExist(err) {
		t.Errorf("ledger file still present after PruneEntry: err=%v", err)
	}
}

func TestPruneEntry_IdempotentOnMissingFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	a := New(nil)
	// No file written, no map entry — should not panic or error.
	a.PruneEntry(filepath.Join(tmpHome, "never-existed.jsonl"))
}

func TestPruneEntry_EmptyPathNoop(t *testing.T) {
	a := New(nil)
	a.PruneEntry("") // must not panic
}
