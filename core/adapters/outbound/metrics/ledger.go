package metrics

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"irrlicht/core/pkg/tailer"
)

const ledgerSchemaVersion = 2 // bumped to force re-scan for task support

var ledgerDirOnce sync.Once

// ensureLedgerDir creates the ledger directory on the first call; subsequent
// calls are no-ops. Silent on error — a missing dir causes saveLedger to fail
// silently, which is acceptable.
func ensureLedgerDir() {
	ledgerDirOnce.Do(func() {
		dir, err := ledgerDir()
		if err != nil {
			return
		}
		_ = os.MkdirAll(dir, 0o755)
	})
}

// ledgerDir returns the directory where per-session ledger files are stored.
func ledgerDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "irrlicht", "sessions"), nil
}

// ledgerPath returns the filesystem path for the ledger of a given transcript.
// The name is a short SHA-256 prefix of the transcript path — collision-free
// for any realistic number of sessions and filesystem-safe on all platforms.
func ledgerPath(transcriptPath string) string {
	dir, err := ledgerDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, LedgerFilename(transcriptPath))
}

// LedgerFilename returns the basename of the ledger file for a transcript path.
// Exposed so the daemon-startup orphan sweep can compute the expected filenames
// for live sessions without re-implementing the SHA-256 scheme.
func LedgerFilename(transcriptPath string) string {
	h := sha256.Sum256([]byte(transcriptPath))
	return fmt.Sprintf("%x.ledger.json", h[:8])
}

// LedgerDir returns the directory holding per-session ledger files.
// Exposed for the daemon-startup orphan sweep.
func LedgerDir() (string, error) { return ledgerDir() }

// loadLedger reads the ledger at path, returning nil on error or version mismatch.
// Silent on all errors so a missing or corrupt ledger just falls back to a fresh scan.
func loadLedger(path string) *tailer.LedgerState {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s tailer.LedgerState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	if s.SchemaVersion != ledgerSchemaVersion {
		return nil
	}
	return &s
}

// saveLedger atomically writes state to path via a tmp-file + rename so a
// crash mid-write never leaves a corrupt ledger. Silent on error.
func saveLedger(path string, state tailer.LedgerState) {
	if path == "" {
		return
	}
	ensureLedgerDir()
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
