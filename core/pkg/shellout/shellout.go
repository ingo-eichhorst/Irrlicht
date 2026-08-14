// Package shellout holds the one predicate every bounded child process in this
// repo classifies its error with: did the child ANSWER, or was it never asked?
//
// It is a leaf on purpose. The family this exists to prevent — #1485, #1492,
// #1513, #1524, #1533, #1537, #1543 — is one sentence seven times: "I could not
// look" collapsed into "I looked and there was nothing". #1538 gave that
// sentence a single implementation, but only inside
// core/adapters/inbound/agents/processlifecycle, so the eighth instance
// (#1543's git adapter, in an OUTBOUND adapter) could not reuse it without
// copying it. A pkg/ leaf is the smallest thing that makes it reachable from
// both directions of the hexagon; core/architecture_test.go's rule for pkg/ is
// "must not import adapters or application", and this package imports stdlib
// only.
//
// What it deliberately does NOT hold is a ceiling. A consent-path version
// check, a window-targeting probe and a `git log --all` over a user's whole
// history are entitled to different budgets, and each package names its own
// (core/pkg/cliprobe's Timeout, processlifecycle's shelloutTimeout,
// core/adapters/outbound/git's gitTimeout, control's execTimeout). Merging them
// would be picking one number by proximity, which is the unexamined copy this
// whole family is about.
package shellout

import (
	"errors"
	"os/exec"
	"slices"
)

// Answered reports whether the child process behind a bounded shellout actually
// ANSWERED, as opposed to never having been asked.
//
// It answers only that half. What to DO with a non-answer is per-call-site and
// deliberately not decided here: processlifecycle's IsKnownInteractiveHost
// gates ADMISSION and so fails open (#1513), while hostIdentity and the git
// adapter feed enrichment and so degrade to "unknown" (#1492, #1543). A shared
// verdict with per-site polarity is the point.
//
// answeredExitCodes is the per-tool part, and it is the only part that
// genuinely differs.
//
// Empty means "any normal exit is an answer". That is plutil's rule — it exits
// 1 both for a missing Info.plist and for a plist carrying no
// CFBundleIdentifier (both measured, #1524 and re-measured in #1538) — and it
// is git's rule too: every failure git reports as a real verdict exits
// non-zero but NORMALLY (measured on git 2.50.1 / Apple Git-155, darwin/arm64:
// not-a-repo 128 for all six of the adapter's subcommands, unborn-branch
// rev-parse 128, a range naming an unknown ref 128, `tag --contains` with an
// unresolvable object name 129, and "no tags"/"no reverts" 0 with empty
// stdout). git has no exit status that means "I could not look", so for git the
// non-answers are exactly: killed by the ceiling, never started, fork failed.
//
// A non-empty list is an allowlist of the non-zero exits that count as answers
// — lsof's and pgrep's rule, where exit 1 is "nothing to report" and any OTHER
// code is not evidence about anything. That distinction is load-bearing rather
// than cosmetic: `sh -c "exit 152"` (a process killed under an exhausted CPU
// limit reports 152 through a shell) exits NORMALLY, so ProcessState.Exited()
// is true and ExitCode() is 152 (measured, go1.25 darwin/arm64) — the empty
// form would call it an answer and lsofProbeRan's committed corpus pins that it
// is not.
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
// errors.As rather than a type assertion because an error that has been wrapped
// on the way here is still an exit status. No caller wraps today (os/exec hands
// back a bare *exec.ExitError), which is exactly why a bare assertion in
// runPgrep survived unnoticed until #1538 — it was correct by accident of its
// caller, not by construction.
func Answered(err error, answeredExitCodes ...int) bool {
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
