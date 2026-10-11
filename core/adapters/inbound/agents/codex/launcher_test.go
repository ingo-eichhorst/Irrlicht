package codex

import (
	"errors"
	"testing"
)

// Issue #2083: a codex root hosted by the managed app-server daemon takes its
// launcher from the one codex TUI in its cwd, and from the daemon otherwise.
// The process table is faked: pids 29284 (managed daemon), 3766 (the VS Code
// extension's app-server), 70874 and 90049 (TUIs) carry the argv rows read on
// the dev machine on 2026-10-11 with `pgrep -fl codex`, paths shortened to
// their last element; ownRoot's row is made up.
func TestLauncherPID(t *testing.T) {
	const (
		cwd     = "/Users/x/proj"
		daemon  = 29284
		vscode  = 3766
		tui     = 70874
		tui2    = 90049
		ownRoot = 51000 // a TUI holding its own rollout (--no-daemon)
	)
	argv := map[int][]string{
		daemon:  {"codex", "app-server", "--listen", "unix://", "--analytics-default-enabled", "--managed-daemon"},
		vscode:  {"codex", "-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"},
		tui:     {"codex", "--yolo"},
		tui2:    {"codex", "--yolo"},
		ownRoot: {"codex", "--no-daemon"},
	}
	errScan := errors.New("lsof timed out")
	for _, tc := range []struct {
		name     string
		pid      int
		cwd      string
		hostArgv []string // overrides argv[pid] when non-nil
		tuis     []int
		scanErr  error
		unread   bool // the lone TUI's argv is unreadable
		want     int
		noScan   bool // the TUI scan must not run
	}{
		{name: "one TUI in the cwd", pid: daemon, tuis: []int{tui}, want: tui},
		{name: "two TUIs in the cwd", pid: daemon, tuis: []int{tui, tui2}},
		{name: "no TUI in the cwd", pid: daemon},
		{name: "root bound to its own TUI", pid: ownRoot, tuis: []int{tui}, noScan: true},
		// The VS Code extension's app-server talks stdio to the extension, so
		// no TUI drives its threads; a TUI that shares a thread's cwd must not
		// take the extension's launcher.
		{name: "root bound to a stdio app-server", pid: vscode, tuis: []int{tui}, noScan: true},
		{name: "--listen=unix form", pid: daemon, hostArgv: []string{"codex", "app-server", "--listen=unix://"}, tuis: []int{tui}, want: tui},
		{name: "--listen ws", pid: daemon, hostArgv: []string{"codex", "app-server", "--listen", "ws://127.0.0.1:4500"}, tuis: []int{tui}, want: tui},
		{name: "--listen stdio", pid: daemon, hostArgv: []string{"codex", "app-server", "--listen", "stdio://"}, tuis: []int{tui}, noScan: true},
		{name: "--listen off", pid: daemon, hostArgv: []string{"codex", "app-server", "--listen", "off"}, tuis: []int{tui}, noScan: true},
		{name: "--stdio", pid: daemon, hostArgv: []string{"codex", "app-server", "--stdio"}, tuis: []int{tui}, noScan: true},
		{name: "--listen with no value", pid: daemon, hostArgv: []string{"codex", "app-server", "--listen"}, tuis: []int{tui}, noScan: true},
		{name: "unreadable host argv", pid: daemon, hostArgv: []string{}, tuis: []int{tui}, noScan: true},
		// A scan that could not look is not a scan that found one TUI.
		{name: "TUI scan failed", pid: daemon, tuis: []int{tui}, scanErr: errScan},
		{name: "lone candidate argv unreadable", pid: daemon, tuis: []int{tui}, unread: true},
		{name: "no pid", pid: 0, tuis: []int{tui}, noScan: true},
		{name: "no cwd", pid: daemon, cwd: "-", tuis: []int{tui}, noScan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := cwd
			if tc.cwd == "-" {
				root = ""
			}
			readArgv := func(pid int) []string {
				switch {
				case pid == tc.pid && tc.hostArgv != nil:
					return tc.hostArgv
				case pid == tui && tc.unread:
					return nil
				}
				return argv[pid]
			}
			scanned := false
			scan := func(dir string) ([]int, error) {
				scanned = true
				if dir != root {
					t.Errorf("scanned cwd %q, want the root's %q", dir, root)
				}
				return tc.tuis, tc.scanErr
			}

			if got := launcherPID(root, tc.pid, readArgv, scan); got != tc.want {
				t.Errorf("launcherPID(%q, %d) = %d, want %d", root, tc.pid, got, tc.want)
			}
			if tc.noScan && scanned {
				t.Error("scanned for TUIs although the bound process is not a listening app-server")
			}
		})
	}
}

// The real hook is the declared one, so the PID manager's seam reaches it.
func TestAgentDeclaresLauncherPID(t *testing.T) {
	if Agent().Process.LauncherPID == nil {
		t.Fatal("codex declares no Process.LauncherPID, so every hosted root keeps the daemon's launcher")
	}
}
