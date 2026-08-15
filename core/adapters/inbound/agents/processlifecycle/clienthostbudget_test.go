package processlifecycle

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// sweepBaseIntervalSource is the file the liveness sweep's cadence is declared
// in. Relative to this package's directory, which is where `go test` runs.
const sweepBaseIntervalSource = "../../../../application/services/pid_manager.go"

// sweepBaseIntervalDecl matches the fastest cadence PIDManager.SweepDeadPIDs
// ticks at. It is a function-local const, so nothing can import it and nothing
// but reading the source can produce it.
var sweepBaseIntervalDecl = regexp.MustCompile(`const baseInterval = (\d+) \* time\.(Second|Millisecond|Minute)`)

// TestClientHostReadFitsTheLivenessSweepTick is what makes clientHostBudget's value
// a decision rather than a preference, and it is the reason #1529 was filed as
// a bug rather than as a doc fix.
//
// It covers BOTH producers, because since #1501 there is one budget rather
// than one per multiplexer (see clientHostBudget for that decision) — so one
// assertion over one constant is the whole coverage, and a second constant
// would be the thing that could drift past this tick unobserved.
//
// What it does NOT cover, stated because a reader would otherwise assume the
// arithmetic is exhaustive: a tmux pane's DIRECT read is not shelloutTimeout.
// hostIdentity skips the ancestry fallbacks for a herdr pane and deliberately
// runs them for a tmux one (a kitty launched from a pane has no other source of
// a host), so a tmux session pays the ordinary non-herdr direct read — two
// walks bounded by a COUNT, which is what noAggregateBudget names and what
// #1529 deliberately did not move. In practice that walk terminates at the
// reparented tmux server in two or three hops; in the worst case it is the
// standing cost noAggregateBudget's doc records for every non-herdr session,
// and #1501 neither adds to it nor fixes it.
//
// refreshMultiplexerHosts runs SYNCHRONOUSLY inside the SweepDeadPIDs ticker handler
// (CheckPIDLiveness → refreshMultiplexerHosts), and Go's Ticker drops ticks it
// overruns. So a herdr read that can outlast one tick delays dead-PID reaping
// for every session behind it — which is what the old bound, a COUNT over up to
// maxClientCandidates candidates each spending several 2s ceilings, allowed.
// The whole read is shelloutTimeout (the pane's own controlling-tty `ps`, the
// only child the direct read starts for a herdr pane) plus clientHostBudget
// (the entire client indirection), so that sum is what has to fit.
//
// The cadence is READ OUT OF THE SERVICES SOURCE rather than copied here. A
// number a test carries and nothing produces is the drift this repo has been
// bitten by more than once, and the coupling is real in both directions: this
// fails if the budget is widened past the tick, and equally if the tick is
// shortened below the budget. An adapter may not import the application layer,
// and `baseInterval` is a function-local const that could not be imported even
// if it could — so the source is the only place the number exists.
//
// It refuses rather than skips when it cannot find that declaration: a gate
// whose inability to look is indistinguishable from a pass is the failure mode
// this repo's guards exist to remove.
func TestClientHostReadFitsTheLivenessSweepTick(t *testing.T) {
	tick := sweepBaseInterval(t)

	if read := shelloutTimeout + clientHostBudget; read >= tick {
		t.Errorf("a pane's ReadLauncherEnv can take shelloutTimeout+clientHostBudget = %v, "+
			"which does not fit inside the liveness sweep's fastest tick (%v, PIDManager.SweepDeadPIDs). "+
			"That sweep calls refreshMultiplexerHosts synchronously and Go's Ticker drops ticks it overruns, "+
			"so one client resolve would again delay dead-PID reaping for every session behind it — #1529 "+
			"exactly, with a duration bound instead of a count", read, tick)
	}
}

// sweepBaseInterval extracts the sweep's fastest cadence from the services
// source, failing loudly if the file, the function or the declaration has moved
// — in which case this test's coupling needs re-pointing, and reporting "no
// finding" would be a lie.
func sweepBaseInterval(t *testing.T) time.Duration {
	t.Helper()
	src, err := os.ReadFile(sweepBaseIntervalSource)
	if err != nil {
		t.Fatalf("read %s: %v — this test measures the sweep cadence rather than restating it, "+
			"so it cannot run at all without that file", sweepBaseIntervalSource, err)
	}
	if !regexp.MustCompile(`func \(pm \*PIDManager\) SweepDeadPIDs\(`).Match(src) {
		t.Fatalf("%s no longer declares PIDManager.SweepDeadPIDs: the cadence this test reads may no "+
			"longer be the one refreshMultiplexerHosts runs under, so re-point it rather than trusting it",
			sweepBaseIntervalSource)
	}
	m := sweepBaseIntervalDecl.FindSubmatch(src)
	if m == nil {
		t.Fatalf("%s no longer declares `const baseInterval = N * time.Unit`: the liveness sweep's "+
			"cadence is what bounds clientHostBudget, and a test that cannot read it must say so "+
			"rather than pass", sweepBaseIntervalSource)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil || n <= 0 {
		t.Fatalf("parsed a nonsensical sweep interval %q from %s", m[1], sweepBaseIntervalSource)
	}
	units := map[string]time.Duration{
		"Millisecond": time.Millisecond,
		"Second":      time.Second,
		"Minute":      time.Minute,
	}
	unit, ok := units[string(m[2])]
	if !ok {
		t.Fatalf("unrecognised time unit %q in %s", m[2], sweepBaseIntervalSource)
	}
	return time.Duration(n) * unit
}
