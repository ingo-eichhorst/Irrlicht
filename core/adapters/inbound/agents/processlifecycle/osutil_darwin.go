//go:build darwin

package processlifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// kittyAncestryPID walks the parent-process chain of pid and returns the PID
// of the first kitty.app ancestor, or 0 when no kitty.app appears within
// maxAncestry levels. Used to back-fill `KittyPID` for sessions whose own
// env was unreadable by sysctl — Apple-signed binaries like `pi` (Python
// signed by Apple) and zsh hide their env even from non-TCC sysctl reads,
// so KITTY_PID never makes it into the env-derived launcher. Ancestry
// walking still works because we only read ppid + comm, not env.
func kittyAncestryPID(pid int) int {
	cur := pid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		ppid, cmd, err := readProcInfo(cur)
		if err != nil {
			return 0
		}
		if termProgramForAppPath(cmd) == "kitty" {
			return cur
		}
		if ppid == cur || ppid <= 1 {
			return 0
		}
		cur = ppid
	}
	return 0
}

// kittenPath returns the absolute path of the kitten CLI, or "" if not
// found. Same candidate list as the Swift activator (KittyActivator.swift).
// Result is cached after first lookup.
var kittenPath = func() string {
	candidates := []string{
		"/Applications/kitty.app/Contents/MacOS/kitten",
		"/usr/local/bin/kitten",
		"/opt/homebrew/bin/kitten",
		os.Getenv("HOME") + "/.local/bin/kitten",
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}()

// kittyListenOnFor returns the socket path of the kitty.app at kittyPID,
// or "" if no socket is reachable. Probes the canonical `unix:/tmp/kitty-PID`
// path documented in the user-facing kitty setup snippet. Doesn't try a
// connect — just checks the socket file exists; kitten will give a clearer
// error if it's stale.
func kittyListenOnFor(kittyPID int) string {
	if kittyPID <= 0 {
		return ""
	}
	candidates := []string{
		fmt.Sprintf("/tmp/kitty-%d", kittyPID),
		fmt.Sprintf("/private/tmp/kitty-%d", kittyPID),
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return "unix:" + p
		}
	}
	return ""
}

// kittyWindowIDForPID queries kitty's remote-control socket and returns the
// id of the kitty-window whose foreground_processes include sessionPID, or
// "" when no match is found (or kitten fails). Used to back-fill
// KittyWindowID for sessions whose own env didn't expose KITTY_WINDOW_ID
// (e.g., the pi adapter — pi's env is unreadable via sysctl). Bounded
// 2-second timeout; runs at session-birth so latency is acceptable.
func kittyWindowIDForPID(socket string, sessionPID int) string {
	if kittenPath == "" || socket == "" || sessionPID <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, kittenPath, "@", "--to", socket, "ls").Output()
	if err != nil {
		return ""
	}
	var osWindows []struct {
		Tabs []struct {
			Windows []struct {
				ID                  int `json:"id"`
				PID                 int `json:"pid"`
				ForegroundProcesses []struct {
					PID int `json:"pid"`
				} `json:"foreground_processes"`
			} `json:"windows"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal(out, &osWindows); err != nil {
		return ""
	}
	for _, w := range osWindows {
		for _, t := range w.Tabs {
			for _, kw := range t.Windows {
				if kw.PID == sessionPID {
					return strconv.Itoa(kw.ID)
				}
				for _, fg := range kw.ForegroundProcesses {
					if fg.PID == sessionPID {
						return strconv.Itoa(kw.ID)
					}
				}
			}
		}
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
