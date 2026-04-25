package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/domain/session"
)

// TestPruneOrphanLedgers_KeepsExpectedRemovesOrphans seeds a temp HOME with a
// sessions dir containing one ledger file matching a live session and one
// orphan, plus a non-ledger file that must be left alone. The sweep should
// remove only the orphan.
func TestPruneOrphanLedgers_KeepsExpectedRemovesOrphans(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := filepath.Join(tmpHome, ".local", "share", "irrlicht", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	livePath := "/some/transcripts/live.jsonl"
	liveLedger := filepath.Join(dir, metrics.LedgerFilename(livePath))
	orphanLedger := filepath.Join(dir, "deadbeefcafef00d.ledger.json")
	bystander := filepath.Join(dir, "README.txt")
	for _, p := range []string{liveLedger, orphanLedger, bystander} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatalf("seed file %s: %v", p, err)
		}
	}

	repo := filesystem.NewWithDir(t.TempDir())
	if err := repo.Save(&session.SessionState{
		SessionID:      "live1",
		State:          session.StateWorking,
		TranscriptPath: livePath,
		UpdatedAt:      time.Now().Unix(),
	}); err != nil {
		t.Fatalf("repo.Save: %v", err)
	}

	logger := &capturingLogger{}
	pruneOrphanLedgers(repo, logger)

	if _, err := os.Stat(liveLedger); err != nil {
		t.Errorf("live ledger removed unexpectedly: %v", err)
	}
	if _, err := os.Stat(orphanLedger); !os.IsNotExist(err) {
		t.Errorf("orphan ledger still present: err=%v", err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("non-ledger file removed: %v", err)
	}

	foundLog := false
	for _, msg := range logger.infos {
		if msg == "pruned 1 orphan ledger files" {
			foundLog = true
		}
	}
	if !foundLog {
		t.Errorf("missing prune log; infos=%v", logger.infos)
	}
}

// TestPruneOrphanLedgers_NoLedgerDir is a no-op when the ledger directory
// does not yet exist (fresh install before any session has run).
func TestPruneOrphanLedgers_NoLedgerDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filesystem.NewWithDir(t.TempDir())
	logger := &capturingLogger{}
	pruneOrphanLedgers(repo, logger) // must not panic
	if len(logger.errors) != 0 {
		t.Errorf("expected no errors on missing dir, got %v", logger.errors)
	}
}
