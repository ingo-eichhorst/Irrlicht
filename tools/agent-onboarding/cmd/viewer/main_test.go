package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRun_usageErrors covers the structured exit codes (#461 finding #6):
// bad flags and a repo-root without replaydata/agents both return
// exitUsageErr (2) rather than the old log.Fatalf's blanket 1.
func TestRun_usageErrors(t *testing.T) {
	t.Run("unknown flag", func(t *testing.T) {
		if got := run([]string{"-nope"}); got != exitUsageErr {
			t.Errorf("run(-nope) = %d; want %d", got, exitUsageErr)
		}
	})

	t.Run("missing replaydata", func(t *testing.T) {
		// A temp dir with no replaydata/agents subdir.
		if got := run([]string{"-repo-root", t.TempDir()}); got != exitUsageErr {
			t.Errorf("run(empty repo) = %d; want %d", got, exitUsageErr)
		}
	})

	t.Run("help exits 0", func(t *testing.T) {
		// -h/--help is a successful user request, not a usage error.
		if got := run([]string{"-h"}); got != exitOK {
			t.Errorf("run(-h) = %d; want %d", got, exitOK)
		}
	})

	t.Run("present replaydata passes the config gate", func(t *testing.T) {
		// Create replaydata/agents so the config check passes, then bind to
		// an impossible port so ListenAndServe fails fast → exitRuntimeErr.
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "replaydata", "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Port 0 is not a valid explicit listen target here ("address :-1");
		// use an out-of-range port so ListenAndServe returns immediately.
		if got := run([]string{"-repo-root", root, "-addr", "127.0.0.1:-1"}); got != exitRuntimeErr {
			t.Errorf("run(bad addr) = %d; want %d", got, exitRuntimeErr)
		}
	})
}
