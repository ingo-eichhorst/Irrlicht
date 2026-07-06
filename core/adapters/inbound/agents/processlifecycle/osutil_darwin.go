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
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"irrlicht/core/pkg/pathutil"
)

// psPath and plutilPath are resolved once from a fixed set of trusted
// directories rather than trusted PATH, per go:S4036.
var (
	psPath     = pathutil.MustResolve("ps")
	plutilPath = pathutil.MustResolve("plutil")
)

// processTTY returns the controlling TTY of pid in the form "/dev/ttysNNN",
// or "" if the process has no controlling terminal (hardened-runtime
// children often don't) or the ps lookup fails. The result is normalized
// to match Terminal.app's AppleScript `tty` property format — `ps -o tty=`
// on macOS omits the "/dev/" prefix that AppleScript returns. This is host
// enrichment (window targeting), not observation, so other platforms stub it.
func processTTY(pid int) string {
	if pid <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, psPath, "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
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

// resolveHostFromAncestry walks the parent-process chain of pid and returns
// both the first recognized host app's TERM_PROGRAM string and the PID at
// which it was found. Returns ("", 0) when no supported host appears within
// maxAncestry levels.
//
// Intentionally ignores tmux: tmux's env vars (TMUX, TMUX_PANE) come from
// the regular env-capture path when readable, and a tmux-only ancestor
// (without a known host terminal above it) can't be brought to the front
// by NSWorkspace.
func resolveHostFromAncestry(pid int) (termProgram string, hostPID int) {
	cur := pid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		ppid, cmd, err := readProcInfo(cur)
		if err != nil {
			return "", 0
		}
		if term := termProgramForAppPath(cmd); term != "" {
			return term, cur
		}
		if ppid == cur || ppid <= 1 {
			return "", 0
		}
		cur = ppid
	}
	return "", 0
}

// bundleIDForAppPath returns the CFBundleIdentifier of the application bundle
// at appPath (".../<App>.app"), or "" when it can't be read. Uses `plutil`,
// which ships with macOS and reads both XML and binary Info.plists; bundles
// under /Applications are world-readable so this needs no TCC consent. Same
// bounded 2-second exec ceiling as the sibling ps helpers.
func bundleIDForAppPath(appPath string) string {
	if appPath == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	plist := appPath + "/Contents/Info.plist"
	out, err := exec.CommandContext(ctx, plutilPath, "-extract", "CFBundleIdentifier", "raw", "-o", "-", plist).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveHostBundleIDFromAncestry walks the parent-process chain of pid and
// returns the CFBundleIdentifier of the first top-level application bundle it
// finds, plus that app's PID. It is the generic fallback used when the curated
// termProgramByAppName map matches no ancestor — it lets the UI bring an
// embedded-terminal GUI host (e.g. Obsidian) to the front without a per-app
// registry entry. Returns ("", 0) when no top-level app appears within
// maxAncestry levels.
//
// The walk starts at pid's *parent*: the agent runs inside the host and is
// never the host itself, and an agent whose own binary lives in a top-level
// bundle (e.g. ClaudeCode.app) must not be mistaken for it.
func resolveHostBundleIDFromAncestry(pid int) (bundleID string, hostPID int) {
	ppid, _, err := readProcInfo(pid)
	if err != nil || ppid <= 1 {
		return "", 0
	}
	cur := ppid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		pp, cmd, err := readProcInfo(cur)
		if err != nil {
			return "", 0
		}
		if appPath := topLevelAppPath(cmd); appPath != "" {
			if bid := bundleIDForAppPath(appPath); bid != "" {
				return bid, cur
			}
		}
		if pp == cur || pp <= 1 {
			return "", 0
		}
		cur = pp
	}
	return "", 0
}

// IsKnownInteractiveHost reports whether pid's process ancestry traces back to
// a recognized terminal emulator or IDE (the curated termProgramByAppName map,
// via resolveHostFromAncestry), or to another host known to embed a real
// terminal (knownEmbeddedHostBundleIDs, e.g. Obsidian). It gates session
// admission for adapters whose process can be spawned non-interactively by
// unrelated tooling — issue #784, where a third-party menu-bar app (CodexBar)
// kept an Antigravity `agy` CLI process running in the background for quota
// polling, with no distinguishing argv or cwd.
//
// Unlike resolveHostBundleIDFromAncestry (which accepts ANY top-level app, for
// click-to-focus purposes where bringing an unrecognized host forward is still
// useful), this is a real allow-list: an ancestor that is a legitimate `.app`
// but isn't a curated terminal/IDE and isn't in knownEmbeddedHostBundleIDs
// returns false — which is exactly what excludes CodexBar.
func IsKnownInteractiveHost(pid int) bool {
	// Short-circuit before the second ancestry walk: resolveHostFromAncestry
	// and resolveHostBundleIDFromAncestry each independently re-walk the same
	// parent chain via their own ps shellouts, so skip the bundle-ID walk
	// entirely once the curated map already matched.
	term, _ := resolveHostFromAncestry(pid)
	if term != "" {
		return true
	}
	bundleID, _ := resolveHostBundleIDFromAncestry(pid)
	return isKnownInteractiveHostFrom(term, bundleID)
}

// isKnownInteractiveHostFrom is the pure decision behind IsKnownInteractiveHost,
// split out so the allow-list logic can be tested with synthetic ancestry
// results instead of depending on whatever launched the test binary.
func isKnownInteractiveHostFrom(termProgram, bundleID string) bool {
	return termProgram != "" || knownEmbeddedHostBundleIDs[bundleID]
}

// resolveTermProgramFromAncestry is a thin wrapper that discards the host
// PID. Kept for the existing call site that only cares whether kitty (or any
// other host) appears in the chain; callers that also need the host PID
// should use resolveHostFromAncestry directly to avoid a second walk.
func resolveTermProgramFromAncestry(pid int) string {
	term, _ := resolveHostFromAncestry(pid)
	return term
}

// kittyAncestryPID is a thin wrapper returning only the kitty.app PID from
// the ancestry walk, or 0 when kitty is not the host. Used to back-fill
// `KittyPID` for sessions whose own env was unreadable by sysctl —
// Apple-signed binaries like `pi` (Python signed by Apple) and zsh hide
// their env even from non-TCC sysctl reads, so KITTY_PID never makes it
// into the env-derived launcher. Ancestry walking still works because we
// only read ppid + comm, not env.
func kittyAncestryPID(pid int) int {
	term, hostPID := resolveHostFromAncestry(pid)
	if term != "kitty" {
		return 0
	}
	return hostPID
}

// kittenPath returns the absolute path of the kitten CLI, or "" if not
// found. Resolved once at package init; the daemon does not pick up newly
// installed kitten without a restart.
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

// kittySocketCandidates returns the filesystem paths a kitty.app at kittyPID
// might have bound its remote-control socket to, given the canonical
// `listen_on unix:/tmp/kitty-{kitty_pid}` config documented in the user-facing
// setup snippet. Both `/tmp` and `/private/tmp` are listed because macOS
// symlinks the former to the latter and either spelling may appear in
// filesystem listings depending on how kitty resolved it at bind time.
func kittySocketCandidates(kittyPID int) []string {
	if kittyPID <= 0 {
		return nil
	}
	return []string{
		fmt.Sprintf("/tmp/kitty-%d", kittyPID),
		fmt.Sprintf("/private/tmp/kitty-%d", kittyPID),
	}
}

// kittyListenOnFor returns the socket path of the kitty.app at kittyPID, or
// "" if no socket is reachable.
//
// Security: `/tmp` is world-writable, so a malicious local process could
// pre-plant a unix socket at `/tmp/kitty-{PID}` before kitty itself binds.
// We require the socket file's owner UID to match the current user — kitty
// binds with its own credentials, so a foreign-owned socket at that path is
// either stale or hostile; either way, we skip it.
func kittyListenOnFor(kittyPID int) string {
	myUID := uint32(os.Getuid())
	for _, p := range kittySocketCandidates(kittyPID) {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != myUID {
			continue
		}
		return "unix:" + p
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
	return parseKittenLsForPID(out, sessionPID)
}

// parseKittenLsForPID parses a `kitten @ ls` JSON response and returns the
// id (as a decimal string) of the kitty-window whose `pid` or
// `foreground_processes[].pid` matches sessionPID, or "" if no match.
// Exposed as a separate function so the JSON-handling can be unit-tested
// without spawning a real kitty.
func parseKittenLsForPID(out []byte, sessionPID int) string {
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
	out, err := exec.CommandContext(ctx, psPath, "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
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
