//go:build darwin

package processlifecycle

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// readProcessEnv reads the exec-time env of pid via KERN_PROCARGS2 sysctl
// and returns the whitelisted entries. Modern macOS disables env visibility
// in `ps e`, so this is the only non-cgo, non-TCC path.
//
// On hardened-runtime processes (e.g. Anthropic's signed `claude` binary)
// the kernel strips argv and env from the response; the returned map is
// empty and callers fall back to resolveTermProgramFromAncestry.
func readProcessEnv(pid int) (map[string]string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, fmt.Errorf("sysctl kern.procargs2 pid %d: %w", pid, err)
	}
	return parseProcargs2(buf), nil
}

// maxAncestry caps how far up the parent-process chain we walk when
// env capture failed. Four is the typical depth for a Claude Code session
// inside VS Code's integrated terminal (claude → zsh → Code Helper → Code);
// ten gives generous headroom for tmux / SSH nesting.
const maxAncestry = 10

// termProgramByAppName maps the `.app` bundle name from a process's
// executable path to the canonical $TERM_PROGRAM string the Swift
// launcher dispatcher expects. Keys match the directory segment that
// precedes `.app/Contents/MacOS/` on macOS.
var termProgramByAppName = map[string]string{
	"iTerm":              "iTerm.app",
	"Terminal":           "Apple_Terminal",
	"Visual Studio Code": "vscode",
	"Cursor":             "cursor",
	"Windsurf":           "windsurf",
	"Ghostty":            "ghostty",
	"WezTerm":            "WezTerm",
	"Hyper":              "Hyper",
	"Warp":               "Warp",
}

// termProgramForAppPath extracts the host app's canonical TERM_PROGRAM
// value from an executable path of the form
// `/Applications/<App>.app/Contents/MacOS/<binary>`. Returns "" for paths
// without an `.app` segment or when the app isn't one of the hosts we
// know how to activate.
func termProgramForAppPath(cmdPath string) string {
	// Split on ".app/" to find the bundle boundary. The element before the
	// split contains the app's display name as its final path segment.
	idx := strings.Index(cmdPath, ".app/")
	if idx < 0 {
		return ""
	}
	head := cmdPath[:idx]
	appName := filepath.Base(head)
	return termProgramByAppName[appName]
}

// resolveTermProgramFromAncestry walks the parent-process chain of pid and
// returns the first recognized host app's TERM_PROGRAM string. Returns ""
// when no supported host appears within maxAncestry levels.
//
// Intentionally ignores tmux: tmux's env vars (TMUX, TMUX_PANE) come from
// the regular env-capture path when readable, and a tmux-only ancestor
// (without a known host terminal above it) can't be brought to the front
// by NSWorkspace.
func resolveTermProgramFromAncestry(pid int) string {
	cur := pid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		ppid, cmd, err := readProcInfo(cur)
		if err != nil {
			return ""
		}
		if term := termProgramForAppPath(cmd); term != "" {
			return term
		}
		if ppid == cur || ppid <= 1 {
			return ""
		}
		cur = ppid
	}
	return ""
}

// readProcInfo returns the parent PID and executable path of pid using a
// bounded `ps` shell-out. Same 2-second timeout pattern as the sibling
// helpers. We shell out rather than parse `kinfo_proc` from sysctl because
// ps already handles the comm-vs-argv-path distinction we need, and the
// existing package is built around these bounded exec calls.
func readProcInfo(pid int) (ppid int, cmd string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", fmt.Errorf("ps pid %d: %w", pid, err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0, "", fmt.Errorf("no process info for pid %d", pid)
	}
	// ppid is the first whitespace-separated token; everything after is the
	// command path (which may itself contain spaces, e.g. "Visual Studio Code").
	space := strings.IndexAny(line, " \t")
	if space < 0 {
		return 0, "", fmt.Errorf("unexpected ps output for pid %d: %q", pid, line)
	}
	ppid, err = strconv.Atoi(strings.TrimSpace(line[:space]))
	if err != nil {
		return 0, "", fmt.Errorf("parse ppid %q: %w", line[:space], err)
	}
	cmd = strings.TrimSpace(line[space:])
	return ppid, cmd, nil
}
