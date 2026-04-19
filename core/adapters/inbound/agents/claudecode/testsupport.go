package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// WriteSessionMetaForTest writes a ~/.claude/sessions/<pid>.json file in dir
// and sets its mtime. Exported so tests in other packages (e.g. services
// E2E) can construct the same metadata fixtures as the package's own unit
// tests. Not for production callers.
func WriteSessionMetaForTest(dir string, pid int, sessionID string, mtime time.Time) error {
	data, err := json.Marshal(claudeSessionMeta{PID: pid, SessionID: sessionID})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return os.Chtimes(path, mtime, mtime)
}

// ReplaceTestDeps swaps DiscoverPID's filesystem dependencies (sessionsDir,
// pidAlive, discoverByCWD) with caller-provided stubs and returns a closure
// that restores the originals. Intended only for tests in other packages
// (e.g. services) that need to drive the real DiscoverPID against controlled
// filesystem state. Not for production callers.
//
// Any of cwd / alive may be nil to keep the current implementation.
func ReplaceTestDeps(
	dir string,
	alive func(int) bool,
	cwd func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error),
) (restore func()) {
	origDir := sessionsDir
	origAlive := pidAlive
	origCWD := discoverByCWD

	sessionsDir = dir
	if alive != nil {
		pidAlive = alive
	}
	if cwd != nil {
		discoverByCWD = cwd
	}

	return func() {
		sessionsDir = origDir
		pidAlive = origAlive
		discoverByCWD = origCWD
	}
}
