package processlifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// TestProbeAnswered is the corpus behind #1538's shared predicate.
//
// Every case is built from a REAL child process rather than from a hand-built
// *exec.ExitError, for the reason TestLsofProbeRan already records: the
// classification's whole job is to be right about what os/exec actually hands
// back, and a mid-run context deadline in particular is not obviously an
// ExitError at all. Asserting the VERDICT rather than the error type is what
// keeps this honest across Go versions. The one synthetic row is the wrapped
// error, which no os/exec path produces today and which is the point of that
// row (see below).
//
// The table pins both directions at once, which is why it is a matrix over the
// two forms rather than two tables:
//
//   - wantAny is the empty-variadic form ("any normal exit is an answer") —
//     plutil's rule, #1524.
//   - wantExit1 is the allowlist form probeAnswered(err, 1) — lsof's and
//     pgrep's rule.
//
// The two disagree on exactly one row, "exit 152", and that disagreement is
// the whole reason the variadic exists: a shell reports a process killed under
// an exhausted CPU limit as a NORMAL exit with status 152, so Exited() is true.
// A single global rule cannot serve both tools, and picking either one for
// both is a silent behaviour change at half the call sites.
func TestProbeAnswered(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) error {
		_, err := exec.CommandContext(ctx, name, args...).Output()
		return err
	}
	// 50ms rather than the 1ms TestLsofProbeRan uses: at 1ms the deadline
	// routinely fires BEFORE Start, which is a different row of this table.
	// The verdict is the same either way, so the row is correct regardless —
	// TestProbeAnsweredRejectsTheContextSpelling below is what actually
	// VERIFIES the mid-run shape, by asserting the error is an ExitError.
	deadline, cancelDeadline := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelDeadline()

	// A context already past its deadline before Start: os/exec never builds
	// an ExitError here, it returns the context's own error. errors.As is
	// false, and errors.Is(err, context.DeadlineExceeded) is TRUE — the mirror
	// image of the mid-run kill below, which is why keying on the context
	// catches this one and misses that one.
	expired, cancelExpired := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelExpired()
	time.Sleep(5 * time.Millisecond)

	cases := []struct {
		name      string
		err       error
		wantAny   bool
		wantExit1 bool
		why       string
	}{
		{
			name: "success", err: nil, wantAny: true, wantExit1: true,
			why: "the vacuity guard: a predicate that answered false here would make every other row pass for the wrong reason",
		},
		{
			name: "exit 1", err: run(context.Background(), "/bin/sh", "-c", "exit 1"),
			wantAny: true, wantExit1: true,
			why: "plutil's 'no CFBundleIdentifier' and lsof's 'nothing to report' are both real verdicts (#1524)",
		},
		{
			name: "exit 2", err: run(context.Background(), "/bin/sh", "-c", "exit 2"),
			wantAny: true, wantExit1: false,
			why: "an allowlist is an allowlist: for lsof/pgrep only exit 1 carries meaning, every other code is not evidence",
		},
		{
			name: "exit 152 — killed under an exhausted CPU limit, reported through a shell",
			err:  run(context.Background(), "/bin/sh", "-c", "exit 152"),
			// The one row where the two forms disagree, and the reason the
			// variadic is not cosmetic. Exited() is TRUE here (measured), so
			// the empty form calls it an answer.
			wantAny: true, wantExit1: false,
			why: "lsofProbeRan's committed corpus pins 152 as NOT an answer; a global Exited() rule would silently flip it",
		},
		{
			name: "killed by a signal", err: run(context.Background(), "/bin/sh", "-c", "kill -9 $$"),
			wantAny: false, wantExit1: false,
			why: "a SIGKILLed child ran but never answered — ExitCode() is -1 and Exited() is false",
		},
		{
			name: "context deadline fires MID-RUN", err: run(deadline, "/bin/sleep", "5"),
			wantAny: false, wantExit1: false,
			why: "#1538 trap 1: this is *exec.ExitError \"signal: killed\" and errors.Is(err, context.DeadlineExceeded) is FALSE — the natural spelling misses exactly this row",
		},
		{
			name: "context already expired before Start", err: run(expired, "/bin/sleep", "5"),
			wantAny: false, wantExit1: false,
			why: "not an ExitError at all; errors.As is false and the predicate must still refuse it",
		},
		{
			name: "binary missing", err: run(context.Background(), "/nonexistent/probe-1538"),
			wantAny: false, wantExit1: false,
			why: "a fork/exec failure is a probe that never ran, not a tool reporting nothing",
		},
		{
			name:    "an exit 1 that was WRAPPED on the way here",
			err:     fmt.Errorf("pgrep -x claude: %w", run(context.Background(), "/bin/sh", "-c", "exit 1")),
			wantAny: true, wantExit1: true,
			why: "the third spelling replaced by #1538 was a bare `err.(*exec.ExitError)` assertion, which reports false here; errors.As reports true. No os/exec path wraps today, so this row is a LOCK on the property rather than a live defect — it is what makes the bare assertion unrewritable without going red",
		},
	}

	for _, tc := range cases {
		if got := probeAnswered(tc.err); got != tc.wantAny {
			t.Errorf("%s: probeAnswered(%v) = %v, want %v — %s", tc.name, tc.err, got, tc.wantAny, tc.why)
		}
		if got := probeAnswered(tc.err, 1); got != tc.wantExit1 {
			t.Errorf("%s: probeAnswered(%v, 1) = %v, want %v — %s", tc.name, tc.err, got, tc.wantExit1, tc.why)
		}
	}
}

// TestProbeAnsweredRejectsTheContextSpelling is #1538's trap 1 stated as an
// assertion rather than as a doc comment.
//
// It exists because the wrong spelling is the natural one. A reader fixing this
// family reaches for errors.Is(err, context.DeadlineExceeded) — it names the
// exact condition, it compiles, and it reads correctly. This test measures that
// it is FALSE for the mid-run kill and TRUE only for the pre-Start case, so the
// evidence lives next to the predicate instead of in a merged PR body.
//
// It is deliberately an assertion about os/exec's behaviour, not about
// probeAnswered: if a future Go release makes CommandContext wrap the context's
// error, this test goes red and the doc comment above probeAnswered becomes
// wrong. That is the signal worth having.
func TestProbeAnsweredRejectsTheContextSpelling(t *testing.T) {
	deadline, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := exec.CommandContext(deadline, "/bin/sleep", "5").Output()
	if err == nil {
		t.Fatal("the ceiling did not fire — this test proves nothing without a killed child")
	}

	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("os/exec now wraps the context error (%v): probeAnswered's doc comment and #1524's measurement are stale", err)
	}
	if deadline.Err() != context.DeadlineExceeded {
		t.Errorf("ctx.Err() = %v, want DeadlineExceeded — the deadline is what actually fired", deadline.Err())
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *exec.ExitError for a mid-run kill, got %T", err)
	}
	if exit.ProcessState.Exited() {
		t.Error("a SIGKILLed child reports Exited() — the predicate's whole discriminator is gone")
	}
	if probeAnswered(err) {
		t.Error("probeAnswered called a killed child an answer (#1538)")
	}
}
