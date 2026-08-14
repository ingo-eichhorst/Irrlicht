//go:build darwin

package processlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
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
//
// It honours the completeness bit both walks report (#1492) and fails OPEN on a
// walk that could not be completed (#1513). An aborted walk and a genuine miss
// both resolve to "", and they are different facts: the second is the #784
// evidence itself, the first is no evidence at all.
//
// The direction is the opposite of #1492's for the same bit, because this call
// site is an ADMISSION gate rather than a click target: a false answer here does
// not degrade an enrichment, it declines a legitimate session outright. It
// declines it permanently, too — SessionDetector caches the rejection in
// hostGateRejected, checks that cache ahead of the gate, and never evicts from
// it — so one slow `ps` cost the user a session for the lifetime of the daemon.
// Failing open is what core/pkg/cliversion already does for a CLI version it
// cannot read, what PIDManager.AllowsSession (this function's only caller)
// already does for a PID it cannot discover, and what this function's own
// linux/other stubs already do unconditionally.
//
// A COMPLETED walk that found no allow-listed host still rejects: that is #784
// and it is unchanged.
//
// "Complete" is exactly "no readProcInfo call failed", which is narrower than
// "every probe in the walk was answered": resolveHostBundleIDFromAncestry also
// shells out to plutil via bundleIDForAppPath, and a plutil that blows its own
// 2s ceiling yields an empty bundle id indistinguishable from an ancestor that
// is not an app at all. That residue is #1524, not something this bit reports.
func IsKnownInteractiveHost(pid int) bool {
	return isKnownInteractiveHostVia(pid, resolveHostFromAncestry, resolveHostBundleIDFromAncestry)
}

// ancestryWalk is the shape resolveHostFromAncestry and
// resolveHostBundleIDFromAncestry share: a host string, the PID it was found
// at, and whether the walk reached its verdict rather than aborting.
type ancestryWalk func(pid int) (host string, hostPID int, complete bool)

// isKnownInteractiveHostVia is IsKnownInteractiveHost with both walks injected,
// so the ORDER between them is testable — the pure isKnownInteractiveHostFrom
// below cannot see it, and no arrangement of live processes can drive the two
// walks to different verdicts on purpose.
//
// The `complete` half of the short-circuit is load-bearing, not an
// optimization, and the asymmetry between the two allow-lists is why: walk 1
// recognizes every curated terminal and IDE by app name (termProgramByAppName),
// while walk 2 recognizes exactly the hosts in knownEmbeddedHostBundleIDs —
// today one entry, md.obsidian. So walk 2 can CONFIRM an embedded host and can
// never rule out a curated one. Letting it answer alone after walk 1 aborted
// would reject every iTerm, VS Code, kitty and JetBrains session whose first
// walk timed out — #1513 again, one walk over, and the very sessions this gate
// exists to admit.
//
// The consequence, stated plainly because it is the trade and not an accident:
// a walk 1 that aborts TRANSIENTLY (its ps over the 2s ceiling, rather than an
// unreadable process) admits without re-probing, where a second walk might have
// completed and found CodexBar. That is the deliberate polarity — a re-probe
// could only move the answer toward rejection, on evidence this gate has
// already decided not to trust.
func isKnownInteractiveHostVia(pid int, walkTerm, walkBundle ancestryWalk) bool {
	term, _, complete := walkTerm(pid)
	bundleID := ""
	if term == "" && complete {
		// Only reached when walk 1 ran to a verdict, so this also skips the
		// duplicate ps shellouts whenever the curated map already matched.
		bundleID, _, complete = walkBundle(pid)
	}
	return isKnownInteractiveHostFrom(term, bundleID, complete)
}

// isKnownInteractiveHostFrom is the pure decision behind IsKnownInteractiveHost,
// split out so the allow-list logic can be tested with synthetic ancestry
// results instead of depending on whatever launched the test binary.
//
// complete is checked first and on its own: when the walk aborted, termProgram
// and bundleID are "" for a reason that says nothing about the host, so reading
// the allow-list at all would be reading a value that was never observed.
func isKnownInteractiveHostFrom(termProgram, bundleID string, complete bool) bool {
	if !complete {
		return true
	}
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
	return sortedDistinctPIDs(herdrClientWriters(string(out), os.Getpid())), true
}

// sortedDistinctPIDs sorts pids newest-attach-first and collapses repeats.
// Sorts in place and reuses the argument's backing array — slices.Compact's
// documented contract — so the result aliases pids. Fine for the single
// production caller, which passes a slice herdrClientWriters just built and
// does not look at it again.
//
// The sort answers open question 3 of #1350; the dedup is what makes the
// result a list of CLIENTS rather than of file descriptors. parseLsofFDs emits
// one entry per FD row by design, so a client holding the client log on two
// write handles is reported twice — and every consumer downstream treats the
// list as one entry per client: resolveClientHostIdentity would run the same
// two ancestry walks twice for it, and maxClientCandidates, whose doc says it
// "bounds how many attached clients are probed", would count it twice against
// the cap and so reach its truncation at three clients instead of five.
//
// Unobserved on herdr 0.8.0 — herdrClientPIDs' own doc records a live check
// where two attached clients produced exactly two writers — so this is a
// latent dependency on herdr's FD layout being one handle per client, not a
// defect today. It is closed here rather than left standing because the cap's
// truncation is one of the two triggers of #1514, and a duplicate is a way to
// reach it that has nothing to do with how many clients are attached.
//
// Deduping BEFORE the cap therefore also moves resolveClientHostIdentity's
// readAll from false to true in those cases, which is a change in the
// host-CLEARING direction and so deserves saying out loud. It is the honest
// direction: the cap poisons the answer because a dropped candidate is one we
// declined to look at, and a second FD row for a candidate we DID look at is
// not that. Removing it removes a false claim of incompleteness, it does not
// weaken #1492 — a candidate genuinely dropped by the cap still poisons.
func sortedDistinctPIDs(pids []int) []int {
	slices.Sort(pids)
	pids = slices.Compact(pids)
	slices.Reverse(pids)
	return pids
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
//
// The maxClientCandidates truncation poisons it too, and for the same reason: a
// candidate dropped by the cap is one we declined to look at. It is a weaker
// case — herdrClientPIDs sorts newest-attach first, so the dropped candidates
// are the stale ones — but "every attached client was read" is exactly the
// claim the cap makes false, and answering it anyway is #1492 arriving through
// the cap instead of through a timeout.
//
// Two residual false negatives survive, both of them "the walk reached a
// verdict" cases rather than aborts, so the bit above cannot express them:
// a candidate inside a tmux pane walks to the reparented tmux server and
// terminates honestly at PID 1 while a local window exists (that indirection is
// #1501, the tmux twin of #1350), and a chain deeper than maxAncestry is
// declared a miss on the same terms.
// Producers must feed it DISTINCT pids. maxClientCandidates is a bound on
// clients, and the truncation below poisons the answer, so a repeated pid both
// burns a slot and costs a second identical hostIdentity. herdr's producer
// dedups (sortedDistinctPIDs) because lsof emits a row per FD; a `tmux
// list-clients` producer is naturally distinct, but the obligation is stated
// here, next to the constant that depends on it, rather than only there.
func resolveClientHostIdentity(pids []int) (*session.Launcher, bool) {
	readAll := len(pids) <= maxClientCandidates
	if !readAll {
		pids = pids[:maxClientCandidates]
	}
	for _, pid := range pids {
		host, complete := hostIdentity(pid)
		if host.HerdrPaneID != "" {
			// A candidate that is itself a multiplexer pane has no window of
			// its own — its host is one more indirection away. Don't recurse;
			// try the next candidate. Unlike the cap above, declining here does
			// NOT poison the answer: refusing to recurse is a standing policy
			// rather than a failed read, and treating it as "unknown" would mean
			// a nested setup's stale host could never be cleared at all.
			continue
		}
		if host.TermProgram == "" && host.HostBundleID == "" {
			readAll = readAll && complete
			continue
		}
		return host, true
	}
	return nil, readAll
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
// #1492; resolveClientHostIdentity's doc carries the argument, and this
// function only passes its verdict through.
//
// Only ever called with a socket path the daemon captured from the pane's own
// $HERDR_SOCKET_PATH, so a resolved identity always accompanies a complete
// herdr address; control keeps routing to herdr (resolveBackend requires both
// Herdr fields and tests them first) rather than to any tmux/kitty identity
// adopted from the client.
//
// Every outcome is memoized briefly, non-answers included, because every pane
// of one herdr server shares a socket and both the startup seed and the
// liveness sweep resolve them one after another, synchronously: an lsof scan
// costs ~0.3s on a quiet machine, so eight panes would otherwise pay eight
// identical scans before the daemon starts serving.
//
// Memoizing the non-answer REVERSES #1485's rule, which was "never memoize a
// non-answer" on the grounds that "caching 'unknown' would spread a single
// failed probe across every session that shares this socket for the next 5
// seconds, which is the opposite of what the tri-state buys". Both halves of
// that reason stopped holding, and the replacement rule is below (#1514):
//
//   - Spreading an unknown is INERT, so it is not the opposite of what the
//     tri-state buys. What the tri-state buys is that a probe which did not run
//     is never mistaken for a detach and never clears a stored host — and a
//     memoized non-answer keeps that exactly, because it is stored AS a
//     non-answer and every consumer of the bit sees the identical (nil, false)
//     pair either way: captureLauncher ignores it outright, and backfillLauncher
//     and refreshHerdrHosts both route it through applyHerdrHostBackfill, which
//     returns before touching the stored launcher. The one thing a cached
//     unknown costs is deferring a RECOVERY — a probe that would have
//     answered. Note the honest bound on that deferral is a SWEEP, not a TTL:
//     the entry expires one TTL after the probe that wrote it, but nothing
//     reads it in between, so what a user observes is quantized to
//     refreshHerdrHosts' cadence. See the TTL note below.
//   - The failure the reason describes is no longer the failure that arrives
//     here. #1485 was written when the only non-answer was a failed lsof probe,
//     which fails fast, so re-paying it per pane was nearly free and the spread
//     was the only cost worth naming. #1492 routed the most EXPENSIVE outcome in
//     this function to the same branch: every candidate probed and none
//     readable, which is up to maxClientCandidates candidates, each costing two
//     independent ancestry walks on readProcInfo's 2s ceiling. Re-paying now
//     dominates, and the trade the rule was making inverted.
//
// The new rule: the memo holds what ONE probe of this socket determined, for
// one TTL — an answer or the absence of one. A non-answer keeps probed=false on
// the way out, so nothing downstream can tell a cached one from a fresh one.
//
// The unknown deliberately shares the answer's TTL rather than getting a
// shorter one of its own. A shorter TTL does not survive the interleaving: the
// sweep iterates sessions, not sockets, so two panes of one herdr server need
// not be adjacent, and any TTL shorter than a whole sweep pass can expire
// between them.
//
// What a deferred recovery actually costs, stated honestly rather than as one
// TTL: the reader is the liveness sweep, so recovery lands on the next sweep
// whose probe runs. SweepDeadPIDs (pid_manager.go) ticks at 5s and backs off
// to 15s after three clean sweeps — the idle steady state — so the worst case
// is about one 15s interval. At the 5s cadence it can instead take two
// intervals, because entry.at is stamped when the probe FINISHES rather than
// when the pass began, so an entry written late in a pass is not yet expired
// when the next pass reaches it. It cannot slip further than that, because
// herdrClientCacheGet does not restamp on a hit: a sweep served from the memo
// costs nothing and so cannot keep pushing the expiry ahead of itself.
func herdrClientLauncher(socketPath string) (*session.Launcher, bool) {
	if socketPath == "" {
		return nil, false
	}
	if cached, hit := herdrClientCacheGet(socketPath); hit {
		return cached.launcher, cached.probed
	}
	resolved, probed := resolveHerdrClientLauncher(socketPath)
	herdrClientCachePut(socketPath, resolved, probed)
	return resolved, probed
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
	// probed carries herdrClientLauncher's second return through the memo. It
	// is what makes caching a non-answer safe: without it a cached nil is
	// indistinguishable from a cached detach, and the next pane to read this
	// socket would be told its client is gone on the strength of a probe that
	// never ran — #1485's defect, re-entered through the cache.
	probed bool
	at     time.Time
}

var (
	herdrClientCacheMu sync.Mutex
	herdrClientCache   = map[string]herdrClientCacheEntry{}
)

// herdrClientCacheGet reports what the last probe of socketPath determined.
// The entry rather than its fields, because launcher and probed are the same
// tri-state herdrClientLauncher returns — a cached "nothing attached" is
// (nil, probed) while a cached non-answer is (nil, !probed), and two adjacent
// bools in a return list is the one shape a future call site can transpose
// silently. Collapsing those two states would be #1485.
//
// A hit deliberately does NOT restamp entry.at. Refreshing on read would let a
// socket busy enough to be read every sweep hold a stale entry — in particular
// a stale non-answer — indefinitely; leaving the stamp alone bounds any entry
// to one TTL from the probe that produced it, however often it is read.
func herdrClientCacheGet(socketPath string) (herdrClientCacheEntry, bool) {
	herdrClientCacheMu.Lock()
	defer herdrClientCacheMu.Unlock()
	return herdrClientCacheLive(socketPath)
}

// herdrClientCacheLive returns socketPath's entry when it has not expired, and
// drops it when it has — so sockets that stop being used don't accumulate.
// Callers hold herdrClientCacheMu.
//
// Split out so the expiry rule has exactly one spelling. Both readers need it
// and they need it with opposite polarity, which is precisely the pair that
// drifts.
func herdrClientCacheLive(socketPath string) (herdrClientCacheEntry, bool) {
	entry, ok := herdrClientCache[socketPath]
	if !ok {
		return herdrClientCacheEntry{}, false
	}
	if time.Since(entry.at) > herdrClientCacheTTL {
		delete(herdrClientCache, socketPath)
		return herdrClientCacheEntry{}, false
	}
	return entry, true
}

// herdrClientCachePut records what a probe of socketPath determined. A
// non-answer never displaces a live answer.
//
// That guard is new with the memo and is the one hazard memoizing non-answers
// introduces: before #1514 a non-answer was never written, so it could not
// overwrite anything. Two goroutines can miss the memo for one socket and
// probe concurrently — refreshHerdrHosts reads outside assignMu, on the sweep
// goroutine, while PID discovery reaches captureLauncher/backfillLauncher on
// its own — and a non-answer is by far the SLOWER outcome to produce (up to
// maxClientCandidates candidates, two ancestry walks each on a 2s ceiling).
// So the loser of that race is systematically the one carrying no
// information, and last-writer-wins would let it both erase a resolved host
// and restamp the entry fresh, extending the deferral by another full TTL.
//
// Keeping the incumbent cannot pin anything: its own at is left alone, so it
// still expires on the schedule the probe that produced it set. The reverse
// direction needs no guard — an answer displacing a non-answer is strictly
// more information, which is the whole point of re-probing.
func herdrClientCachePut(socketPath string, l *session.Launcher, probed bool) {
	herdrClientCacheMu.Lock()
	defer herdrClientCacheMu.Unlock()
	if !probed {
		if prev, live := herdrClientCacheLive(socketPath); live && prev.probed {
			return
		}
	}
	herdrClientCache[socketPath] = herdrClientCacheEntry{launcher: l, probed: probed, at: time.Now()}
}
