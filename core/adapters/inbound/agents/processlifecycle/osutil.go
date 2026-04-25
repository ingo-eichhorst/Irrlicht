// Package processlifecycle owns the full process lifecycle for agent sessions:
// birth detection (polling) and death detection (kqueue on darwin, polling
// elsewhere). It unifies the previously separate processscanner and
// process/watcher packages, deduplicating shared OS utilities (process
// enumeration, cwd, env capture).
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
	env, _ := readProcessEnv(pid)

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
	// Hardened-runtime processes (e.g. Anthropic's signed `claude` binary)
	// hide env from sysctl. Fall back to process-ancestry walking so the UI
	// can at least bring the host app to the front. Darwin-only; other
	// platforms return "" and this is a no-op.
	if l.TermProgram == "" {
		l.TermProgram = resolveTermProgramFromAncestry(pid)
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

// readProcessEnv is implemented per-platform (osutil_darwin.go,
// osutil_linux.go, osutil_windows.go, osutil_other.go) and returns the
// whitelisted env vars for pid. Returns nil, nil on unsupported platforms.

// findProcesses, processCWD, processTTY, and PidAlive are implemented in
// per-platform files (osutil_unix.go on darwin/linux, osutil_windows.go on
// windows). The Windows path uses CreateToolhelp32Snapshot + OpenProcess
// instead of pgrep/lsof/ps shell-outs.

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
	if len(buf) < 4 {
		return out
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	p := 4
	// Skip exec path (NUL-terminated) and any alignment NULs before argv[0].
	for p < len(buf) && buf[p] != 0 {
		p++
	}
	for p < len(buf) && buf[p] == 0 {
		p++
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
