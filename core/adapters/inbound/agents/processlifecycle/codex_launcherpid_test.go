package processlifecycle_test

import (
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// Review of #2083: codex.LauncherPID's own process reads — the `codex` process
// name, the IsAppServerArgv exclusion and ReadArgv — run here against a fake
// process table, since codex's table test injects both readers. The rows
// mirror the dev machine on 2026-10-11 (`pgrep -fl codex`, and `lsof -a -p
// <pid> -d cwd` for the cwd): the managed daemon and its pid-update-loop
// supervisor run in the cwd of the TUI that spawned them, and npm's node
// wrapper sits beside each TUI.
func TestCodexLauncherPIDReadsTheProcessTable(t *testing.T) {
	cwd := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved // CWDOf reports the canonical path
	}
	const (
		daemon     = 29284
		supervisor = 70892
		tui        = 70874
		wrapper    = 70873
		elsewhere  = 90049
	)
	processlifecycle.SetFakeProcessesForTest(t, map[int]processlifecycle.FakeProcess{
		daemon:     {Name: "codex", CWD: cwd, Argv: []string{"codex", "app-server", "--listen", "unix://", "--analytics-default-enabled", "--managed-daemon"}},
		supervisor: {Name: "codex", CWD: cwd, Argv: []string{"codex", "app-server", "daemon", "pid-update-loop"}},
		tui:        {Name: "codex", CWD: cwd, Argv: []string{"codex", "--yolo"}},
		wrapper:    {Name: "node", CWD: cwd, Argv: []string{"node", "/usr/local/bin/codex", "--yolo"}},
		elsewhere:  {Name: "codex", CWD: "/Users/x/other", Argv: []string{"codex", "--yolo"}},
	})

	if got := codex.LauncherPID(cwd, "", daemon); got != tui {
		t.Errorf("codex.LauncherPID(cwd, daemon) = %d, want the lone TUI %d: the scan must count codex processes in the cwd other than the app-servers", got, tui)
	}
}
