package codex

import (
	"strings"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// LauncherPID is codex's Process.LauncherPID (#2083). A root hosted by the
// shared app-server daemon is bound to the daemon's PID (#2077), and the
// daemon's environment is that of whichever TUI first spawned it, so the
// daemon's launcher points every hosted root at that one terminal. This names
// the TUI to read instead, when it can be told:
//
//   - pid is a codex app-server listening for clients on a socket — the
//     managed daemon runs `app-server --listen unix:// … --managed-daemon`
//     (`pgrep -fl codex` on the dev machine, 2026-10-11). The VS Code extension's
//     app-server has no --listen, and `codex app-server --help` gives
//     `stdio://` as its default, so it talks over its stdin and stdout to the
//     extension that spawned it: no TUI drives its threads, and its own
//     launcher (the extension's) stands;
//   - exactly one other codex process — one IsAppServerArgv does not match —
//     runs in cwd, and its argv was readable.
//
// Otherwise it returns 0 and the root keeps the daemon's launcher. Several
// TUIs in one cwd are never told apart: codex 0.162.1 exposes no mapping from
// a thread to the client attached to it (the re-triage on #2083 read the
// app-server protocol and ~/.codex/logs_2.sqlite for one and found none).
//
// What it does not see: a TUI started with `--cd DIR` whose process runs
// elsewhere is not counted in DIR. Whether codex chdirs to DIR was not
// checked, so a root in DIR can be attributed to a TUI that runs in DIR while
// the root's own TUI ran with --cd from somewhere else.
//
// Cost, as the darwin process observer implements the reads: one argv sysctl
// for pid; when pid is a listening app-server, one pgrep for `codex`, an argv
// sysctl per codex process and an lsof cwd read per non-app-server codex
// process (processlifecycle.PIDsByCWDExcludingArgv), plus one more argv
// sysctl for a lone candidate. The PID manager asks it only where a codex
// session's launcher is read: when a PID is bound to a session that has no
// launcher yet, and at most once per session at daemon startup.
func LauncherPID(cwd, transcriptPath string, pid int) int {
	return launcherPID(cwd, pid, processlifecycle.ReadArgv, tuisIn)
}

// tuisIn returns the codex processes in cwd that are not app-servers.
func tuisIn(cwd string) ([]int, error) {
	return processlifecycle.PIDsByCWDExcludingArgv(ProcessName, cwd, IsAppServerArgv)
}

// launcherPID is LauncherPID with its process reads injected, so the rule can
// be tested against a fake process table.
func launcherPID(cwd string, pid int, readArgv func(int) []string, tuisIn func(cwd string) ([]int, error)) int {
	if pid <= 0 || cwd == "" || !listensForClients(readArgv(pid)) {
		return 0
	}
	tuis, err := tuisIn(cwd)
	if err != nil || len(tuis) != 1 {
		return 0
	}
	// An unreadable argv is not matched by IsAppServerArgv, so the scan keeps
	// it as a TUI; a lone candidate nothing is known about is not one.
	if len(readArgv(tuis[0])) == 0 {
		return 0
	}
	return tuis[0]
}

// listensForClients reports whether argv is a codex app-server listening on a
// transport other clients can attach to: `--listen <URL>` or `--listen=<URL>`
// with a URL other than `stdio://` (the default) or `off`, per `codex
// app-server --help` at 0.162.1.
func listensForClients(argv []string) bool {
	if !IsAppServerArgv(argv) {
		return false
	}
	for i, arg := range argv {
		url, ok := strings.CutPrefix(arg, "--listen=")
		if !ok {
			if arg != "--listen" || i+1 >= len(argv) {
				continue
			}
			url = argv[i+1]
		}
		return url != "" && url != "stdio://" && url != "off"
	}
	return false
}
