//go:build darwin

package processlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"irrlicht/core/domain/session"
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
// complete reports whether the walk reached that verdict on its own terms —
// it found a host, ran out of chain, or ran out of depth — rather than
// aborting because a `ps` could not be answered. Those two produce the same
// ("", 0) and are different facts: the first is evidence that this process has
// no local window, the second is no evidence at all. Every abort is a
// readProcInfo failure, which on a loaded machine is that helper's 2s ceiling
// (#1492); the caller that acts on the distinction is
// resolveClientHostIdentity.
//
// Intentionally ignores tmux: tmux's env vars (TMUX, TMUX_PANE) come from
// the regular env-capture path when readable, and a tmux-only ancestor
// (without a known host terminal above it) can't be brought to the front
// by NSWorkspace.
func resolveHostFromAncestry(pid int) (termProgram string, hostPID int, complete bool) {
	cur := pid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		ppid, cmd, err := readProcInfo(cur)
		if err != nil {
			return "", 0, false
		}
		if term := termProgramForAppPath(cmd); term != "" {
			return term, cur, true
		}
		if ppid == cur || ppid <= 1 {
			return "", 0, true
		}
		cur = ppid
	}
	return "", 0, true
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
// finds, plus that app's PID. complete carries the same meaning as
// resolveHostFromAncestry's, and the first read is split out of the `ppid <= 1`
// guard for exactly that reason: "pid's parent is init" is a verdict, "pid
// could not be read" is not. It is the generic fallback used when the curated
// termProgramByAppName map matches no ancestor — it lets the UI bring an
// embedded-terminal GUI host (e.g. Obsidian) to the front without a per-app
// registry entry. Returns ("", 0) when no top-level app appears within
// maxAncestry levels.
//
// The walk starts at pid's *parent*: the agent runs inside the host and is
// never the host itself, and an agent whose own binary lives in a top-level
// bundle (e.g. ClaudeCode.app) must not be mistaken for it.
func resolveHostBundleIDFromAncestry(pid int) (bundleID string, hostPID int, complete bool) {
	ppid, _, err := readProcInfo(pid)
	if err != nil {
		return "", 0, false
	}
	if ppid <= 1 {
		return "", 0, true
	}
	cur := ppid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		pp, cmd, err := readProcInfo(cur)
		if err != nil {
			return "", 0, false
		}
		if appPath := topLevelAppPath(cmd); appPath != "" {
			if bid := bundleIDForAppPath(appPath); bid != "" {
				return bid, cur, true
			}
		}
		if pp == cur || pp <= 1 {
			return "", 0, true
		}
		cur = pp
	}
	return "", 0, true
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
	term, _, _ := resolveHostFromAncestry(pid)
	if term != "" {
		return true
	}
	bundleID, _, _ := resolveHostBundleIDFromAncestry(pid)
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
	term, _, _ := resolveHostFromAncestry(pid)
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
	term, hostPID, _ := resolveHostFromAncestry(pid)
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

// kittyLsWindow is one entry of a `kitten @ ls` response's tabs[].windows[].
type kittyLsWindow struct {
	ID                  int `json:"id"`
	PID                 int `json:"pid"`
	ForegroundProcesses []struct {
		PID int `json:"pid"`
	} `json:"foreground_processes"`
}

// kittyLsTab is one entry of a `kitten @ ls` response's os_windows[].tabs[].
type kittyLsTab struct {
	Windows []kittyLsWindow `json:"windows"`
}

// kittyLsOSWindow is one top-level entry of a `kitten @ ls` JSON response.
type kittyLsOSWindow struct {
	Tabs []kittyLsTab `json:"tabs"`
}

// parseKittenLsForPID parses a `kitten @ ls` JSON response and returns the
// id (as a decimal string) of the kitty-window whose `pid` or
// `foreground_processes[].pid` matches sessionPID, or "" if no match.
// Exposed as a separate function so the JSON-handling can be unit-tested
// without spawning a real kitty.
func parseKittenLsForPID(out []byte, sessionPID int) string {
	var osWindows []kittyLsOSWindow
	if err := json.Unmarshal(out, &osWindows); err != nil {
		return ""
	}
	for _, w := range osWindows {
		for _, t := range w.Tabs {
			if id := findKittyWindowIDForPID(t.Windows, sessionPID); id != "" {
				return id
			}
		}
	}
	return ""
}

// findKittyWindowIDForPID scans one tab's windows for one whose own pid or
// any foreground_processes[].pid matches sessionPID, returning its window id
// (decimal string), or "" if none match.
func findKittyWindowIDForPID(windows []kittyLsWindow, sessionPID int) string {
	for _, kw := range windows {
		if kw.PID == sessionPID {
			return strconv.Itoa(kw.ID)
		}
		for _, fg := range kw.ForegroundProcesses {
			if fg.PID == sessionPID {
				return strconv.Itoa(kw.ID)
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

// herdrClientPIDs returns the PIDs of every herdr client currently attached to
// the session addressed by socketPath, newest attach first, and whether the
// probe actually ran. A detached session yields (nil, true) — verified live
// against herdr 0.8.0, where a session with no client has no writer on its
// client log, and one with two clients attached (herdr supports attaching from
// more than one place) has two.
//
// The second return value is the whole of #1485. "Nobody is attached" and "I
// could not look" are different facts, and a caller that merges this answer
// into a host it already has must be able to tell them apart: the first is
// evidence, the second is not. Collapsing them was harmless while the host was
// resolved once and never revisited, and became a defect when #1405 made the
// liveness sweep re-resolve it — a probe that fails to run then overwrites a
// good host with nothing.
//
// Only writers count. "Holds the log open" is not the predicate: a `tail -f`
// reader is not a client, and adopting its terminal as the host of every
// session on that server would be the #1348 misroute with a new cause — and,
// being the newer process, it would sort first. Hence the FD-column filter,
// which is also why this cannot use `lsof -t` (that form drops the column).
//
// Ordering answers open question 3 of #1350: the most recently attached client
// is the window the user most recently chose to view the session in, and PIDs
// are allocated ascending, so descending PID is "newest first". lsof matches
// by device and inode rather than by string, so a symlinked path (a macOS
// t.TempDir() lives under /var -> /private/var) needs no canonicalisation here.
func herdrClientPIDs(socketPath string) (pids []int, probed bool) {
	logPath := herdrClientLogPath(socketPath)
	if logPath == "" {
		return nil, false
	}
	// Stat before probing, because lsof answers exit 1 for BOTH "the file
	// exists and nobody holds it open" and "there is no such file" (measured,
	// lsof 4.91: the second also writes a "status error on <path>" line to
	// stderr, which .Output() discards). Only the first is a detach. Without
	// this, a client log that resolves to NOTHING — herdr moving or renaming
	// it between releases — reads as a permanent, self-consistent "nobody is
	// attached" rather than as a probe that never reached the question.
	//
	// It covers only that case, and deliberately not the neighbouring one: a
	// socket path addressing a *different, real* herdr session resolves to a
	// client log that exists, so the stat passes and lsof honestly reports no
	// holders. Verified live against herdr 0.8.0 — a detached session keeps
	// its client log (~/.config/herdr/sessions/factory/herdr-client.log, 505
	// bytes, no holders), so file existence cannot separate "detached" from
	// "probed somebody else's session". Only deriving the path correctly can.
	if _, err := os.Stat(logPath); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, lsofPath, logPath).Output()
	if !lsofProbeRan(err) {
		return nil, false
	}
	pids = herdrClientWriters(string(out), os.Getpid())
	sort.Sort(sort.Reverse(sort.IntSlice(pids)))
	return pids, true
}

// lsofProbeRan reports whether an lsof invocation that returned err
// nevertheless ran and answered. Exit 1 is lsof's "nothing to report", which —
// on a path herdrClientPIDs has already confirmed exists — is the detached
// case and not an error. Every other failure is a probe that did not run: the
// 2s deadline above, a fork failure, a signal (a process killed under an
// exhausted CPU limit exits 152, measured on this machine), a missing binary.
// None of them carry information about who is attached, and returning them as
// "detached" is #1485.
//
// Split out so the classification is testable without arranging a real lsof
// failure — its table of committed cases is the evidence that a revert to
// `if err != nil` would be caught. runPgrep (process_darwin.go) draws the same
// distinction for pgrep's exit 1.
func lsofProbeRan(err error) bool {
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

// herdrClientWriters selects the client PIDs from an lsof table. 'u' counts
// alongside 'w': it is read/write, which a client's log handle may legitimately
// be, whereas a plain 'r' reader never is. Split out so the rule is testable
// without a live herdr.
func herdrClientWriters(out string, self int) []int {
	var pids []int
	for _, e := range parseLsofFDs(out, self) {
		if mode := e.Mode(); mode == 'w' || mode == 'u' {
			pids = append(pids, e.PID)
		}
	}
	return pids
}

// maxClientCandidates bounds how many attached clients are probed for a
// resolvable host. Each candidate costs an env read plus an ancestry walk, and
// attaching more than a handful of clients to one session is not a real
// workflow, so the cap keeps that work proportionate.
const maxClientCandidates = 4

// resolveClientHostIdentity picks the host-window identity of the first
// candidate in pids that reports one. It is the shared "which window is this
// multiplexer session displayed in" loop: herdr feeds it the writers of the
// session's client log (resolveHerdrClientLauncher), and #1501's tmux path is
// queued to feed it `tmux list-clients -F '#{client_pid}'`.
//
// The second return is the same tri-state herdrClientPIDs draws one layer
// down: true means the answer is evidence, false means the candidates could
// not be read and the caller must not treat an absent host as a host that is
// gone.
//
// #1492 is the whole of that second return. "This candidate has no local GUI
// window" and "this candidate's identity could not be READ" arrive at the same
// place — an empty TermProgram and an empty HostBundleID — and only the first
// is evidence. The first is real and must stay an answer: an SSH client has a
// real tty and no local window, and reporting one anyway is the misroute #1348
// removed. The second is not: kitty sets no TERM_PROGRAM (upstream kitty
// #4793), so for a client attached from kitty the ancestry walk is the ONLY
// source of a host, and that walk is a `ps` chain under a 2s ceiling. On a
// loaded machine it times out, the candidate reports nothing, and answering
// "detached" makes AdoptHostIdentity clear TermProgram / HostBundleID /
// ITermSessionID / the kitty selectors from a session whose client is attached
// right now.
//
// So an unreadable candidate poisons the negative answer rather than
// contributing to it: "no attached client has a local window" is a claim about
// every candidate, and one that could not be read cannot be vouched for by the
// others. A candidate that DOES resolve still wins outright, because a found
// host is evidence regardless of what the rest of the list did.
func resolveClientHostIdentity(pids []int) (*session.Launcher, bool) {
	if len(pids) > maxClientCandidates {
		pids = pids[:maxClientCandidates]
	}
	unreadable := false
	for _, pid := range pids {
		host, complete := hostIdentity(pid)
		if host.HerdrPaneID != "" {
			// A candidate that is itself a multiplexer pane has no window of
			// its own — its host is one more indirection away. Don't recurse;
			// try the next candidate.
			continue
		}
		if host.TermProgram == "" && host.HostBundleID == "" {
			unreadable = unreadable || !complete
			continue
		}
		return host, true
	}
	return nil, !unreadable
}

// herdrClientLauncher resolves the host-window identity of the herdr session
// addressed by socketPath, by reading it from the attached client exactly the
// way it would be read from any directly-hosted agent (hostIdentity). Returns
// (nil, true) when nothing is attached, or when every attached client was read
// and none has a local GUI host — an SSH client has a real tty but no local
// window, and reporting one anyway is the misroute #1348 removed. (nil, false)
// is the third state, "I could not look": either the lsof probe did not run
// (#1485) or a candidate's own identity could not be read (#1492).
//
// "Was read and has no host" rather than "reported no host" is the whole of
// #1492, and the two are not the same claim. hostIdentity swallows its env
// read's failure by design — the ProcessObserver port defines an unreadable env
// as an empty map, and the ancestry fallback is the only signal a
// hardened-runtime process has — so the distinction is drawn where it can be:
// resolveHostFromAncestry and resolveHostBundleIDFromAncestry now report
// whether their walk reached a verdict or aborted on a readProcInfo failure,
// hostIdentity carries that out as its second return, and
// resolveClientHostIdentity refuses to call the negative an answer when any
// candidate could not be read. Before that, a client attached from kitty (which
// sets no TERM_PROGRAM upstream, so ancestry is its only source) yielded no host
// on a loaded machine and was indistinguishable here from an SSH client.
//
// Only ever called with a socket path the daemon captured from the pane's own
// $HERDR_SOCKET_PATH, so a resolved identity always accompanies a complete
// herdr address; control keeps routing to herdr (resolveBackend requires both
// Herdr fields and tests them first) rather than to any tmux/kitty identity
// adopted from the client.
//
// Results are memoized briefly because every pane of one herdr server shares a
// socket, and the startup seed resolves them back to back, synchronously: an
// lsof scan costs ~0.3s on a quiet machine, so eight panes would otherwise pay
// eight identical scans before the daemon starts serving.
func herdrClientLauncher(socketPath string) (*session.Launcher, bool) {
	if socketPath == "" {
		return nil, false
	}
	if cached, ok := herdrClientCacheGet(socketPath); ok {
		return cached, true
	}
	resolved, probed := resolveHerdrClientLauncher(socketPath)
	if !probed {
		// Never memoize a non-answer. The cache exists to collapse one startup
		// seed's worth of identical scans; caching "unknown" would instead
		// spread a single failed probe across every session that shares this
		// socket for the next 5 seconds, which is the opposite of what the
		// tri-state buys.
		return nil, false
	}
	herdrClientCachePut(socketPath, resolved)
	return resolved, true
}

func resolveHerdrClientLauncher(socketPath string) (*session.Launcher, bool) {
	pids, probed := herdrClientPIDs(socketPath)
	if !probed {
		return nil, false
	}
	return resolveClientHostIdentity(pids)
}

// herdrClientCacheTTL is short enough that attaching a client is picked up by
// the next session bound after it, and long enough to collapse one startup
// seed's worth of repeats into a single scan.
const herdrClientCacheTTL = 5 * time.Second

type herdrClientCacheEntry struct {
	launcher *session.Launcher
	at       time.Time
}

var (
	herdrClientCacheMu sync.Mutex
	herdrClientCache   = map[string]herdrClientCacheEntry{}
)

// herdrClientCacheGet reports the cached identity for socketPath. A cached
// "nothing attached" is a real answer and returns (nil, true); ok is false only
// when there is no live entry. Expired entries are dropped on the way past, so
// sockets that stop being used don't accumulate.
func herdrClientCacheGet(socketPath string) (*session.Launcher, bool) {
	herdrClientCacheMu.Lock()
	defer herdrClientCacheMu.Unlock()
	entry, ok := herdrClientCache[socketPath]
	if !ok {
		return nil, false
	}
	if time.Since(entry.at) > herdrClientCacheTTL {
		delete(herdrClientCache, socketPath)
		return nil, false
	}
	return entry.launcher, true
}

func herdrClientCachePut(socketPath string, l *session.Launcher) {
	herdrClientCacheMu.Lock()
	defer herdrClientCacheMu.Unlock()
	herdrClientCache[socketPath] = herdrClientCacheEntry{launcher: l, at: time.Now()}
}
