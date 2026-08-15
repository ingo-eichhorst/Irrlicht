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
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/session"
)

// clientHostBudget is the ONE aggregate deadline covering an entire attached-
// client indirection: the scan that finds the attached clients and every
// candidate probed behind it (resolveHerdrClientLauncherVia and
// resolveTmuxClientLauncherVia, osutil_darwin.go).
//
// It exists because every individual shellout on that path was bounded and the
// path was not (#1529). resolveClientHostIdentity loops over up to
// maxClientCandidates candidates; each costs two independent ancestry walks
// plus a tty `ps`, and the bundle-id walk pays a `plutil` per hop up to
// maxAncestry — so the bound was a COUNT, and one resolve could run to tens of
// seconds.
//
// ONE constant for BOTH producers, which #1501 decided rather than inherited.
// The two scans differ by more than an order of magnitude — `tmux -S <sock>
// list-clients` measured at 14ms/call against an `lsof` of a herdr client log
// at roughly 0.3s — so a per-producer budget was the obvious alternative. It is
// not taken, for three reasons:
//
//   - What the budget bounds is the CANDIDATE LOOP, and that is literally the
//     same function for both (resolveClientHostIdentityVia). The scan is the
//     cheap stage in both spellings; the ancestry walks behind it are what can
//     run to tens of seconds, and they do not know which producer found the pid.
//   - A cheaper scan does not want a smaller aggregate, it wants the same one:
//     it leaves MORE of the budget for the candidates, which is exactly the
//     trade resolveHerdrClientLauncherVia's doc says lsof loses.
//   - The sum this bounds has to fit inside one liveness-sweep tick, and that
//     assertion is over ONE number. Two constants would let one drift past the
//     tick while the test still passed on the other — a second number with no
//     second piece of evidence behind it.
//
// The value is set by what it has to fit inside rather than by what the probes
// cost: PIDManager.SweepDeadPIDs calls refreshMultiplexerHosts synchronously on
// its ticker, and Go's Ticker drops ticks it overruns, so a single client
// resolve delays dead-PID reaping for every session behind it.
// TestClientHostReadFitsTheLivenessSweepTick reads that cadence out of the
// services source and pins the sum against it, so the relation is measured
// rather than restated.
//
// Abandoning candidates is safe by construction and that is what makes the
// budget cheap: an abandoned candidate is a NON-answer (#1492), not a detach,
// so the caller is told "I could not look" and nothing clears a stored host.
//
// It lives here, platform-neutral, although the loop it bounds is darwin-only.
// The contract it defines is stated in ReadLauncherEnv's doc, which compiles on
// every platform, so a constant behind //go:build darwin would leave that doc
// naming a symbol a linux reader cannot look up, and would put the arithmetic
// that matters behind the same tag. It also belongs beside noAggregateBudget:
// together the two ARE the decision about which host reads get an aggregate and
// which deliberately do not, and splitting that across a build tag hides half
// of it.
const clientHostBudget = 2 * time.Second

// noAggregateBudget is the context every host read that deliberately has NO
// aggregate deadline runs under. Naming it is the whole point: #1529 bounded
// the herdr client indirection (clientHostBudget above) and left these
// unbounded, and a named helper makes that a reviewable absence rather than a
// context.Background() whose meaning the next reader has to infer.
//
// Its two production call sites are the direct (non-herdr) launcher read in
// ReadLauncherEnv and the interactive-host admission gate
// (IsKnownInteractiveHost). Both keep exactly the aggregate they had before
// #1529 — a COUNT — and their reasons are OPPOSITE polarities rather than one
// shared argument, which is why they are stated separately instead of as
// "bounding either would change an answer":
//
//   - ReadLauncherEnv discards hostIdentity's completeness for the direct read
//     (see the call site), so an abandoned walk there arrives at consumers as a
//     determined "this process has no host" with hostKnown TRUE. Bounding it
//     would move answers in the host-CLEARING direction — the misroute #1348
//     opened and #1492 narrowed.
//   - IsKnownInteractiveHost fails OPEN on an incomplete walk (#1513), so
//     bounding it would move answers the other way: the #784 exclusion would
//     quietly stop excluding. Nothing counts how often either of its walks
//     fails to be answered (#1534), so that degradation would be invisible.
//     The standing cost is that this gate — reached synchronously from PID
//     discovery — is still bounded only by a COUNT: two walks of up to
//     maxAncestry hops, each hop a `ps` and possibly a `plutil`, every one of
//     them at shelloutTimeout. #1529 gathered no evidence about that path and
//     deliberately did not move it.
//
// This is NOT the bare context.Background() core/architecture_shellout_test.go
// forbids: nothing passes it to exec.CommandContext. Every shellout downstream
// still derives its own shelloutTimeout from it, so each CHILD keeps a
// ceiling. What is absent is only the ceiling ACROSS children.
func noAggregateBudget() context.Context { return context.Background() }

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
// nothing: a pane whose client probe did not run (#1485 for herdr, #1501 for
// tmux), a pid that cannot be looked at at all (pid <= 0, or a process whose
// env, ancestry and tty all came back empty), and — at the wiring in
// cmd/irrlichd — a revoked launcher consent. A caller merging this read into a
// launcher it already holds must not clear host fields when hostKnown is false:
// their absence means "not looked up", not "gone". A caller capturing a
// session's first launcher may ignore it, because there is nothing to clear and
// dropping the read would cost the pane its address.
//
// Read it as "the host of the PANE this launcher addresses was determined",
// which is what the merging contract above actually needs, and which #1501
// separated from "a host was determined". For a launcher addressing no pane at
// all the two readings coincide and it is true, exactly as before.
//
// A process that merely INHERITED a $TMUX_PANE used to be the case that made
// the distinction bite: it has a host of its own and no claim on that pane's,
// so the answer for it was false and the merge left its own identity alone.
// Since #1582 it no longer carries the pane address at all
// (dropInheritedTmuxPane), so it is an ordinary launcher addressing no pane and
// the answer for it is true — which is also what stops the liveness sweep
// paying a re-read per tick for a session whose identity was never the pane's
// to change (services.hostedInAMultiplexerPane). The distinction itself stays:
// it is what a genuine pane whose client probe did not run still needs.
//
// Never prompts the user — on macOS we use
// `sysctl(kern.procargs2)` (no TCC prompt; `ps e` stopped exposing env on
// modern macOS). On Linux we read /proc/<pid>/environ. Other platforms return
// nil.
//
// On the herdr path this function is bounded at shelloutTimeout +
// clientHostBudget. One shelloutTimeout is the pane's own controlling-TTY
// `ps`, which is the ONLY child process the direct read starts for a herdr
// pane: launcherFromEnv returns early with HerdrPaneID set, hostIdentityVia
// skips the ancestry fallbacks for exactly that case, and the env read is a
// sysctl rather than a shellout. clientHostBudget is one aggregate deadline
// covering the entire client indirection below — the lsof scan and every
// candidate behind it. The sum is not restated here as a number, because a
// number a doc carries and nothing produces drifts: it is
// TestClientHostReadFitsTheLivenessSweepTick that measures it, against the sweep
// cadence that has to accommodate it.
//
// It used to say "never blocks longer than 2 seconds", and then, honestly,
// that it said nothing at all. Every individual shellout was bounded at 2s and
// the function was not: resolveClientHostIdentity looped over up to
// maxClientCandidates candidates, each independently spending two ancestry
// walks plus a tty `ps`, with the bundle-id walk paying a `plutil` per hop.
// The bound was a COUNT, not a duration. #1529 gave that loop one aggregate
// deadline, so the arithmetic above is a real ceiling rather than a hope.
//
// The DIRECT path is deliberately still a count, and saying so is the point of
// scoping the sentence above to herdr. A non-herdr pid walks its ancestry
// under per-shellout ceilings only, up to maxAncestry hops on each of two
// walks. Bounding it would be a behaviour change in the host-CLEARING
// direction on a path #1529 has no evidence about: an abandoned direct walk
// returns an empty host with hostKnown TRUE — this function discards
// hostIdentity's completeness for the direct read, see below — which is
// exactly the shape the contract above tells callers they may act on. So the
// budget stops at the herdr indirection, and noAggregateBudget names what the
// direct read runs under instead.
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
	l, _ = hostIdentity(noAggregateBudget(), pid)
	hostKnown = true

	// A multiplexer pane's window belongs to the attached client, so its host
	// identity is resolved from that process instead — one indirection past
	// the ancestry walk (#1350 for herdr, #1501 for tmux). Runs after the TTY
	// capture so the pane keeps its own pty when nothing is attached, and is
	// overridden by the client's when something is: AdoptHostIdentity owns
	// that rule.
	if client, probed, addressesAPane := clientHostFor(l); addressesAPane {
		hostKnown = probed
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

// clientHostFor resolves the identity of the client displaying l's pane, for
// the one multiplexer l's session lives in.
//
// Three-valued on purpose, and the third value is the one worth reading twice.
// addressesAPane says l carries a pane address at all; probed is the
// #1485/#1492 tri-state for that pane's host. They come apart in exactly one
// case and it is the case #1499 was filed about: a launcher that carries a
// $TMUX_PANE it INHERITED — a GUI terminal or IDE launched from inside a pane —
// addresses a pane whose host it must not adopt, because its own host is real
// and the pane's belongs to a different process. So that case answers
// (nil, false, true): "this addresses a pane, and the pane's host was not
// looked up", which is precisely the shape ReadLauncherEnv's hostKnown contract
// tells a merging caller not to clear fields on.
//
// Since #1582 that case no longer arrives from ReadLauncherEnv, which drops the
// inherited address one step earlier (dropInheritedTmuxPane), so the branch is
// reached only by tests today. It is kept rather than deleted because it guards
// a DIFFERENT failure from the one that drop fixes — adopting a stranger's
// window onto a session that has its own, which is #1499 rather than #1582 —
// and the two are not one edit apart.
//
// It is the dispatcher, and having one is the point: the two producers differ
// only in how they enumerate clients (osutil_darwin.go), and the ORDER they are
// tried in is #1348's — herdr first, because a herdr server started from inside
// a tmux pane hands every pane it spawns a $TMUX_PANE that is the server's.
// session.Launcher.pane applies the same precedence for the adoption's
// put-back, and the two must not disagree.
func clientHostFor(l *session.Launcher) (client *session.Launcher, probed, addressesAPane bool) {
	switch {
	case l.HerdrPaneID != "":
		client, probed = herdrClientLauncher(l.HerdrSocketPath)
		return client, probed, true
	case l.TmuxPane == "":
		return nil, false, false
	case !tmuxPaneAwaitsItsClient(l):
		return nil, false, true
	default:
		client, probed = tmuxClientLauncher(l.TmuxSocket)
		return client, probed, true
	}
}

// tmuxPaneAwaitsItsClient reports whether l is a tmux pane whose host can only
// come from the attached client.
//
// It is deliberately NARROWER than "$TMUX_PANE is set", and the difference is
// the whole reason #1499 keyed its suppression on tmux's own TERM_PROGRAM
// marker rather than on the pane address. $TMUX_PANE is not self-describing:
// every descendant of a pane inherits it, so a GUI terminal or IDE launched
// from inside one (`code .`, `kitty`) carries a stale pane address next to a
// perfectly good host identity of its own. Resolving THAT session's tmux client
// and adopting it would replace a correct host with the window displaying a
// pane the session is not in — the #1348 misroute, arriving through the fix for
// it.
//
// The three-term test is what tells those apart, and each term is doing work:
//
//   - TmuxPane non-empty: there is a pane address to resolve a client for.
//   - TermProgram and HostBundleID both empty: nothing has claimed a host for
//     this process. A descendant that reports its own $TERM_PROGRAM never had
//     it suppressed (launcherFromEnv), and one that reports none of its own —
//     kitty, upstream kitty#4793 — has just had it recovered by the ancestry
//     fallbacks, which hostIdentity deliberately runs for tmux for exactly that
//     reason. Either way the host is the process's own and is not the client's
//     to overwrite.
//
// A genuine pane passes all three, because its ancestry terminates at the
// reparented tmux server: the walk is inert there (hostIdentityVia says so),
// which is what leaves the two host fields empty for it and for nothing else.
// So this is the same "no local window" test resolveClientHostIdentityVia
// applies to a CANDIDATE, applied one level up to decide whether to look for
// candidates at all.
//
// Since #1582 the same three terms answer a second question — whether the pane
// address is worth RECORDING at all — and dropInheritedTmuxPane below asks it
// through this function rather than restating the terms. The two questions are
// the same one: an address that is not this process's own is neither a pane
// whose client we may adopt nor a pane we may address.
func tmuxPaneAwaitsItsClient(l *session.Launcher) bool {
	return l.TmuxPane != "" && l.TermProgram == "" && l.HostBundleID == ""
}

// dropInheritedTmuxPane clears l's tmux address when that address is not the
// process it was read from's own.
//
// $TMUX_PANE is inherited by every descendant of a pane, so a GUI terminal or
// IDE launched from inside one (`code .`, `kitty`, `open -a iTerm`) carries a
// pane address belonging to a different process in a different window. #1499
// already keeps such a session's own host identity — its suppression keys on
// tmux's own TERM_PROGRAM marker — and copied the pane address alongside it.
// control.resolveBackend routed to the tmux backend on that address, so the
// backchannel ran `tmux -S <inherited socket> send-keys -t %17 -l -- <text>`
// into a stranger's pane, and interrupt and capture addressed the same one
// (#1582). That is the failure #1348 removed for herdr, reached through the
// one field #1499 deliberately left populated for a descendant. (resolveBackend
// has since grown a socket requirement of its own, #1593 — an independent rule
// that would not have caught this one, because $TMUX is inherited beside
// $TMUX_PANE and a descendant carries both.)
//
// The CAPTURE is the only place this can be decided, which is why the fix is
// here and resolveBackend was untouched by it. A stored launcher cannot tell the two
// apart: a genuine pane that adopted its client's identity (#1501) and a
// descendant that reported its own end up with the same fields —
// {TermProgram: iTerm.app, ITermSessionID, TmuxPane, TmuxSocket} — and nothing
// records which process the host came from. Requiring BOTH tmux fields the way
// resolveBackend requires both herdr ones does not discriminate either: $TMUX
// is inherited beside $TMUX_PANE, so a descendant carries the socket too.
//
// It runs AFTER the ancestry fallbacks, and not inside launcherFromEnv, because
// the env alone does not answer the question. tmux stamps TERM_PROGRAM=tmux
// onto the panes it spawns, and a kitty window launched from a pane inherits
// exactly that (kitty sets no TERM_PROGRAM of its own, upstream kitty#4793) —
// so at env time a genuine pane and a kitty descendant are the same map, and an
// env-only check would leave that descendant addressing the pane kitty was
// launched from while its own kitty backend sat one branch further down
// resolveBackend. The walk is what separates them: from a genuine pane it
// terminates at the reparented tmux server having found nothing, and from a
// descendant it finds the host app.
//
// It fails towards DROPPING. An address wrongly dropped costs click-to-focus
// and the backchannel for that one session, which keeps its own host and stays
// visible; an address wrongly kept types the user's text into a terminal they
// were not looking at, and no amount of it being rare makes that the better
// error. The residual is a descendant whose host cannot be resolved at all (no
// $TERM_PROGRAM of its own, no `.app` in its ancestry): it is indistinguishable
// from a genuine pane here and keeps the address, which is the same residual
// #1499's suppression already accepted.
func dropInheritedTmuxPane(l *session.Launcher) {
	if l.TmuxPane == "" || tmuxPaneAwaitsItsClient(l) {
		return
	}
	l.TmuxPane, l.TmuxSocket = "", ""
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
// this process" (#1492). It is false when an ancestry walk aborted rather than
// reaching a verdict — on an unreadable process, or on a bundle-id probe that
// was never answered (#1524) — and, since #1533, when the controlling-TTY read
// never answered either. Every bounded shellout behind this answer now feeds
// it; the TTY one did not, because it runs after complete is computed and
// nothing folded it back in.
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
//
// ctx carries the caller's aggregate budget, and every bounded shellout below
// derives its own shelloutTimeout from it — so a child is killed at whichever
// comes first, its own ceiling or the budget. That composition is what makes
// #1529's aggregate deadline real rather than advisory: this function is
// applied once per herdr candidate, and a candidate cut short by the shared
// budget reports complete=false, which is already the "could not be read"
// non-answer #1492 gave it. Callers with no aggregate to impose pass
// noAggregateBudget().
func hostIdentity(ctx context.Context, pid int) (l *session.Launcher, complete bool) {
	return hostIdentityVia(ctx, pid, processTTY)
}

// ttyProbe is the controlling-TTY read hostIdentityVia makes, injected so the
// #1533 non-answer can be arranged — a real `ps` cannot be driven over its
// ceiling on purpose. Same idiom as resolveHostBundleIDVia's two probes.
type ttyProbe func(ctx context.Context, pid int) (string, bool)

// hostIdentityVia is hostIdentity with the TTY read injected.
func hostIdentityVia(ctx context.Context, pid int, readTTY ttyProbe) (l *session.Launcher, complete bool) {
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
		complete = applyAncestryFallbacks(ctx, l, pid, &ancestryProbe{pid: pid, reads: newAncestryReads()})
	}

	// Here rather than in launcherFromEnv, because the answer needs the walk
	// above: see dropInheritedTmuxPane.
	dropInheritedTmuxPane(l)

	// Capture the controlling TTY so Terminal.app (and potentially others)
	// can target the exact tab — Terminal.app's AppleScript dictionary
	// matches tabs by `tty` but has no session-UUID analog.
	tty, ttyProbed := readTTY(ctx, pid)
	l.TTY = tty
	// #1533: the TTY read is the third bounded shellout behind this answer, and
	// it was the one whose verdict was dropped — it runs AFTER complete is
	// computed, so folding it in has to be explicit. Its polarity is #1492's
	// rather than #1513's: this is enrichment, so the bit to carry is "this
	// field is missing rather than absent", not "admit on no evidence".
	return l, complete && ttyProbed
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
	// What this branch decides is which HOST fields survive, never whether the
	// pane ADDRESS is the process's own — both shapes above carry one and only
	// one of them is in that pane, and this map cannot tell them apart. That is
	// dropInheritedTmuxPane's question, asked once the ancestry walk has run
	// (#1582).
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
		TmuxPane:       env["TMUX_PANE"], // provisional; see dropInheritedTmuxPane (#1582)
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

// procInfoProbe is one per-PID `ppid + comm` read — the unit both ancestry
// walks are built out of. It lives here rather than beside its darwin
// implementation because ancestryReads below is what the two walks SHARE, and
// that sharing is arranged by cross-platform code (hostIdentityVia).
type procInfoProbe func(ctx context.Context, pid int) (ppid int, cmd string, err error)

// errProcessGone is the error a per-PID read returns when the read ANSWERED and
// the answer was "there is no such process". It is part of this type's contract
// rather than one implementation's private detail, which is why it lives here
// beside procInfoProbe and not next to readProcInfo (osutil_darwin.go).
//
// It is #1574, and the asymmetry it removes was one file apart: processTTYVia
// classifies its `ps` with probeAnswered and says in its own doc why an exit 1
// must be an ANSWER — "a real 'no such process', not a probe that did not run —
// so the allowlist form would turn every reaped pid into a non-answer and poison
// hostIdentity's completeness for it" — while readProcInfo, eleven lines further
// down, returned a bare error for exactly that exit and did what that sentence
// warns against. #1534's counter then made the two visibly disagree: `ps` exit 1
// is ANSWERED by probeOutcomeRule, so the #784 gate reported "admitted on a walk
// I could not complete" beside a ps.proc_info row reporting perfect health
// (measured in PR #1576).
//
// What it does NOT do is change a walk's verdict. A gone process still ends the
// walk with no host and completeness false, because there is no ancestry left to
// read; what it changes is that the daemon can now say WHY, and the one caller
// that publishes the difference does (hostGateProcessGone, hostgate.go). The
// alternative — terminating the walk as if at PID 1, i.e. a COMPLETED walk that
// found no host — was considered and rejected: at the admission gate that turns
// a reaped pid into a rejection, which SessionDetector caches per session id and
// never evicts, so a short-lived agent whose process exits between PID discovery
// and the walk would be hidden for the lifetime of the daemon. That is the harm
// #1513 was filed about, and "this process is gone" is no more evidence of a
// non-interactive host than "this probe was killed" is.
var errProcessGone = errors.New("no such process")

// procInfoAnswer is one ANSWERED per-PID read. Only answers are representable:
// there is no error field, which is the admission rule of ancestryReads made
// structural rather than remembered.
type procInfoAnswer struct {
	ppid int
	cmd  string
}

// ancestryReads dedups the per-PID `ps` reads the two ancestry walks make,
// **within one resolve and no further**. It is the second half of #1544, and
// its lifetime is the OPPOSITE of the bundle-id memo's in osutil_darwin.go —
// which is the single thing to get right here:
//
//   - A CFBundleIdentifier is immutable for the life of a bundle, so that memo
//     is process-global and synchronised.
//   - A ppid is not. The process table changes constantly: PIDs die, are
//     reaped, and are REUSED. A process-global memo of ppid/comm would resolve
//     a dead process's ancestry, or — the case that is a real defect rather
//     than a stale enrichment — a recycled PID's, reporting the window of
//     whatever process last held that number. So this one is created per
//     resolve, handed to both walks, and dropped when the resolve returns.
//
// Being per-resolve is also why it carries no mutex: one instance is reachable
// from exactly one goroutine, for the duration of one call. A shared instance
// would need one, and would be wrong for the reason above — so the missing
// mutex is a consequence of the lifetime, not an oversight to be "fixed" by
// adding one.
//
// What it buys, measured on the committed code rather than argued: both call
// sites run walk 1 to a full verdict and then walk 2 over the SAME chain, so
// every PID in the chain was read exactly twice — 2 x depth `ps` execs, at a
// measured 6.6ms median per exec on the machine #1544 was implemented on
// (n=300; p99 10.2ms). At the maxAncestry ceiling that is ten wasted execs on
// the synchronous discovery path.
//
// The admission rule is the same sentence as the bundle-id memo's, at a
// different lifetime: **memoize answers, never a non-answer.** A failed read is
// returned and forgotten, so walk 2 re-probes a PID walk 1 could not read. That
// keeps the change purely a performance one — the two walks reach exactly the
// verdicts they reached before, including when a `ps` is killed by its ceiling
// mid-chain and the second walk's retry succeeds.
type ancestryReads struct {
	read procInfoProbe
	seen map[int]procInfoAnswer
	// gone records that a read in THIS resolve was answered "there is no such
	// process" (#1574). It is the REASON behind whatever completeness bit the
	// walks report, and it is kept here because this memo is the only object
	// that sees every per-PID read of one evaluation — the two walks each
	// return a bare bool, and three callers consume it while only one (the #784
	// gate) acts on the difference.
	//
	// One flag rather than a per-read record, because a read that fails ends its
	// walk immediately and walk 2 runs only when walk 1 both completed and found
	// nothing: at most one read fails per resolve, so this flag IS that
	// failure's reason rather than a summary of several.
	gone bool
}

// newAncestryReadsVia builds a per-resolve memo over an injected read. The
// production binding is newAncestryReads (osutil_darwin.go); every other
// platform's ancestry walks are stubs that never probe.
func newAncestryReadsVia(read procInfoProbe) *ancestryReads {
	return &ancestryReads{read: read, seen: map[int]procInfoAnswer{}}
}

// probe is the procInfoProbe the walks are handed. It answers from seen when it
// can, and records only what the underlying read ANSWERED.
func (r *ancestryReads) probe(ctx context.Context, pid int) (ppid int, cmd string, err error) {
	if answer, hit := r.seen[pid]; hit {
		// #1534: a hit starts no child, so runProbe never sees it, and an
		// "N answered" that silently excluded hits would understate how often
		// this probe is ASKED. #1544's own hand-back named this — "a memo now
		// also hides how often the probe runs" — so the hit is counted where it
		// happens, on the kind the underlying read would have used, and
		// published as its own outcome rather than folded into the runs.
		observeProbeMemoHit(probePSProcInfo)
		return answer.ppid, answer.cmd, nil
	}
	ppid, cmd, err = r.read(ctx, pid)
	if err != nil {
		// #1574: record WHICH kind of failure this was before returning it. A
		// gone process is an ANSWER — the caller that publishes gate outcomes
		// reads this so its "walk aborted" row keeps meaning "a child did not
		// answer" — while everything else about the failure path is unchanged.
		if errors.Is(err, errProcessGone) {
			r.gone = true
		}
		// Never memoize a non-answer. A `ps` killed by its ceiling says
		// nothing about this PID, and storing that would let one loaded moment
		// abort both walks instead of one — turning a transient into a verdict
		// for the whole resolve. #1524 drew this line for the plutil probe;
		// this is the same line one shellout over.
		//
		// A gone process is not memoized either, and that is deliberate rather
		// than an oversight: what would be stored is an absence, the walks
		// re-probe it at most once more, and the PID could be recycled between
		// the two reads — the hazard this memo's lifetime exists to bound.
		return 0, "", err
	}
	r.seen[pid] = procInfoAnswer{ppid: ppid, cmd: cmd}
	return ppid, cmd, nil
}

// sawProcessGone reports whether any read in this resolve was answered "there is
// no such process". Read AFTER the walks have run, by the one caller that
// distinguishes the two ways a walk can fail to reach a verdict (#1574).
func (r *ancestryReads) sawProcessGone() bool { return r.gone }

// ancestryProbe resolves pid's process ancestry via resolveHostFromAncestry at
// most once, caching the result for repeat calls. ReadLauncherEnv's
// ancestry-dependent fallbacks may all need the same walk; this keeps it
// bounded to a single `ps` shellout chain instead of up to three.
//
// It memoizes walk 1's VERDICT. reads memoizes the per-PID shellouts BELOW that
// verdict, and the two are different scopes rather than one mechanism spelled
// twice: the verdict memo cannot help walk 2, which asks a different question
// (#1544) and re-derives its own answer from the same chain.
type ancestryProbe struct {
	pid      int
	reads    *ancestryReads
	resolved bool
	term     string
	hostPID  int
	complete bool
}

// host walks the ancestry once and memoizes the result. ctx is a parameter
// rather than a field of the memo on purpose: a context stored in a struct
// outlives the call it was scoped to, and this memo is passed between three
// guarded blocks that each already hold the caller's budget.
func (a *ancestryProbe) host(ctx context.Context) (term string, hostPID int) {
	if !a.resolved {
		a.term, a.hostPID, a.complete = resolveHostFromAncestry(ctx, a.pid, a.reads)
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
// and the separate bundle-id walk: false means one of them aborted on a probe
// it could not get an answer out of, so the fields it would have filled are
// missing rather than absent (#1492). A run in which neither was needed is
// complete by definition. The bundle-id walk aborts on an unreadable process
// AND on an unanswerable plutil (#1524); the memoized one has no plutil to
// make, so for it the two are the same thing.
func applyAncestryFallbacks(ctx context.Context, l *session.Launcher, pid int, ancestry *ancestryProbe) (complete bool) {
	return applyAncestryFallbacksVia(ctx, l, pid, ancestry, kittyWindowIDForPID)
}

// kittyWindowProbe is the `kitten @ ls` read applyKittyAncestryBackfill makes,
// injected for the same reason ttyProbe is: a real kitten cannot be driven over
// its ceiling on purpose.
type kittyWindowProbe func(ctx context.Context, socket string, sessionPID int) (string, bool)

// applyAncestryFallbacksVia is applyAncestryFallbacks with the kitty
// remote-control read injected.
func applyAncestryFallbacksVia(ctx context.Context, l *session.Launcher, pid int, ancestry *ancestryProbe, readKittyWindow kittyWindowProbe) (complete bool) {
	complete = true
	// kitty intentionally does not set TERM_PROGRAM (upstream kitty issue
	// #4793), so the env-captured value may be inherited from whatever
	// process launched kitty.app (e.g. a VS Code integrated terminal). When
	// KITTY_WINDOW_ID is present, kitty *is* the host of this session — but
	// we still verify via process ancestry to rule out the reverse case
	// (KITTY_WINDOW_ID leaked from a kitty shell that spawned VS Code).
	if l.KittyWindowID != "" && l.TermProgram != "kitty" {
		if term, _ := ancestry.host(ctx); term == "kitty" {
			l.TermProgram = "kitty"
		}
	}
	// Hardened-runtime processes (e.g. Anthropic's signed `claude` binary)
	// hide env from sysctl. Fall back to process-ancestry walking so the UI
	// can at least bring the host app to the front. Darwin-only; other
	// platforms return "" and this is a no-op.
	if l.TermProgram == "" {
		l.TermProgram, _ = ancestry.host(ctx)
	}
	// Generic host fallback: when no curated host matched (TermProgram still
	// empty), resolve the first top-level `.app` ancestor's bundle id so the
	// client can bring an embedded-terminal GUI host (e.g. Obsidian) to the
	// front without a per-app registry entry. Purely additive — it only runs
	// on a map miss, so every curated host keeps its exact behavior. Darwin-
	// only; other platforms return "" and this is a no-op.
	if l.TermProgram == "" {
		// ancestry.reads, not a fresh memo: the block above has already walked
		// this exact ppid chain, and handing walk 2 those reads is the whole of
		// #1544's second half. Every PID below was otherwise read twice.
		bundleID, _, ok := resolveHostBundleIDFromAncestry(ctx, pid, ancestry.reads)
		// AND, not assign: this bit is hand-accumulated three guarded blocks
		// down, and a block inserted above that also writes it would otherwise
		// be silently overwritten here — the failure ancestryProbe.walked()'s
		// doc describes for the sibling walk. Identical today (nothing writes
		// complete between its initialisation and here); the point is that it
		// stays identical after the next block lands.
		complete = complete && ok
		l.HostBundleID = bundleID
	}
	// Back-fill kitty fields for sessions whose own env is unreadable
	// (Apple-signed agents like `pi`, hardened-runtime binaries). If kitty
	// is the host per ancestry walk but env yielded no kitty signals,
	// derive them from kitty.app itself + its remote-control socket.
	// Without this, clicking the session in the UI raises kitty but can't
	// target the right tab — exactly the symptom reported for pi sessions
	// in issue #326.
	kittyProbed := applyKittyAncestryBackfill(ctx, l, pid, ancestry, readKittyWindow)
	return complete && ancestry.walked() && kittyProbed
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
//
// It makes TWO bounded shellouts, not one, and since #1537 both feed the
// caller's completeness bit: the ancestry `ps` chain behind ancestry.host()
// (read off the probe by the caller) and the `kitten @ ls` behind
// readKittyWindow (returned here). Only the first was reported before.
//
// Be exact about what that buys today, because it is less than it looks and
// the next reader must not conclude otherwise: NO consumer can currently
// observe the kitten half. This block only runs when TermProgram is already
// "kitty", and resolveClientHostIdentity returns a candidate with a non-empty
// TermProgram outright without reading complete; ReadLauncherEnv discards the
// bit entirely. So the verdict is carried for consistency with the walk beside
// it — a second probe in the same block whose verdict is dropped is how the
// first one went wrong — and it becomes load-bearing the moment a consumer
// reads completeness for a host-resolved candidate. The TTY fold in
// hostIdentityVia is NOT in this position: that one is reached by candidates
// with no TermProgram at all, which is exactly the branch that reads the bit.
func applyKittyAncestryBackfill(ctx context.Context, l *session.Launcher, pid int, ancestry *ancestryProbe, readKittyWindow kittyWindowProbe) (probed bool) {
	if l.TermProgram != "kitty" || l.KittyPID != 0 {
		return true
	}
	term, kpid := ancestry.host(ctx)
	if term != "kitty" || kpid <= 0 {
		return true
	}
	l.KittyPID = kpid
	if l.KittyListenOn == "" {
		l.KittyListenOn = kittyListenOnFor(kpid)
	}
	if l.KittyListenOn != "" && l.KittyWindowID == "" {
		id, ok := readKittyWindow(ctx, l.KittyListenOn, pid)
		l.KittyWindowID = id
		// #1537: the empty id from a kitten that never ran and the empty id
		// from a kitty with no matching window arrived here as the same value.
		// Returning the verdict is what lets the caller AND it into complete —
		// the sibling failure ancestryProbe.walked() exists for, one probe over.
		return ok
	}
	return true
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
