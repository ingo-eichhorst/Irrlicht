// Package processlifecycle owns the full process lifecycle for agent sessions:
// birth detection (polling) and death detection (kqueue). It unifies the
// previously separate processscanner and process/watcher packages, deduplicating
// shared OS utilities (pgrep, lsof, CWD resolution).
package processlifecycle

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/session"
)

// findProcesses returns PIDs of processes whose name exactly matches name
// (uses pgrep -x for exact binary name match).
func findProcesses(name string) ([]int, error) {
	return runPgrep("-x", name)
}

// findProcessesByCmdLine returns PIDs of processes whose full command line
// matches the regex pattern (uses pgrep -f). Used for agents whose process
// name on disk doesn't match their CLI name — e.g. Python tools launched
// via a wrapper, where the OS process is `python` and the agent script is
// in argv[1]. The pattern is interpreted by pgrep, which uses extended
// regex on macOS / basic regex on Linux.
func findProcessesByCmdLine(pattern string) ([]int, error) {
	// Exclude our own pgrep call from matching itself when the pattern
	// happens to be a substring of pgrep's argv. Filter the result.
	ownPID := strconv.Itoa(os.Getpid())
	pids, err := runPgrep("-f", pattern)
	if err != nil {
		return nil, err
	}
	out := pids[:0]
	for _, p := range pids {
		if strconv.Itoa(p) == ownPID {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// runPgrep invokes pgrep with the given flag and pattern, parses the PIDs
// from stdout, and returns nil for the no-match (exit 1) case.
func runPgrep(flag, pattern string) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pgrep", flag, pattern).Output()
	if err != nil {
		// pgrep exits 1 when there are no matches — not an error.
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// processCWD returns the working directory of pid using lsof.
func processCWD(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof cwd pid %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}
	return "", fmt.Errorf("cwd not found for pid %d", pid)
}

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
	"TERM_PROGRAM":     {},
	"ITERM_SESSION_ID": {},
	"TERM_SESSION_ID":  {},
	"TMUX":             {},
	"TMUX_PANE":        {},
	"VSCODE_PID":       {},
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

// processTTY returns the controlling TTY of pid in the form "/dev/ttysNNN",
// or "" if the process has no controlling terminal (hardened-runtime
// children often don't) or the ps lookup fails. The result is normalized
// to match Terminal.app's AppleScript `tty` property format — `ps -o tty=`
// on macOS omits the "/dev/" prefix that AppleScript returns.
func processTTY(pid int) string {
	if pid <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	tty := strings.TrimSpace(string(out))
	if tty == "" || tty == "?" || tty == "??" || tty == "-" {
		return ""
	}
	if !strings.HasPrefix(tty, "/dev/") {
		tty = "/dev/" + tty
	}
	return tty
}

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

