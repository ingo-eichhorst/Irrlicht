package processlifecycle

import (
	"context"
	"os/exec"
	"time"

	"irrlicht/core/pkg/shellout"
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

// probeAnswered is this package's binding of the shared predicate
// core/pkg/shellout.Answered, which every bounded child process in the repo
// classifies its error with (#1538). The implementation, and the measurements
// behind it — why a mid-run deadline kill is an *exec.ExitError that does NOT
// wrap context.DeadlineExceeded, and why ProcessState.Exited() rather than
// errors.Is is the ran-vs-killed discriminator — live there.
//
// It is a pure alias — same signature, same semantics, binding nothing — and
// saying so is better than the tidier story that it is "this package's binding
// of the empty-variadic form". That story is false: process_darwin.go calls
// probeAnswered(err, pgrepNoMatch), so the variadic is forwarded, not bound.
// lsofProbeRan is the only real binding in this package.
//
// The honest reason it survives #1543's promotion is churn: five call sites, a
// guard classifier table and a committed corpus all spell it this way, and
// #1547 argues that verified enforcement machinery is worth not disturbing for
// a rename. Deleting it — or narrowing it to a non-variadic form so it binds
// what its name suggests — is a reasonable follow-up; shellout_guard_test.go
// already recognises the qualified spelling, so nothing mechanical blocks it.
func probeAnswered(err error, answeredExitCodes ...int) bool {
	return shellout.Answered(err, answeredExitCodes...)
}
