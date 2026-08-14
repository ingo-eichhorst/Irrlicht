package processlifecycle

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"time"
)

// shelloutTimeout is the ceiling EVERY bounded shellout in this package runs
// under. It was an unnamed `2*time.Second` literal in eight places until #1538,
// while three doc comments and a test assertion had already made the value
// load-bearing ("half of 2s") — so the number was a contract that nothing named.
//
// Two sibling packages already name their own copy and one of them points here
// for the justification: core/pkg/cliprobe's exported Timeout, and
// core/adapters/outbound/control's execTimeout, whose doc reads "matching the
// process observer's 2-second ceiling (process_darwin.go)" — a cross-reference
// to a constant this package did not have. Naming it here does not merge those
// (a consent-path version check and a window-targeting probe are entitled to
// different ceilings, and control's is a different layer); it gives that
// reference something to point AT.
//
// The value is a compromise between two failures the shellouts sit between: a
// probe that blocks session discovery, and a probe killed before it answers —
// and #1538's whole subject is that the second is only safe because
// probeAnswered reports it as a non-answer rather than as an empty one.
const shelloutTimeout = 2 * time.Second

// shelloutCmd builds one bounded child process. Every injected shellout in this
// package is one of these, and the site's own arguments are CLOSED OVER rather
// than passed through: a builder that took them would be handing the callee
// values it already holds, and no test has ever read one — every fixture in
// this package spells them `_`.
//
// Injection is the point, not the arguments. The distinction each caller draws
// is a property of the CHILD PROCESS — ran to a normal exit, versus killed or
// never started — which no faked return value can pin and no arrangement of
// live processes can be driven into on purpose. It began as bundleIDCmd
// (#1524); #1538 made it one type instead of five, because five signatures
// carrying arguments nobody reads is five chances to forget the ceiling.
type shelloutCmd func(ctx context.Context) *exec.Cmd

// probeAnswered reports whether the child process behind a bounded shellout
// actually ANSWERED, as opposed to never having been asked. It is the one
// predicate every shellout in this package classifies its error with (#1538),
// replacing three spellings that had drifted apart:
//
//   - lsofProbeRan's `err == nil || (errors.As(&exit) && exit.ExitCode() == 1)`
//   - bundleIDVia's `errors.As(&exit) && exit.ProcessState.Exited()`
//   - runPgrep's bare `err.(*exec.ExitError)` type assertion, which misses a
//     wrapped error for no reason anyone chose.
//
// The whole family it exists to prevent — #1485, #1492, #1513, #1524, #1533,
// #1537 — is one sentence six times: "I could not look" collapsed into "I
// looked and there was nothing". This predicate answers only the first half.
// What to DO with a non-answer is per-call-site and deliberately not decided
// here: IsKnownInteractiveHost gates ADMISSION and so fails open (#1513),
// while hostIdentity feeds a click target and so degrades an enrichment
// (#1492). A shared verdict with per-site polarity is the point.
//
// answeredExitCodes is the per-tool part, and it is the only part that
// genuinely differs. Empty means "any normal exit is an answer" — plutil's
// rule, because it exits 1 both for a missing Info.plist and for a plist
// carrying no CFBundleIdentifier (both measured: exit 1, #1524 and re-measured
// in #1538), so treating a non-zero exit as a non-answer would fail the
// admission gate OPEN and widen #784. A non-empty list is an allowlist of the
// non-zero exits that count as answers — lsof's and pgrep's rule, where exit 1
// is "nothing to report" and any OTHER code is not evidence about anything.
// That distinction is load-bearing rather than cosmetic: `sh -c "exit 152"`
// (a process killed under an exhausted CPU limit reports 152 through a shell)
// exits NORMALLY, so ProcessState.Exited() is true and ExitCode() is 152
// (measured, go1.25 darwin/arm64) — the empty form would call it an answer and
// lsofProbeRan's committed corpus pins that it is not.
//
// The hard half does NOT differ, and it is the half that is easy to get wrong:
//
//   - A killed child is not an answer, and `errors.Is(err,
//     context.DeadlineExceeded)` will not tell you. When CommandContext's
//     deadline fires MID-RUN the child is SIGKILLed and Output returns
//     *exec.ExitError "signal: killed", which does not wrap the context's
//     error: errors.Is reports FALSE while ctx.Err() reports the deadline
//     (measured independently in #1524 and again in #1538, go1.25
//     darwin/arm64). The natural-looking spelling compiles, reads correctly,
//     and misses the exact condition every issue in this family is about.
//   - ProcessState.Exited() is what separates a process that RAN from one that
//     was killed, and it covers the deaths the ceiling did not cause too: an
//     OOM kill, a fork that failed under the same load, and a ctx already past
//     its deadline before Start — where the error is not an ExitError at all
//     (measured: `errors.As` false, `errors.Is(DeadlineExceeded)` true).
//
// errors.As rather than a type assertion because an error that has been
// wrapped on the way here is still an exit status. No caller wraps today
// (os/exec hands back a bare *exec.ExitError), which is exactly why the bare
// assertion in runPgrep survived unnoticed — it was correct by accident of its
// caller, not by construction.
func probeAnswered(err error, answeredExitCodes ...int) bool {
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !exit.ProcessState.Exited() {
		// Killed, never started, or not a child-exit error at all. None of
		// these carry information about the question that was asked.
		return false
	}
	if len(answeredExitCodes) == 0 {
		return true
	}
	return slices.Contains(answeredExitCodes, exit.ExitCode())
}
