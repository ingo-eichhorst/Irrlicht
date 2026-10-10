package codex

import "testing"

// Issue #2082: since codex ~0.162 the binary named `codex` also runs
// non-interactive app-server infrastructure, and the process scanner minted a
// `proc-<pid>` placeholder row for each of them. The argv rows below were
// captured on the dev machine on 2026-10-11 with `ps -o args= -p <pid>` for
// every `pgrep -x codex` PID (pids 29284, 70892, 3766, 70874); the paths are
// shortened to their last element, which the predicate never reads.
func TestAgentExcludesCodexAppServerProcesses(t *testing.T) {
	exclude := Agent().Process.ExcludeArgv
	if exclude == nil {
		t.Fatal("codex declares no Process.ExcludeArgv, so the scanner mints a placeholder row for every codex app-server process")
	}
	for _, tc := range []struct {
		name string
		argv []string
		want bool
	}{
		{"managed daemon (pid 29284)", []string{"codex", "app-server", "--listen", "unix://", "--analytics-default-enabled", "--managed-daemon"}, true},
		{"daemon supervisor (pid 70892)", []string{"codex", "app-server", "daemon", "pid-update-loop"}, true},
		{"VS Code extension app-server (pid 3766)", []string{"codex", "-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"}, true},
		{"TUI (pid 70874)", []string{"codex", "--yolo"}, false},
		{"bare TUI", []string{"codex"}, false},
		{"TUI whose prompt mentions app-server", []string{"codex", "--yolo", "start the app-server"}, false},
		{"exec", []string{"codex", "exec", "fix the tests"}, false},
		// The ExcludeArgv contract: an unreadable argv is a session.
		{"unreadable argv", nil, false},
		{"empty argv", []string{}, false},
		// argv[0] is the executable path, never an argument.
		{"app-server only as argv[0]", []string{"app-server"}, false},
		// The value of an option is not the subcommand (review of #2082): a
		// monorepo package directory named app-server.
		{"TUI with --cd app-server", []string{"codex", "--cd", "app-server"}, false},
		{"TUI with --add-dir app-server", []string{"codex", "--yolo", "--add-dir", "app-server"}, false},
		{"app-server after a -c value", []string{"codex", "-c", "app-server", "--yolo"}, false},
	} {
		if got := exclude(tc.argv); got != tc.want {
			t.Errorf("%s: ExcludeArgv(%q) = %t, want %t", tc.name, tc.argv, got, tc.want)
		}
	}
}
