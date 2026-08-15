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

// TestHerdrReadFitsTheLivenessSweepTick is what makes herdrClientBudget's value
// a decision rather than a preference, and it is the reason #1529 was filed as
// a bug rather than as a doc fix.
//
// refreshHerdrHosts runs SYNCHRONOUSLY inside the SweepDeadPIDs ticker handler
// (CheckPIDLiveness → refreshHerdrHosts), and Go's Ticker drops ticks it
// overruns. So a herdr read that can outlast one tick delays dead-PID reaping
// for every session behind it — which is what the old bound, a COUNT over up to
// maxClientCandidates candidates each spending several 2s ceilings, allowed.
// The whole read is shelloutTimeout (the pane's own controlling-tty `ps`, the
// only child the direct read starts for a herdr pane) plus herdrClientBudget
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
func TestHerdrReadFitsTheLivenessSweepTick(t *testing.T) {
	tick := sweepBaseInterval(t)

	if read := shelloutTimeout + herdrClientBudget; read >= tick {
		t.Errorf("a herdr ReadLauncherEnv can take shelloutTimeout+herdrClientBudget = %v, "+
			"which does not fit inside the liveness sweep's fastest tick (%v, PIDManager.SweepDeadPIDs). "+
			"That sweep calls refreshHerdrHosts synchronously and Go's Ticker drops ticks it overruns, "+
			"so one herdr resolve would again delay dead-PID reaping for every session behind it — #1529 "+
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
			"longer be the one refreshHerdrHosts runs under, so re-point it rather than trusting it",
			sweepBaseIntervalSource)
	}
	m := sweepBaseIntervalDecl.FindSubmatch(src)
	if m == nil {
		t.Fatalf("%s no longer declares `const baseInterval = N * time.Unit`: the liveness sweep's "+
			"cadence is what bounds herdrClientBudget, and a test that cannot read it must say so "+
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
