//go:build darwin

package processlifecycle

import (
	"context"
	"encoding/json"
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
// children often don't), plus whether the ps lookup ANSWERED at all — those
// were one value until #1533, and merging them reported a ps the ceiling
// killed as "this process has no terminal", silently and permanently for that
// identity. The result is normalized
// to match Terminal.app's AppleScript `tty` property format — `ps -o tty=`
// on macOS omits the "/dev/" prefix that AppleScript returns. This is host
// enrichment (window targeting), not observation, so other platforms stub it.
func processTTY(ctx context.Context, pid int) (string, bool) {
	return processTTYVia(ctx, pid, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, psPath, "-o", "tty=", "-p", strconv.Itoa(pid))
	})
}

// processTTYVia is processTTY with the shellout injected. The second return is
// #1533: whether ps actually answered.
//
// The two empty strings below used to be one. "ps could not be run" and "this
// process has no controlling terminal" are the family's usual pair, and only
// the second is a verdict — a hardened-runtime child genuinely has no tty, and
// reporting one anyway would misroute a click.
//
// No answeredExitCodes: for ps ANY normal exit is an answer. It exits 1 for a
// pid it cannot find (measured), which is a real "no such process", not a
// probe that did not run — so the allowlist form would turn every reaped pid
// into a non-answer and poison hostIdentity's completeness for it.
func processTTYVia(ctx context.Context, pid int, build shelloutCmd) (tty string, probed bool) {
	if pid <= 0 {
		// Nothing was asked, so nothing failed — the same reading bundleIDVia
		// gives an empty appPath.
		return "", true
	}
	out, err := runProbe(ctx, probePSTTY, build)
	if !probeAnswered(err) {
		return "", false
	}
	tty = strings.TrimSpace(string(out))
	if tty == "" || tty == "?" || tty == "??" || tty == "-" {
		return "", true
	}
	if !strings.HasPrefix(tty, "/dev/") {
		tty = "/dev/" + tty
	}
	return tty, true
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
// (#1492) — or, since #1529, ctx's aggregate budget expiring first, which
// produces the same non-answer for the same reason. The caller that acts on the
// distinction is resolveClientHostIdentity.
//
// Intentionally ignores tmux: tmux's env vars (TMUX, TMUX_PANE) come from
// the regular env-capture path when readable, and a tmux-only ancestor
// (without a known host terminal above it) can't be brought to the front
// by NSWorkspace.
// reads is the per-resolve dedup of the `ps` reads this walk shares with
// resolveHostBundleIDFromAncestry (#1544). It is never nil on this platform —
// every caller builds one with newAncestryReads — and the walk goes through it
// rather than calling readProcInfo directly, because a walk that reaches its
// own read is a walk walk 2 cannot share.
func resolveHostFromAncestry(ctx context.Context, pid int, reads *ancestryReads) (termProgram string, hostPID int, complete bool) {
	return resolveHostFromAncestryVia(ctx, pid, reads.probe)
}

// resolveHostFromAncestryVia is resolveHostFromAncestry with the per-PID read
// injected — the same idiom resolveHostBundleIDVia has had since #1524, and
// needed here for the same reason: no arrangement of live processes can drive a
// read into a chosen failure, or be counted, on purpose.
func resolveHostFromAncestryVia(ctx context.Context, pid int, procInfo procInfoProbe) (termProgram string, hostPID int, complete bool) {
	cur := pid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		ppid, cmd, err := procInfo(ctx, cur)
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

// newAncestryReads is the production binding of the per-resolve read memo:
// darwin is the one platform whose ancestry walks actually shell out.
func newAncestryReads() *ancestryReads { return newAncestryReadsVia(readProcInfo) }

// bundleIDCmd builds the shellout for one named Info.plist. See bundleIDVia
// for why this one site takes a factory where the others take a shelloutCmd.
type bundleIDCmd func(plist string) shelloutCmd

// plutilBundleIDCmd is the production shellout for one app bundle's
// CFBundleIdentifier. `plutil` ships with macOS and reads both XML and binary
// Info.plists; bundles under /Applications are world-readable, so this needs
// no TCC consent.
func plutilBundleIDCmd(plist string) shelloutCmd {
	return func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, plutilPath, "-extract", "CFBundleIdentifier", "raw", "-o", "-", plist)
	}
}

// bundleIDForAppPath returns the CFBundleIdentifier of the application bundle
// at appPath (".../<App>.app"). Same bounded 2-second exec ceiling as the
// sibling ps helpers.
//
// The error is non-nil ONLY when plutil could not be ASKED, never when it
// answered — see bundleIDVia for why those are different facts and where the
// line between them falls (#1524).
//
// Since #1544 an ANSWER is memoized for the life of the process (bundleIDMemo
// below). The memo sits in FRONT of runProbe rather than inside it, so a hit
// starts no child, spends none of the caller's aggregate budget, and is invisible
// to shellout_guard_test.go's two rules — the run site is still the single
// `runProbe` call inside bundleIDVia, and it still classifies its own error.
func bundleIDForAppPath(ctx context.Context, appPath string) (string, error) {
	return bundleIDs.resolve(ctx, appPath, bundleIDUncached)
}

// bundleIDUncached is the plutil read itself, split out so the memo below has
// something to wrap and so a test can drive the memo without a real bundle.
func bundleIDUncached(ctx context.Context, appPath string) (string, error) {
	return bundleIDVia(ctx, appPath, plutilBundleIDCmd)
}

// bundleIDMemo caches app-path → CFBundleIdentifier for the life of the
// process. It is #1544's first half, and its LIFETIME is the opposite of
// ancestryReads' (osutil.go) — the pairing is the whole design, so read them
// together:
//
//   - A CFBundleIdentifier is immutable for the life of a bundle. It is the
//     name LaunchServices, the keychain and every preferences domain key off,
//     so an app that changed it across an update would break its own state.
//     That is what makes a process-global memo defensible HERE and nowhere
//     near ancestryReads, whose subject — the process table — changes by the
//     second.
//   - Process-global therefore means shared between goroutines: PID discovery
//     and the liveness sweep both reach it (see refreshHerdrHosts' own comment
//     on running outside assignMu). It is synchronised with an RWMutex, the
//     same idiom herdrClientCache uses one file down.
//
// It carries no TTL and no eviction, and both are deliberate. No TTL because
// the entry describes something immutable — herdrClientCache's 5s exists
// precisely because ITS subject (who is attached to a herdr socket) is not.
// No eviction because the key space is the set of top-level `.app` bundles in
// one machine's process ancestry: a handful, bounded by what is installed and
// running, and reached only through topLevelAppPath — which already rejects
// every nested helper bundle.
//
// The two ways an entry could go stale, stated rather than waved past, since a
// dismissal is worth what its evidence is worth:
//
//   - An in-place app replacement that also CHANGES the bundle id. Requires the
//     daemon to outlive the update and the developer to have renamed their own
//     identifier; the memo would then name the previous id until restart.
//   - A path reused by a DIFFERENT app (`/Applications/Foo.app` removed, an
//     unrelated Foo.app installed). Same window, same restart.
//
// Neither is mitigated, and the cost of being wrong is bounded: the value feeds
// click-to-focus enrichment and the #784 embedded-host allow-list, not any
// persisted state.
//
// UNVERIFIED, and marked as such because #1544 carried the claim forward from
// #1538 without a profile: that the daemon seed makes N identical plutil calls
// with N persisted sessions under one host. What #1544 DID measure is the
// per-call cost — 9.7ms median, p99 13.8ms, n=300, warm, on one machine — and
// the call shape: one plutil per ancestry resolve that reaches a top-level
// `.app`, so N such resolves under one host collapse to one. Note that is a
// long way from #1524's "2.2ms median" for the same exec on the same class of
// machine; neither figure is produced by anything that would keep it honest, so
// treat both as snapshots rather than constants.
type bundleIDMemo struct {
	mu  sync.RWMutex
	ids map[string]string
}

// bundleIDs is the one process-global instance. Tests build their own rather
// than reaching for this, so nothing in the suite depends on the order tests
// run in — the exception is the wiring lock, which asserts that
// bundleIDForAppPath populates exactly this one.
var bundleIDs = &bundleIDMemo{ids: map[string]string{}}

// lookup reports what plutil last ANSWERED for appPath.
func (m *bundleIDMemo) lookup(appPath string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, hit := m.ids[appPath]
	return id, hit
}

// record stores an answer. Only resolve calls it, and only on a nil error.
func (m *bundleIDMemo) record(appPath, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ids[appPath] = id
}

// resolve answers from the memo or runs probe, storing what probe ANSWERED.
//
// The admission rule is **memoize answers including the empty one, never
// memoize a non-answer**, and the two halves are separately load-bearing:
//
//   - The EMPTY answer is admitted. "" here means plutil ran and declined —
//     this ancestor has no bundle id we can name (bundleIDVia says where that
//     line falls). It is a verdict the walk continues on, so re-paying an exec
//     to be told it again is exactly what this memo exists to stop.
//   - A NON-answer is never admitted. That is the bug this family keeps
//     producing (#1485, #1492, #1513, #1524, #1533, #1537), and storing one
//     here would be strictly worse than every previous instance: those were
//     transient, and a cache entry is not. One loaded moment would freeze
//     "this ancestor is not an app" for the life of the daemon, which is the
//     verdict that REJECTS at the #784 admission gate and that SessionDetector
//     then caches in hostGateRejected and never evicts.
//
// #1514 reached the OPPOSITE conclusion for herdrClientCache — it memoizes
// non-answers on purpose — and the two must not be read as a contradiction,
// because they turn on a property this cache does not have. #1514's rule is
// that a non-answer may be cached only when every consumer sees it AS a
// non-answer: herdrClientLauncher returns `(*session.Launcher, bool)` and the
// memo carries that second bool through, so a cached "I could not look" is
// still a "I could not look" at the call site, and #1514 additionally guards
// the write so a non-answer never displaces a live answer. This cache has no
// such channel to carry: its consumers are ancestry walks whose only use of the
// value is `bid != ""`, so a stored non-answer would arrive as the empty
// ANSWER — indistinguishable, permanent, and wrong. The rule differs because
// the consumer does, not because one of the two is a mistake.
func (m *bundleIDMemo) resolve(ctx context.Context, appPath string, probe bundleIDProbe) (string, error) {
	if id, hit := m.lookup(appPath); hit {
		// #1534, and the sibling of ancestryReads.probe's line: a hit starts no
		// child, so the counter at runProbe cannot see it. Counting it here is
		// what keeps "answered" from silently meaning "answered, of the ones
		// that were not memoized" — #1544 flagged exactly this in its own
		// hand-back.
		observeProbeMemoHit(probePlutilBundleID)
		return id, nil
	}
	id, err := probe(ctx, appPath)
	if err != nil {
		return "", err
	}
	m.record(appPath, id)
	return id, nil
}

// bundleIDVia is bundleIDForAppPath with the shellout injected.
//
// It reports ("", nil) for an app whose bundle id plutil ANSWERED that it
// cannot supply, and ("", err) for one plutil never answered about at all —
// the #1524 distinction, and the reason this helper has an error return where
// the original swallowed everything into "".
//
// The line is drawn at "did the child run to a normal exit", NOT at the
// context error, and both halves of that are load-bearing:
//
//   - A non-zero exit IS an answer. plutil exits 1 both for a missing
//     Info.plist and for a plist that exists but carries no CFBundleIdentifier
//     (measured), and neither is evidence that the machine was too loaded to
//     look — treating either as a non-answer would fail the admission gate open
//     for any ancestor with an unusual bundle, widening #784 rather than
//     narrowing #1524.
//   - A kill IS NOT an answer, and `errors.Is(err, context.DeadlineExceeded)`
//     does not detect one. When CommandContext's deadline fires mid-run the
//     child is SIGKILLed and Output returns *exec.ExitError "signal: killed";
//     that error does not wrap the context's, so errors.Is reports false
//     (measured on go1.25, darwin/arm64). Keying on it would compile, read
//     correctly, and miss the exact condition #1524 is about. ProcessState
//     .Exited() is what separates a process that ran from one that was killed,
//     and it also covers the deaths the ceiling did not cause — an OOM kill, a
//     fork that failed under the same load, or a ctx already past its deadline
//     before Start (where the error is not an ExitError at all).
//
// build is a FACTORY producing a shelloutCmd rather than one directly, and it
// is the only site in the package that needs to be: the command depends on the
// Info.plist path, which this function derives from appPath. Everywhere else
// the site's arguments are closed over at the call site (see shelloutCmd).
func bundleIDVia(ctx context.Context, appPath string, build bundleIDCmd) (string, error) {
	if appPath == "" {
		return "", nil
	}
	plist := appPath + "/Contents/Info.plist"
	out, err := runProbe(ctx, probePlutilBundleID, build(plist))
	if err != nil {
		// No answeredExitCodes: for plutil ANY normal exit is an answer, which
		// is the empty-variadic form's whole reason for existing — see
		// probeAnswered, and the paragraph above for why exit 1 in particular
		// must stay one.
		if probeAnswered(err) {
			// plutil ran and declined: this ancestor has no bundle id we can
			// name. A real, readable verdict — the walk continues on it.
			return "", nil
		}
		return "", fmt.Errorf("plutil %s: %w", plist, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveHostBundleIDFromAncestry walks the parent-process chain of pid and
// returns the CFBundleIdentifier of the first top-level application bundle it
// finds, plus that app's PID. It is the generic fallback used when the curated
// termProgramByAppName map matches no ancestor — it lets the UI bring an
// embedded-terminal GUI host (e.g. Obsidian) to the front without a per-app
// registry entry. Returns ("", 0) when no top-level app appears within
// maxAncestry levels. complete carries the same meaning as
// resolveHostFromAncestry's, and the first read is split out of the `ppid <= 1`
// guard for exactly that reason: "pid's parent is init" is a verdict, "pid
// could not be read" is not.
//
// This walk makes TWO bounded shellouts per ancestor, not one, and both feed
// that bit: the `ps` every walk makes, and the `plutil` behind
// bundleIDForAppPath. Reporting only the first is #1524 — a plutil over its
// ceiling returned an empty bundle id, the walk read it as "this ancestor is
// not an app", and the miss it eventually reported was indistinguishable from
// a walk that had actually seen every ancestor.
//
// It aborts at the FIRST ancestor whose bundle id it could not get, rather than
// walking on to look for an outer one. That is deliberate and it is the one
// place this walk takes a different polarity from resolveClientHostIdentity's
// "a found host wins outright": here the first top-level app IS the host, so an
// outer app that answers is not a better answer — it is the wrong window. An
// honest "I don't know" leaves click-to-focus with no target; carrying on would
// give it a confidently wrong one. It needs two top-level `.app` ancestors in
// one chain to matter at all, which the `Contents/Frameworks/` rejection in
// topLevelAppPath makes rare.
//
// The walk starts at pid's *parent*: the agent runs inside the host and is
// never the host itself, and an agent whose own binary lives in a top-level
// bundle (e.g. ClaudeCode.app) must not be mistaken for it.
//
// reads is the per-resolve read memo this walk shares with walk 1 (#1544), and
// the sharing is the point: both walks traverse the same ppid chain, so without
// it every PID here is read a second time. It is never nil on this platform.
func resolveHostBundleIDFromAncestry(ctx context.Context, pid int, reads *ancestryReads) (bundleID string, hostPID int, complete bool) {
	return resolveHostBundleIDVia(ctx, pid, reads.probe, bundleIDForAppPath)
}

// bundleIDProbe is the plutil half of the two bounded shellouts
// resolveHostBundleIDVia makes per ancestor (procInfoProbe, the other half,
// is declared in osutil.go beside the memo that dedups it). Both are
// parameters for the same reason isKnownInteractiveHostVia injects its two
// walks: no arrangement of live processes can drive either into a chosen
// failure on purpose.
type bundleIDProbe func(ctx context.Context, appPath string) (string, error)

// resolveHostBundleIDVia is resolveHostBundleIDFromAncestry with both probes
// injected.
func resolveHostBundleIDVia(ctx context.Context, pid int, procInfo procInfoProbe, bundleID bundleIDProbe) (bundleIDOut string, hostPID int, complete bool) {
	ppid, _, err := procInfo(ctx, pid)
	if err != nil {
		return "", 0, false
	}
	if ppid <= 1 {
		return "", 0, true
	}
	cur := ppid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		pp, cmd, err := procInfo(ctx, cur)
		if err != nil {
			return "", 0, false
		}
		if appPath := topLevelAppPath(cmd); appPath != "" {
			bid, err := bundleID(ctx, appPath)
			if err != nil {
				// #1524: the probe was never answered, so "" here says nothing
				// about this ancestor. Falling through would walk on and report
				// a completed miss — the verdict that REJECTS at the admission
				// gate, and that SessionDetector then caches forever.
				return "", 0, false
			}
			if bid != "" {
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
// Which of the three outcomes each evaluation reached IS counted (#1525): the
// walk is classified once, by hostGateOutcomeFrom, and this bool is derived
// from that classification rather than computed beside it. hostgate.go carries
// why the count lives here and not at the admission gate above, and what the
// bundle does with it. The per-probe non-answer counts underneath it are
// #1534's; the two figures are meant to reconcile, and #1574 is the known
// reason they can fail to.
//
// Measured on one machine, plutil stays ~25x under its ceiling even at 12x CPU
// oversubscription, so whatever triggers a real non-answer is not CPU pressure
// and is still unidentified.
//
// That is also why #1529 bounded the herdr client indirection and left THIS
// call site running under noAggregateBudget(): here an aggregate deadline would
// manufacture the very non-answer the gate fails open on, so it would move
// answers toward admitting — invisibly, per the paragraph above. The walks
// therefore remain bounded by a COUNT (two of them, up to maxAncestry hops
// each, every hop a `ps` and possibly a `plutil` at shelloutTimeout), which is
// a real standing hazard on the discovery path and is stated rather than
// implied. noAggregateBudget carries the polarity argument for both call sites
// side by side.
//
// "Complete" means every probe in the walk was ANSWERED, which is wider than
// "no readProcInfo call failed" — the walk also shells out to plutil (#1524).
// resolveHostBundleIDFromAncestry says why that mattered; bundleIDVia is where
// the line between an answer and a non-answer is drawn.
func IsKnownInteractiveHost(pid int) bool {
	return hostGateFor(pid).admits()
}

// hostGateFor is IsKnownInteractiveHost's outcome rather than its verdict, and
// is what the production entry point (HostGate, hostgate.go) evaluates.
//
// Both spellings go through this one function, so the logging gate and the bare
// predicate cannot disagree about a pid — the #1390 lesson, which is that "the
// thing under test" and "the thing the daemon builds" must be one object rather
// than two that are believed to match.
func hostGateFor(pid int) hostGateOutcome {
	return hostGateOutcomeSharingReads(noAggregateBudget(), pid, newAncestryReads(), bundleIDForAppPath)
}

// isKnownInteractiveHostSharingReads builds both walks over ONE per-resolve
// read memo and hands them to isKnownInteractiveHostVia (#1544).
//
// This is the call site the sharing matters most at, and the reason is the
// short-circuit one function down: walk 2 runs only when walk 1 found nothing
// AND completed, which for walk 1 means it walked the chain to its END. So walk
// 2 never reads a PID walk 1 did not already read — the duplication here is not
// partial, it is total. Measured on a synthetic chain that misses the curated
// map: 2 x depth `ps` execs, D duplicates at depth D, up to maxAncestry.
//
// Both probes are injected rather than the two walks, which is the difference
// from isKnownInteractiveHostVia below: that one exists so the ORDER between
// the walks is testable, this one so what the walks READ is countable. A test
// that injected walks could not observe a memo the walks are built out of.
func isKnownInteractiveHostSharingReads(ctx context.Context, pid int, reads *ancestryReads, bundleID bundleIDProbe) bool {
	return hostGateOutcomeSharingReads(ctx, pid, reads, bundleID).admits()
}

// hostGateOutcomeSharingReads is isKnownInteractiveHostSharingReads' outcome.
func hostGateOutcomeSharingReads(ctx context.Context, pid int, reads *ancestryReads, bundleID bundleIDProbe) hostGateOutcome {
	return hostGateOutcomeVia(ctx, pid,
		func(ctx context.Context, pid int) (string, int, bool) {
			return resolveHostFromAncestryVia(ctx, pid, reads.probe)
		},
		func(ctx context.Context, pid int) (string, int, bool) {
			return resolveHostBundleIDVia(ctx, pid, reads.probe, bundleID)
		})
}

// ancestryWalk is the shape resolveHostFromAncestry and
// resolveHostBundleIDFromAncestry share: a host string, the PID it was found
// at, and whether the walk reached its verdict rather than aborting.
type ancestryWalk func(ctx context.Context, pid int) (host string, hostPID int, complete bool)

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
func isKnownInteractiveHostVia(ctx context.Context, pid int, walkTerm, walkBundle ancestryWalk) bool {
	return hostGateOutcomeVia(ctx, pid, walkTerm, walkBundle).admits()
}

// hostGateOutcomeVia is isKnownInteractiveHostVia's outcome, and is the ONE
// place a gate evaluation is counted (#1525).
//
// Counted here rather than in the pure hostGateOutcomeFrom below, or in
// hostGateFor above, and the middle is the right place for a reason at each
// end. hostGateOutcomeFrom is reached by a table test with synthetic inputs and
// no walk behind it, so counting there would count decisions nobody made;
// hostGateFor is reached only with live ancestry, so counting there would leave
// every outcome but the machine's own unreachable from a test. This function is
// the innermost point that both performs one complete evaluation and can be
// driven to any of the three by injecting the walks.
func hostGateOutcomeVia(ctx context.Context, pid int, walkTerm, walkBundle ancestryWalk) hostGateOutcome {
	term, _, complete := walkTerm(ctx, pid)
	bundleID := ""
	if term == "" && complete {
		// Only reached when walk 1 ran to a verdict, so this also skips the
		// duplicate ps shellouts whenever the curated map already matched.
		bundleID, _, complete = walkBundle(ctx, pid)
	}
	return observeHostGate(hostGateOutcomeFrom(term, bundleID, complete))
}

// isKnownInteractiveHostFrom is the pure decision behind IsKnownInteractiveHost,
// split out so the allow-list logic can be tested with synthetic ancestry
// results instead of depending on whatever launched the test binary.
//
// Since #1525 it is derived from hostGateOutcomeFrom rather than deciding
// anything itself, so the bool the daemon acts on and the row the diagnostics
// bundle publishes come from ONE classification of ONE walk. A second copy of
// the allow-list check — one to decide, one to label — is the drift this repo
// keeps paying for elsewhere, and there is no version of it that fails
// visibly.
func isKnownInteractiveHostFrom(termProgram, bundleID string, complete bool) bool {
	return hostGateOutcomeFrom(termProgram, bundleID, complete).admits()
}

// hostGateOutcomeFrom is the three-way classification the gate's bool is
// derived from: which of the outcomes in hostgate.go this walk reached.
//
// complete is checked first and on its own: when the walk aborted, termProgram
// and bundleID are "" for a reason that says nothing about the host, so reading
// the allow-list at all would be reading a value that was never observed. That
// ordering is also what keeps the aborted arm distinguishable from the
// completed-miss arm — both carry the same two empty strings, and the
// completeness bit is the only thing separating a session admitted on no
// evidence from #784 doing its job.
//
// Pure, and deliberately does not count: it is reached by a table test with
// synthetic inputs and no walk behind it. hostGateOutcomeVia counts.
func hostGateOutcomeFrom(termProgram, bundleID string, complete bool) hostGateOutcome {
	if !complete {
		return hostGateWalkAborted
	}
	if termProgram != "" || knownEmbeddedHostBundleIDs[bundleID] {
		return hostGateHostMatched
	}
	return hostGateNoKnownHost
}

// resolveTermProgramFromAncestry is a thin wrapper that discards the host
// PID. Kept for the existing call site that only cares whether kitty (or any
// other host) appears in the chain; callers that also need the host PID
// should use resolveHostFromAncestry directly to avoid a second walk.
func resolveTermProgramFromAncestry(ctx context.Context, pid int) string {
	term, _, _ := resolveHostFromAncestry(ctx, pid, newAncestryReads())
	return term
}

// kittyAncestryPID is a thin wrapper returning only the kitty.app PID from
// the ancestry walk, or 0 when kitty is not the host. Used to back-fill
// `KittyPID` for sessions whose own env was unreadable by sysctl —
// Apple-signed binaries like `pi` (Python signed by Apple) and zsh hide
// their env even from non-TCC sysctl reads, so KITTY_PID never makes it
// into the env-derived launcher. Ancestry walking still works because we
// only read ppid + comm, not env.
func kittyAncestryPID(ctx context.Context, pid int) int {
	term, hostPID, _ := resolveHostFromAncestry(ctx, pid, newAncestryReads())
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
func kittyWindowIDForPID(ctx context.Context, socket string, sessionPID int) (string, bool) {
	return kittyWindowIDForPIDVia(ctx, socket, sessionPID, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, kittenPath, "@", "--to", socket, "ls")
	})
}

// kittyWindowIDForPIDVia is kittyWindowIDForPID with the shellout injected.
// The second return is #1537's sixth instance of the family: whether kitten
// answered at all.
//
// No answeredExitCodes: any normal exit of `kitten @ ls` is an answer — a
// non-zero one means kitty declined to describe its windows, which is a
// verdict about kitty, where a killed kitten is a verdict about nothing.
//
// The guard below reports PROBED, not a failure, and that split is deliberate.
// An absent kitten binary or an empty socket is a settled property of this
// installation — "there is no window id to be had here" — which is the same
// reading osutil_linux.go and osutil_other.go give their stubs. Calling it a
// failed probe would poison hostIdentity's completeness permanently on any
// machine where kitten is not on the trusted path, in exchange for nothing:
// only an invocation that actually ran and did not answer is new information.
func kittyWindowIDForPIDVia(ctx context.Context, socket string, sessionPID int, build shelloutCmd) (string, bool) {
	if kittenPath == "" || socket == "" || sessionPID <= 0 {
		return "", true
	}
	out, err := runProbe(ctx, probeKittenWindow, build)
	if !probeAnswered(err) {
		return "", false
	}
	return parseKittenLsForPID(out, sessionPID), true
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
func readProcInfo(ctx context.Context, pid int) (ppid int, cmd string, err error) {
	out, err := runProbe(ctx, probePSProcInfo, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, psPath, "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
	})
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
func herdrClientPIDs(ctx context.Context, socketPath string) (pids []int, probed bool) {
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
	out, err := runProbe(ctx, probeLsofHerdrClients, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, lsofPath, logPath)
	})
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
	return probeAnswered(err, lsofNothingToReport)
}

// lsofNothingToReport is lsof's "I looked and there is nothing to report" exit
// status. Named because it is the entire per-tool half of lsofProbeRan: the
// hard half — what separates a child that ran from one that was killed — is
// probeAnswered's and is shared with every other shellout in this package
// (#1538).
const lsofNothingToReport = 1

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
//
// Since #1529 it is a SANITY cap and no longer the de-facto time bound it had
// been: herdrClientBudget bounds the loop by duration, so a machine slow enough
// for four candidates to matter now stops on the clock instead of on the count.
// The cap still poisons the answer when it truncates (see
// resolveClientHostIdentity), because "we declined to look at the rest" is the
// same non-answer whichever bound produced it.
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
// #1529's aggregate budget arrives at the SAME conclusion by a third route, and
// getting its polarity backwards would have made this fix cause the misroute
// #1348/#1492 removed. ctx carries one deadline shared by the lsof scan and
// every candidate behind it, and a candidate abandoned on it is a candidate we
// declined to look at — so it poisons readAll exactly as an unreadable one
// does, and the caller is told (nil, false) = "I could not look". Two ways in,
// both covered:
//
//   - A candidate ABANDONED before it starts: the check below is why the loop
//     tests the budget itself rather than relying on the children to inherit
//     it. An expired ctx still lets every remaining candidate run three
//     shellouts that fail instantly, and — worse for correctness than for cost —
//     an aggregate that is only ever inherited is one no caller can observe as
//     a bound.
//   - A candidate CUT SHORT mid-flight: its own shellouts die on the shared
//     deadline, hostIdentity reports complete=false, and the existing #1492
//     line below poisons on it with no new code. That composition is the whole
//     reason the tri-state needed no fourth state.
//
// A candidate that resolved a host BEFORE the budget ran out still wins
// outright, on the same grounds as an unreadable-siblings answer: a found host
// is evidence regardless of what the rest of the list did.
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
//
// This wrapper is the loop paired with the PRODUCTION identity read, and it is
// the name the rest of this file's prose refers to. Say plainly what it is
// today rather than let a reader infer: nothing in production calls it —
// resolveHerdrClientLauncher reaches the Via form so it can name both probes at
// one wiring — and its callers are the #1492 tests, which drive real PIDs
// through the real hostIdentity and would otherwise have to name that probe
// themselves. So the production probe is named twice, one line apart, which is
// a drift worth knowing about and too small to be worth a seam.
func resolveClientHostIdentity(ctx context.Context, pids []int) (*session.Launcher, bool) {
	return resolveClientHostIdentityVia(ctx, pids, hostIdentity)
}

// hostIdentityProbe is the per-candidate identity read
// resolveClientHostIdentityVia makes. Injected for the reason ttyProbe,
// kittyWindowProbe and resolveHostBundleIDVia's two probes are: no arrangement
// of live processes can be driven to consume a shared budget at a chosen
// candidate, and #1529's whole subject is what the loop does when it has.
//
// It returns a NON-NIL launcher, as hostIdentity does for every input: the loop
// reads fields off it before deciding anything, and a probe that returned nil
// would panic rather than report a non-answer. "Nothing was determined" is the
// second return, never a nil first.
type hostIdentityProbe func(ctx context.Context, pid int) (*session.Launcher, bool)

// resolveClientHostIdentityVia is resolveClientHostIdentity with the
// per-candidate identity read injected.
func resolveClientHostIdentityVia(ctx context.Context, pids []int, identify hostIdentityProbe) (*session.Launcher, bool) {
	readAll := len(pids) <= maxClientCandidates
	if !readAll {
		pids = pids[:maxClientCandidates]
	}
	for _, pid := range pids {
		if ctx.Err() != nil {
			// #1529: the aggregate budget is gone and candidates are left. They
			// are candidates we declined to look at, which is the cap's case
			// rather than the detach case — so poison and stop, exactly as a
			// truncation does. Checked HERE rather than left to the children:
			// an expired deadline reaches them anyway, but only this check
			// turns "every probe failed" into "we stopped looking", and only
			// this check makes the aggregate a bound a caller can observe.
			//
			// #1558/#1534: counted here because this branch is invisible
			// everywhere else. It produces the same (nil, false) a failed lsof
			// produces, so on a machine where the scan is chronically
			// slow-but-successful a herdr host would never resolve and nothing
			// would say so. ONE event per loop, not one per candidate left —
			// the break means the loop never learns how many those were.
			observeHerdrCandidatesAbandoned()
			readAll = false
			break
		}
		observeHerdrCandidateProbed()
		host, complete := identify(ctx, pid)
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
// A miss costs at most herdrClientBudget, and that is a real ceiling rather
// than a sum of per-child ones (#1529): resolveHerdrClientLauncherVia derives
// ONE deadline covering the lsof scan and every candidate behind it. Before
// that the cost here was bounded by a COUNT — up to maxClientCandidates
// candidates, each two independent ancestry walks plus a tty `ps`, the
// bundle-id walk paying a `plutil` per hop up to maxAncestry — so one resolve
// could run to tens of seconds inside a 5s liveness-sweep tick. A cache HIT is
// free either way and is the common case; the budget bounds the miss.
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
//     readable, which is the whole herdrClientBudget spent before an answer
//     arrives. Re-paying now dominates, and the trade the rule was making
//     inverted. (Until #1529 that outcome had no ceiling at all — up to
//     maxClientCandidates candidates, two independent ancestry walks each on
//     readProcInfo's 2s ceiling, with a `plutil` per hop — which is why #1514's
//     memo made it rarer without making it shorter.)
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
	return resolveHerdrClientLauncherVia(socketPath, herdrClientPIDs, hostIdentity)
}

// clientPIDProbe is the attached-client scan resolveHerdrClientLauncherVia
// makes — the lsof half of the budget, injected so a test can drive the loop
// behind it without a live herdr server.
type clientPIDProbe func(ctx context.Context, socketPath string) (pids []int, probed bool)

// resolveHerdrClientLauncherVia is resolveHerdrClientLauncher with both probes
// injected. It DERIVES the aggregate deadline rather than taking one, so a test
// driving it exercises herdrClientBudget itself rather than a budget the test
// supplied — the same reason #1390 collapsed a receiver's two constructors into
// the one the daemon builds.
//
// One context spans both stages on purpose: two deadlines, one per stage, would
// read as a bound while summing to twice the budget, and what the herdr
// indirection costs is one number, so it is one context.
//
// What sharing COSTS, stated rather than argued away. The two stages compete
// for one budget and the scan goes first, so an lsof that runs to nearly
// herdrClientBudget and still SUCCEEDS leaves the loop nothing: every candidate
// is abandoned and the answer is (nil, false). On a machine where lsof is
// chronically slow-but-answering, a herdr host would then never resolve, where
// before #1529 it resolved slowly. Three things bound that, and none of them
// makes it go away:
//
//   - It fails to the SAFE side. A non-answer clears no stored host (#1492) and
//     the memo re-probes on the next sweep, so the cost is a deferred recovery
//     rather than a wrong answer.
//   - The band is narrow. An lsof at its own shelloutTimeout is normally KILLED
//     rather than slow, and a killed one is already (nil, false) via
//     lsofProbeRan — so only the slow-and-successful window is new.
//   - Nobody would see it. #1534 is that nothing counts probe non-answers, and
//     that now covers budget abandonment too.
//
// Splitting the budget between the stages is the obvious alternative and is
// deliberately NOT taken here: it needs evidence about the real distribution of
// scan times, which #1529 has none of. This is the same trade #1553 records for
// the git adapter — an unbounded call that answered slowly becomes a bounded
// one that does not answer at all — and it is written down for the same reason.
func resolveHerdrClientLauncherVia(socketPath string, scan clientPIDProbe, identify hostIdentityProbe) (*session.Launcher, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), herdrClientBudget)
	defer cancel()
	pids, probed := scan(ctx, socketPath)
	if !probed {
		return nil, false
	}
	return resolveClientHostIdentityVia(ctx, pids, identify)
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
// its own — and a non-answer is still by far the SLOWER outcome to produce: it
// is the one that can spend the whole herdrClientBudget, where an answer stops
// at the first candidate that resolves.
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
