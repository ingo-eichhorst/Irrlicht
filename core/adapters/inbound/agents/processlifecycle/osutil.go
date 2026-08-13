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
	"path/filepath"
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
	"HERDR_PANE_ID":     {}, // herdr pane address (e.g. "w1:p2") — injected per pane, so unlike the vars above it always describes *this* pane
	"HERDR_SOCKET_PATH": {}, // herdr server socket; the complete addressing key for that server
}

// herdrClientLogName is the per-session file every attached herdr client holds
// open for writing, in the same directory as the server socket. It is what
// makes $HERDR_SOCKET_PATH — which the daemon already captures — a complete
// key for finding the client, with no need to also capture $HERDR_SESSION or
// to parse a client's argv (which differs between `herdr --session <name>`,
// a bare `herdr` on the default session, and `herdr session attach <name>`).
// Verified against herdr 0.8.0: the layout is identical for the default
// session (<config>/herdr.sock) and named ones
// (<config>/sessions/<name>/herdr.sock), and a session with no client attached
// has no writer on this file at all.
const herdrClientLogName = "herdr-client.log"

// herdrClientLogPath maps a captured $HERDR_SOCKET_PATH to the client log
// beside it. Returns "" for an empty socket path so callers inherit the
// "no address, no client" answer instead of probing a bare directory.
func herdrClientLogPath(socketPath string) string {
	if socketPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(socketPath), herdrClientLogName)
}

// ReadLauncherEnv returns the launcher identity captured from the process env
// of pid. Returns nil if env cannot be read or no interesting vars are present.
//
// hostKnown reports whether the host window could be *determined*, which is
// not the same as being found. It is false whenever this read established
// nothing: a herdr pane whose client probe did not run (#1485), a pid that
// cannot be looked at at all (pid <= 0, or a process whose env, ancestry and
// tty all came back empty), and — at the wiring in cmd/irrlichd — a revoked
// launcher consent. A caller merging this read into a launcher it already
// holds must not clear host fields when hostKnown is false: their absence
// means "not looked up", not "gone". A caller capturing a session's first
// launcher may ignore it, because there is nothing to clear and dropping the
// read would cost the pane its herdr address.
//
// Never blocks longer than 2 seconds. Never prompts the user — on macOS we use
// `sysctl(kern.procargs2)` (no TCC prompt; `ps e` stopped exposing env on
// modern macOS). On Linux we read /proc/<pid>/environ. Other platforms return
// nil.
func ReadLauncherEnv(pid int) (l *session.Launcher, hostKnown bool) {
	if pid <= 0 {
		return nil, false
	}
	// The agent's own identity deliberately ignores hostIdentity's second
	// return. hostKnown here is about the herdr CLIENT indirection below; a
	// direct session whose ancestry walk timed out is a different question
	// with different consumers (captureLauncher, launcherBackfillNeedsFor),
	// and widening it here would change behaviour for every non-herdr session
	// on a signal #1492 has no evidence about. See resolveClientHostIdentity
	// for the read that DOES act on it.
	l, _ = hostIdentity(pid)
	hostKnown = true

	// A herdr pane's window belongs to the attached client, so its host
	// identity is resolved from that process instead — one indirection past
	// the ancestry walk hostIdentity skipped (#1350). Runs after the TTY
	// capture so the pane keeps its own pty when nothing is attached, and is
	// overridden by the client's when something is: AdoptHostIdentity owns
	// that rule.
	if l.HerdrPaneID != "" {
		var client *session.Launcher
		client, hostKnown = herdrClientLauncher(l.HerdrSocketPath)
		l.AdoptHostIdentity(client)
	}

	if l.IsEmpty() {
		// Nothing was determined, so say so rather than passing hostKnown
		// through: an empty read is "env, ancestry and tty all came back
		// blank", which is a hardened-runtime process or one that has already
		// exited as often as it is a process with genuinely no terminal. Every
		// consumer today bails on the nil before reading the flag, so this
		// costs nothing — but a future consumer that reads the flag first
		// would otherwise be told an unreadable process HAS no host.
		return nil, false
	}
	return l, hostKnown
}

// hostIdentity resolves the window-owning identity of pid: its whitelisted
// env, the ancestry fallbacks, and its controlling TTY. This is the sequence
// that answers "which window is this process displayed in", and it is applied
// twice — once to the agent, and once to a herdr client standing in for it
// (osutil_darwin.go). Keeping it in one function is what stops the two from
// drifting when a step is added.
//
// complete reports whether the reads behind that answer actually ran, so a
// caller can tell "this process has no local window" from "I could not read
// this process" (#1492). It is false only when an ancestry walk aborted on an
// unreadable process rather than reaching a verdict.
//
// It deliberately says nothing about the env read, which cannot fail: the
// ProcessObserver port defines an unreadable env as an empty map rather than
// an error, and all three implementations discard it (process_darwin.go,
// process_linux.go, process_other.go).
//
// On darwin that costs nothing, because an env yielding no host is exactly the
// condition that makes the ancestry fallbacks run — so a false "no local
// window" always passes through a walk, and the walk is where the distinction
// can be drawn. **That argument does not carry to linux or other**, where both
// ancestry helpers are stubs reporting complete: there the env IS the only host
// source, so a failed `/proc/<pid>/environ` read reports complete here. It is
// inert only because herdrClientLauncher — the sole consumer of this bit — is
// itself a stub off darwin. A second consumer on another platform needs the
// port widened first, and must not read this comment as saying otherwise.
//
// The ordering is load-bearing: ancestry before TTY, and both before any
// adoption by the caller.
func hostIdentity(pid int) (l *session.Launcher, complete bool) {
	// Env may be empty — hardened-runtime processes hide it from sysctl.
	// Don't bail here: the ancestry fallback below is the only signal we
	// have in that case.
	env, _ := osProc.EnvOf(pid)

	l = launcherFromEnv(env)
	complete = true

	// The ancestry fallbacks resolve the *host application* of the process
	// tree, so they are skipped for a herdr pane: that tree leads to the herdr
	// server, which is a different terminal from the one the pane is displayed
	// in — and often no terminal at all, once the server detaches. Running
	// them would undo launcherFromEnv's suppression by another route (a server
	// started in the foreground from kitty would give every pane
	// TermProgram=kitty plus a backfilled kitty socket and window id).
	//
	// The ancestry walk is cached because three guarded blocks may all need it
	// (kitty TermProgram override, hardened-runtime TermProgram fallback,
	// kitty field back-fill). Walking the ppid chain once instead of up to
	// three times keeps this bounded — each readProcInfo is a `ps` shellout
	// with a 2s ceiling.
	//
	// A tmux pane is deliberately NOT skipped here, though the same argument
	// looks like it should apply. It does not: tmux's server is reparented to
	// PID 1, so the walk from a genuine pane terminates there having found
	// nothing — it is inert, not dangerous, and cannot put back what
	// launcherFromEnv suppressed. Meanwhile it is the only thing that recovers
	// a host for a process launched from a pane by a terminal that reports
	// none of its own (kitty), whose fields the suppression above drops. So
	// skipping it would buy no correctness and cost exactly that case.
	if l.HerdrPaneID == "" {
		complete = applyAncestryFallbacks(l, pid, &ancestryProbe{pid: pid})
	}

	// Capture the controlling TTY so Terminal.app (and potentially others)
	// can target the exact tab — Terminal.app's AppleScript dictionary
	// matches tabs by `tty` but has no session-UUID analog.
	l.TTY = processTTY(pid)
	return l, complete
}

// launcherFromEnv builds a Launcher from the whitelisted per-PID env vars
// alone, with no process-ancestry lookups. Split out of ReadLauncherEnv so
// the env-only assembly (TERM_PROGRAM and friends, tmux socket parsing,
// VS Code / JetBrains inference) can be reasoned about independently of the
// ancestry-walk fallbacks.
func launcherFromEnv(env map[string]string) *session.Launcher {
	// A herdr pane carries only its own address — see session.Launcher's
	// Herdr* fields for why everything else in that environment is a stale
	// description of the herdr server's. Returning early rather than clearing
	// fields one by one means an identity var added below later cannot
	// silently start leaking into herdr sessions (#1348).
	if pane := env["HERDR_PANE_ID"]; pane != "" {
		return &session.Launcher{
			HerdrPaneID:     pane,
			HerdrSocketPath: env["HERDR_SOCKET_PATH"],
		}
	}
	// A tmux pane is the same shape as a herdr pane and for the same reason,
	// so it keeps only its own address too — see session.Launcher's Tmux*
	// fields.
	//
	// The condition is $TMUX_PANE *and* tmux's own TERM_PROGRAM marker, never
	// $TMUX_PANE alone. $TMUX_PANE is not self-describing: every descendant of
	// a pane inherits it, including a GUI terminal or IDE launched from inside
	// one (`code .`, `kitty`), and such a process carries the stale pane
	// address alongside a correct host identity it reported itself. Keying on
	// the address alone discards that host and turns a working click-to-focus
	// into a silent no-op. tmux stamps TERM_PROGRAM=tmux onto the panes it
	// spawns, so a process claiming any other host claimed it for itself and
	// is left alone; one claiming no host of its own (kitty sets none —
	// upstream kitty#4793) is suppressed here and recovered by the ancestry
	// fallbacks, which is why hostIdentity must not skip them for tmux.
	//
	// Checked *after* herdr on purpose: a herdr server started from a tmux
	// pane hands every pane a $TMUX_PANE as well, and that one is the
	// server's, not the agent's (#1348). herdrPaneEnv in the tests is exactly
	// that env, and TestLauncherFromEnv_HerdrCapture locks the ordering.
	if pane := env["TMUX_PANE"]; pane != "" && env["TERM_PROGRAM"] == tmuxTermProgram {
		return &session.Launcher{
			TmuxPane:   pane,
			TmuxSocket: tmuxSocketFromEnv(env["TMUX"]),
		}
	}
	l := &session.Launcher{
		TermProgram:    env["TERM_PROGRAM"],
		ITermSessionID: env["ITERM_SESSION_ID"],
		TermSessionID:  env["TERM_SESSION_ID"],
		TmuxPane:       env["TMUX_PANE"],
		KittyListenOn:  env["KITTY_LISTEN_ON"],
		KittyWindowID:  env["KITTY_WINDOW_ID"],
		TmuxSocket:     tmuxSocketFromEnv(env["TMUX"]),
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
	return l
}

// tmuxTermProgram is the value tmux writes into $TERM_PROGRAM for the panes it
// spawns (measured on 3.6a, alongside TERM_PROGRAM_VERSION=3.6a). It is not a
// host — it names a multiplexer, matches no entry in the macOS activator
// registry, and being non-empty it suppresses the TermProgram=="" guards that
// reach the ancestry fallbacks — so it is used only as evidence that tmux, and
// not something launched from within it, spawned this process.
const tmuxTermProgram = "tmux"

// tmuxSocketFromEnv extracts the server socket from $TMUX, whose value is
// "/path/to/socket,pid,session". Returns "" for an empty $TMUX so a launcher
// with no tmux server keeps a zero socket rather than an empty-but-set one.
// Shared by both branches of launcherFromEnv: a tmux pane's address is the one
// thing it keeps, so the parse must not live only on the path that no longer
// runs for it.
func tmuxSocketFromEnv(tmux string) string {
	if tmux == "" {
		return ""
	}
	if i := strings.Index(tmux, ","); i > 0 {
		return tmux[:i]
	}
	return tmux
}

// ancestryProbe resolves pid's process ancestry via resolveHostFromAncestry at
// most once, caching the result for repeat calls. ReadLauncherEnv's
// ancestry-dependent fallbacks may all need the same walk; this keeps it
// bounded to a single `ps` shellout chain instead of up to three.
type ancestryProbe struct {
	pid      int
	resolved bool
	term     string
	hostPID  int
	complete bool
}

func (a *ancestryProbe) host() (term string, hostPID int) {
	if !a.resolved {
		a.term, a.hostPID, a.complete = resolveHostFromAncestry(a.pid)
		a.resolved = true
	}
	return a.term, a.hostPID
}

// walked reports whether this probe left nothing unread: either no walk was
// ever needed, or the one that ran reached a verdict rather than aborting on
// an unreadable process (#1492). Keeping the bit on the memo — rather than
// ANDing it at each of the guarded blocks that call host() — is what stops a
// block that forgets to accumulate from silently reporting an aborted walk as
// a complete one.
func (a *ancestryProbe) walked() bool { return !a.resolved || a.complete }

// applyAncestryFallbacks fills in Launcher fields that require walking pid's
// process ancestry — cases the env alone can't resolve: kitty's missing
// TERM_PROGRAM, hardened-runtime processes with no readable env at all, the
// generic top-level-.app host fallback, and kitty field back-fill for
// Apple-signed agents. ancestry is expected to be memoized by the caller so
// this can call it from multiple guarded blocks at no extra cost.
//
// The return is the AND of both walks it can run — the memoized ancestry probe
// and the separate bundle-id walk: false means one of them aborted on an
// unreadable process, so the fields it would have filled are missing rather
// than absent (#1492). A run in which neither was needed is complete by
// definition.
func applyAncestryFallbacks(l *session.Launcher, pid int, ancestry *ancestryProbe) (complete bool) {
	complete = true
	// kitty intentionally does not set TERM_PROGRAM (upstream kitty issue
	// #4793), so the env-captured value may be inherited from whatever
	// process launched kitty.app (e.g. a VS Code integrated terminal). When
	// KITTY_WINDOW_ID is present, kitty *is* the host of this session — but
	// we still verify via process ancestry to rule out the reverse case
	// (KITTY_WINDOW_ID leaked from a kitty shell that spawned VS Code).
	if l.KittyWindowID != "" && l.TermProgram != "kitty" {
		if term, _ := ancestry.host(); term == "kitty" {
			l.TermProgram = "kitty"
		}
	}
	// Hardened-runtime processes (e.g. Anthropic's signed `claude` binary)
	// hide env from sysctl. Fall back to process-ancestry walking so the UI
	// can at least bring the host app to the front. Darwin-only; other
	// platforms return "" and this is a no-op.
	if l.TermProgram == "" {
		l.TermProgram, _ = ancestry.host()
	}
	// Generic host fallback: when no curated host matched (TermProgram still
	// empty), resolve the first top-level `.app` ancestor's bundle id so the
	// client can bring an embedded-terminal GUI host (e.g. Obsidian) to the
	// front without a per-app registry entry. Purely additive — it only runs
	// on a map miss, so every curated host keeps its exact behavior. Darwin-
	// only; other platforms return "" and this is a no-op.
	if l.TermProgram == "" {
		bundleID, _, ok := resolveHostBundleIDFromAncestry(pid)
		complete = ok
		l.HostBundleID = bundleID
	}
	// Back-fill kitty fields for sessions whose own env is unreadable
	// (Apple-signed agents like `pi`, hardened-runtime binaries). If kitty
	// is the host per ancestry walk but env yielded no kitty signals,
	// derive them from kitty.app itself + its remote-control socket.
	// Without this, clicking the session in the UI raises kitty but can't
	// target the right tab — exactly the symptom reported for pi sessions
	// in issue #326.
	applyKittyAncestryBackfill(l, pid, ancestry)
	return complete && ancestry.walked()
}

// applyKittyAncestryBackfill fills in KittyPID/KittyListenOn/KittyWindowID
// for a session whose env yielded no kitty signals despite ancestry saying
// kitty is the host (Apple-signed agents like `pi`, hardened-runtime
// binaries) — split out of applyAncestryFallbacks so its nested guards don't
// compound that function's cognitive complexity (go:S3776).
//
// It is the one block that can be the ONLY caller of the ancestry probe in a
// run — a candidate whose env names kitty but carries no KITTY_PID skips all
// three blocks above — which is why its caller reads the verdict off the probe
// afterwards rather than trusting the blocks to have accumulated it.
func applyKittyAncestryBackfill(l *session.Launcher, pid int, ancestry *ancestryProbe) {
	if l.TermProgram != "kitty" || l.KittyPID != 0 {
		return
	}
	term, kpid := ancestry.host()
	if term != "kitty" || kpid <= 0 {
		return
	}
	l.KittyPID = kpid
	if l.KittyListenOn == "" {
		l.KittyListenOn = kittyListenOnFor(kpid)
	}
	if l.KittyListenOn != "" && l.KittyWindowID == "" {
		l.KittyWindowID = kittyWindowIDForPID(l.KittyListenOn, pid)
	}
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
	p = skipProcargs2ArgvEntries(buf, argc, p)
	collectProcargs2EnvEntries(buf, p, out)
	return out
}

// skipProcargs2ArgvEntries advances past the argc NUL-terminated argv[]
// strings starting at offset p, returning the offset of the first envp
// entry (or len(buf) if the buffer ends first).
func skipProcargs2ArgvEntries(buf []byte, argc, p int) int {
	for i := 0; i < argc && p < len(buf); i++ {
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		if p < len(buf) {
			p++ // skip NUL
		}
	}
	return p
}

// collectProcargs2EnvEntries reads NUL-terminated "KEY=VALUE" envp entries
// starting at offset p until an empty string or the end of the buffer,
// recording the whitelisted ones into out.
func collectProcargs2EnvEntries(buf []byte, p int, out map[string]string) {
	for p < len(buf) {
		start := p
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		if p == start {
			return
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
