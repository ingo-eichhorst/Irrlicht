// Package processlifecycle owns the full process lifecycle for agent sessions:
// birth detection (polling) and death detection (exit watching). It unifies the
// previously separate processscanner and process/watcher packages. All OS
// coupling for process *discovery* (find-by-name/cmdline, cwd, file ownership,
// env) lives behind the outbound.ProcessObserver seam (process_darwin.go,
// process_linux.go, process_other.go), selected at compile time; this file
// holds the OS-agnostic launcher-identity assembly plus the darwin-specific
// KERN_PROCARGS2 parser used by the darwin observer.
package processlifecycle

import (
	"encoding/binary"
	"strconv"
	"strings"

	"irrlicht/core/domain/session"
)

// CWDToProjectDir converts a working directory path to the directory name used
// by Claude Code under ~/.claude/projects/. Claude Code replaces both "/" and
// "." with "-", so "/Users/ingo/projects/foo" becomes "-Users-ingo-projects-foo"
// and "/path/.hidden/sub" becomes "-path--hidden-sub".
func CWDToProjectDir(cwd string) string {
	s := strings.ReplaceAll(cwd, "/", "-")
	return strings.ReplaceAll(s, ".", "-")
}

// launcherEnvKeys are the env vars whitelisted for launcher identity capture.
// Everything else is ignored — we never read the full env, only these keys.
var launcherEnvKeys = map[string]struct{}{
	"TERM_PROGRAM":      {},
	"ITERM_SESSION_ID":  {},
	"TERM_SESSION_ID":   {},
	"TMUX":              {},
	"TMUX_PANE":         {},
	"VSCODE_PID":        {},
	"TERMINAL_EMULATOR": {}, // JetBrains JediTerm sets this to "JetBrains-JediTerm"
	"KITTY_LISTEN_ON":   {}, // kitty remote-control socket path (e.g. "unix:/tmp/kitty-NNN/sock")
	"KITTY_WINDOW_ID":   {}, // kitty window ID for precise window targeting
	"KITTY_PID":         {}, // kitty.app PID; lets the macOS activator target this specific kitty instance
}

// ReadLauncherEnv returns the launcher identity captured from the process env
// of pid. Returns nil if env cannot be read or no interesting vars are present.
//
// Never blocks longer than 2 seconds. Never prompts the user — on macOS we use
// `sysctl(kern.procargs2)` (no TCC prompt; `ps e` stopped exposing env on
// modern macOS). On Linux we read /proc/<pid>/environ. Other platforms return
// nil.
func ReadLauncherEnv(pid int) *session.Launcher {
	if pid <= 0 {
		return nil
	}
	// Env may be empty — hardened-runtime processes hide it from sysctl.
	// Don't bail here: the ancestry fallback below is the only signal we
	// have in that case.
	env, _ := osProc.EnvOf(pid)

	l := &session.Launcher{
		TermProgram:    env["TERM_PROGRAM"],
		ITermSessionID: env["ITERM_SESSION_ID"],
		TermSessionID:  env["TERM_SESSION_ID"],
		TmuxPane:       env["TMUX_PANE"],
		KittyListenOn:  env["KITTY_LISTEN_ON"],
		KittyWindowID:  env["KITTY_WINDOW_ID"],
	}
	if tmux := env["TMUX"]; tmux != "" {
		// $TMUX is "/path/to/socket,pid,session" — first field is the socket.
		if i := strings.Index(tmux, ","); i > 0 {
			l.TmuxSocket = tmux[:i]
		} else {
			l.TmuxSocket = tmux
		}
	}
	if v := env["VSCODE_PID"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			l.VSCodePID = n
		}
	}
	if v := env["KITTY_PID"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			l.KittyPID = n
		}
	}
	// Treat VSCODE_PID as an implicit TERM_PROGRAM hint when the env only
	// exposes VS Code / Cursor / Windsurf indirectly (their integrated
	// terminal sets VSCODE_PID but not always TERM_PROGRAM=vscode).
	if l.TermProgram == "" && l.VSCodePID > 0 {
		l.TermProgram = "vscode"
	}
	// JetBrains IDEs embed JediTerm which sets TERMINAL_EMULATOR but not
	// TERM_PROGRAM. Map it to the shared "jetbrains" term_program key that
	// the Swift registry routes to JetBrainsActivator.
	if l.TermProgram == "" && env["TERMINAL_EMULATOR"] == "JetBrains-JediTerm" {
		l.TermProgram = "jetbrains"
	}
	// The ancestry walk is cached because three guarded blocks below may
	// all need it (kitty TermProgram override, hardened-runtime TermProgram
	// fallback, kitty field back-fill). Walking the ppid chain once instead
	// of up to three times keeps ReadLauncherEnv bounded — each readProcInfo
	// is a `ps` shellout with a 2s ceiling.
	var ancestryTerm string
	var ancestryHostPID int
	ancestryResolved := false
	ancestry := func() (term string, hostPID int) {
		if !ancestryResolved {
			ancestryTerm, ancestryHostPID = resolveHostFromAncestry(pid)
			ancestryResolved = true
		}
		return ancestryTerm, ancestryHostPID
	}

	// kitty intentionally does not set TERM_PROGRAM (upstream kitty issue
	// #4793), so the env-captured value may be inherited from whatever
	// process launched kitty.app (e.g. a VS Code integrated terminal). When
	// KITTY_WINDOW_ID is present, kitty *is* the host of this session — but
	// we still verify via process ancestry to rule out the reverse case
	// (KITTY_WINDOW_ID leaked from a kitty shell that spawned VS Code).
	if l.KittyWindowID != "" && l.TermProgram != "kitty" {
		if term, _ := ancestry(); term == "kitty" {
			l.TermProgram = "kitty"
		}
	}
	// Hardened-runtime processes (e.g. Anthropic's signed `claude` binary)
	// hide env from sysctl. Fall back to process-ancestry walking so the UI
	// can at least bring the host app to the front. Darwin-only; other
	// platforms return "" and this is a no-op.
	if l.TermProgram == "" {
		l.TermProgram, _ = ancestry()
	}
	// Back-fill kitty fields for sessions whose own env is unreadable
	// (Apple-signed agents like `pi`, hardened-runtime binaries). If kitty
	// is the host per ancestry walk but env yielded no kitty signals,
	// derive them from kitty.app itself + its remote-control socket.
	// Without this, clicking the session in the UI raises kitty but can't
	// target the right tab — exactly the symptom reported for pi sessions
	// in issue #326.
	if l.TermProgram == "kitty" && l.KittyPID == 0 {
		if term, kpid := ancestry(); term == "kitty" && kpid > 0 {
			l.KittyPID = kpid
			if l.KittyListenOn == "" {
				l.KittyListenOn = kittyListenOnFor(kpid)
			}
			if l.KittyListenOn != "" && l.KittyWindowID == "" {
				l.KittyWindowID = kittyWindowIDForPID(l.KittyListenOn, pid)
			}
		}
	}
	// Capture the controlling TTY so Terminal.app (and potentially others)
	// can target the exact tab — Terminal.app's AppleScript dictionary
	// matches tabs by `tty` but has no session-UUID analog.
	l.TTY = processTTY(pid)
	if l.IsEmpty() {
		return nil
	}
	return l
}

// ReadArgv returns pid's argument vector (argv[0] is the executable as invoked),
// or nil when it can't be read (hardened-runtime process, already exited). It
// wraps the platform ProcessObserver so the services-layer liveness sweep can
// apply an adapter's ExcludeArgv predicate to a bound PID without importing the
// observer. Mirrors ReadLauncherEnv's contract: never blocks long, never prompts.
func ReadArgv(pid int) []string {
	if pid <= 0 {
		return nil
	}
	argv, _ := osProc.ArgvOf(pid)
	return argv
}

// processTTY is the controlling-TTY half of the host-enrichment capability;
// it is darwin-only (ps-based, osutil_darwin.go) and a no-op stub elsewhere
// (osutil_linux.go, osutil_other.go). Like the kitty/ancestry helpers, it
// enriches a session for window targeting and never gates observation.

// readProcessEnv is implemented per-platform (osutil_darwin.go,
// osutil_linux.go, osutil_other.go) and returns the whitelisted env vars
// for pid. Returns nil, nil on unsupported platforms.

// parseProcargs2 extracts the env portion of a KERN_PROCARGS2 sysctl buffer
// and returns the whitelisted entries. The buffer layout is:
//
//	int32 argc
//	NUL-terminated exec path (possibly followed by alignment padding of \0)
//	argv[0] NUL ... argv[argc-1] NUL
//	envp[0] NUL ... envp[n] NUL
//
// Modern macOS disables `ps e` envvar output, so sysctl is the only
// non-cgo / non-TCC path to read another process's env.
func parseProcargs2(buf []byte) map[string]string {
	out := map[string]string{}
	argc, p, ok := procargs2ArgvOffset(buf)
	if !ok {
		return out
	}
	// Skip argv entries.
	for i := 0; i < argc && p < len(buf); i++ {
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		if p < len(buf) {
			p++ // skip NUL
		}
	}
	// Remaining NUL-terminated strings are env entries until we hit an empty
	// string or the end of the buffer.
	for p < len(buf) {
		start := p
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		if p == start {
			break
		}
		entry := string(buf[start:p])
		if eq := strings.IndexByte(entry, '='); eq > 0 {
			key := entry[:eq]
			if _, ok := launcherEnvKeys[key]; ok {
				out[key] = entry[eq+1:]
			}
		}
		if p < len(buf) {
			p++
		}
	}
	return out
}

// procargs2ArgvOffset reads the int32 argc header of a KERN_PROCARGS2 buffer
// and skips the NUL-terminated exec path plus alignment padding, returning
// argc and the byte offset of argv[0]. ok is false when the buffer is too
// short to contain the header. Shared by parseProcargs2 (env) and
// parseProcargs2Argv (argv) so the two parsers of the same layout cannot
// drift.
func procargs2ArgvOffset(buf []byte) (argc, offset int, ok bool) {
	if len(buf) < 4 {
		return 0, 0, false
	}
	argc = int(binary.LittleEndian.Uint32(buf[:4]))
	p := 4
	// Skip exec path (NUL-terminated) and any alignment NULs before argv[0].
	for p < len(buf) && buf[p] != 0 {
		p++
	}
	for p < len(buf) && buf[p] == 0 {
		p++
	}
	return argc, p, true
}

// parseProcargs2Argv extracts the argv portion of a KERN_PROCARGS2 sysctl
// buffer (same layout as parseProcargs2 documents above). Returns nil when
// the buffer holds no argv at all — hardened-runtime processes strip it, so
// callers must treat a nil argv as "unknown", not "no args". A buffer
// truncated mid-argv (argc promises more strings than the buffer holds, e.g.
// args+env exceeding ARG_MAX) yields the partial argv that is present —
// fail-open: an exclusion predicate then sees an incomplete command line and
// treats the process as a session, the pre-filter status quo.
func parseProcargs2Argv(buf []byte) []string {
	argc, p, ok := procargs2ArgvOffset(buf)
	if !ok {
		return nil
	}
	argv := make([]string, 0, argc)
	for i := 0; i < argc && p < len(buf); i++ {
		start := p
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		argv = append(argv, string(buf[start:p]))
		if p < len(buf) {
			p++ // skip NUL
		}
	}
	if len(argv) == 0 {
		return nil
	}
	return argv
}
