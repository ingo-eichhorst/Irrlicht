package shellout

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// These are LOCKS, and saying so is the point rather than a disclaimer: #1543
// moved this implementation out of processlifecycle without changing a line of
// it, so there is no "before the fix" for them to have been red against.
// processlifecycle's TestProbeAnswered — an 8-row matrix over both variadic
// forms, every case built from a real child process, and carrying no build tag
// so it runs on Linux CI too — still grades this code through the forwarder.
//
// What these add is that the leaf is graded AT ITS OWN LOCATION. A package
// whose only coverage arrives through two importers stops being covered the
// moment either stops importing it, and "no test files" is what a reviewer sees
// in the meantime.
//
// Every case is built from a real child process on purpose. The distinction
// under test — ran to a normal exit, versus killed or never started — is a
// property of the process, and a hand-constructed error cannot pin it: an
// *exec.ExitError's ProcessState is not something a test can forge.

func TestAnswered_RealChildProcesses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		build func(t *testing.T) error
		codes []int
		want  bool
		why   string
	}{
		{
			name:  "clean exit is an answer",
			build: func(*testing.T) error { return exec.Command("/bin/sh", "-c", "exit 0").Run() },
			want:  true,
		},
		{
			name:  "exit 1, empty variadic",
			build: func(*testing.T) error { return exec.Command("/bin/sh", "-c", "exit 1").Run() },
			want:  true,
			why:   "plutil's and git's rule: any NORMAL exit is an answer, however unhappy",
		},
		{
			name:  "exit 1, allowlisted",
			build: func(*testing.T) error { return exec.Command("/bin/sh", "-c", "exit 1").Run() },
			codes: []int{1},
			want:  true,
			why:   "lsof's and pgrep's rule: 1 means 'nothing to report'",
		},
		{
			name:  "exit 152, allowlist of {1}",
			build: func(*testing.T) error { return exec.Command("/bin/sh", "-c", "exit 152").Run() },
			codes: []int{1},
			want:  false,
			why: "152 is what a process killed under an exhausted CPU limit reports through a shell. " +
				"It EXITS NORMALLY, so Exited() is true and a global Exited()-only rule would call it " +
				"an answer — which is the whole reason the allowlist exists rather than being cosmetic",
		},
		{
			name:  "exit 152, empty variadic",
			build: func(*testing.T) error { return exec.Command("/bin/sh", "-c", "exit 152").Run() },
			want:  true,
			why:   "the other side of the row above: with no allowlist, 152 IS an answer",
		},
		{
			name: "killed mid-run by the context deadline",
			build: func(*testing.T) error {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				return exec.CommandContext(ctx, "/bin/sleep", "30").Run()
			},
			want: false,
			why:  "the case every issue in this family is about",
		},
		{
			name: "context already expired before Start",
			build: func(*testing.T) error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
				defer cancel()
				time.Sleep(time.Millisecond)
				return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0").Run()
			},
			want: false,
			why:  "not an ExitError at all — errors.As is false, so the first guard is what catches it",
		},
		{
			name:  "binary does not exist",
			build: func(*testing.T) error { return exec.Command("/nonexistent/irrlicht-1543").Run() },
			want:  false,
			why:   "nothing started, so there is no exit status to read",
		},
		{
			name:  "nil error",
			build: func(*testing.T) error { return nil },
			want:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.build(t)
			if got := Answered(err, tc.codes...); got != tc.want {
				t.Errorf("Answered(%v, %v) = %v, want %v — %s", err, tc.codes, got, tc.want, tc.why)
			}
		})
	}
}

// TestAnswered_RejectsTheContextSpelling pins the trap that makes the natural
// implementation wrong: a child SIGKILLed when CommandContext's deadline fires
// returns an *exec.ExitError that does NOT wrap the context's error, so
// errors.Is(err, context.DeadlineExceeded) is FALSE while ctx.Err() reports the
// deadline.
//
// A predicate written as `!errors.Is(err, context.DeadlineExceeded)` compiles,
// reads correctly, and misses exactly the condition this package exists for.
// Measured independently in #1524, again in #1538, and re-run here.
func TestAnswered_RejectsTheContextSpelling(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := exec.CommandContext(ctx, "/bin/sleep", "30").Run()

	if err == nil {
		t.Fatal("the sleep was not killed; this machine did not reproduce the case under test")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("the mid-run kill DOES wrap context.DeadlineExceeded on this toolchain — the " +
			"doc comment's justification for ProcessState.Exited() no longer holds and must be re-measured")
	}
	if ctx.Err() == nil {
		t.Error("the context did not expire; the fixture is not arranging the case under test")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("expected an *exec.ExitError, got %T", err)
	}
	if exit.ProcessState.Exited() {
		t.Error("a SIGKILLed child reported Exited()==true; Exited() is the discriminator this " +
			"package relies on")
	}
	if Answered(err) {
		t.Error("a child killed by the deadline was reported as having answered")
	}
}

// TestAnswered_UnwrapsAWrappedExitStatus pins errors.As over a type assertion.
// Nothing in the repo wraps today — os/exec hands back a bare *exec.ExitError —
// which is precisely why a bare assertion survived unnoticed in runPgrep until
// #1538: it was correct by accident of its caller, not by construction.
func TestAnswered_UnwrapsAWrappedExitStatus(t *testing.T) {
	t.Parallel()

	raw := exec.Command("/bin/sh", "-c", "exit 1").Run()
	if raw == nil {
		t.Fatal("expected a non-nil error from exit 1")
	}
	wrapped := fmt.Errorf("running the probe: %w", raw)

	if !Answered(wrapped) {
		t.Error("a wrapped exit status was not recognised; an error wrapped on the way here is " +
			"still an exit status")
	}
	if Answered(wrapped, 2) && !Answered(raw, 2) {
		t.Error("the wrapped and unwrapped forms disagreed about the allowlist")
	}
	if Answered(wrapped, 2) {
		t.Error("exit 1 was accepted against an allowlist of {2}")
	}
}
