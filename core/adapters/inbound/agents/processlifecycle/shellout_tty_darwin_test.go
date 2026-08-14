//go:build darwin

package processlifecycle

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestProcessTTYVia_NonAnswerIsNotAnAbsentTerminal is #1533's primitive: the
// two empty strings processTTY used to return were "ps could not be run" and
// "this process has no controlling terminal", and only the second is a verdict.
//
// Rows 1-3 are LOCKS on the pre-#1533 behaviour — every shape that legitimately
// yields no tty must keep yielding one, and must keep reporting probed. Row 1
// is also the vacuity guard: a processTTYVia hard-wired to probed=false would
// satisfy row 4 while proving nothing, and one hard-wired to probed=true would
// satisfy rows 1-3 the same way, so both directions are needed.
func TestProcessTTYVia_NonAnswerIsNotAnAbsentTerminal(t *testing.T) {
	shell := func(script string) ttyCmd {
		return func(ctx context.Context, _ int) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sh", "-c", script)
		}
	}

	cases := []struct {
		name       string
		pid        int
		build      ttyCmd
		wantTTY    string
		wantProbed bool
		why        string
	}{
		{
			name: "a real tty", pid: 1, build: shell("echo ttys004"),
			wantTTY: "/dev/ttys004", wantProbed: true,
			why: "the vacuity guard: without a row that resolves, every row below passes for the wrong reason",
		},
		{
			name: "ps answers '??' — a hardened-runtime child with no terminal", pid: 1, build: shell("echo '??'"),
			wantTTY: "", wantProbed: true,
			why: "the LOCK #1533 names explicitly: this empty string is a verdict and must stay one",
		},
		{
			name: "ps exits 1 — no such process", pid: 1, build: shell("exit 1"),
			wantTTY: "", wantProbed: true,
			why: "ps exits 1 for a pid it cannot find (measured); that is a real answer, which is why this probe takes no exit-code allowlist",
		},
		{
			name: "killed by a signal", pid: 1, build: shell("kill -9 $$"),
			wantTTY: "", wantProbed: false,
			why: "#1533: a killed ps knows nothing about this process's terminal",
		},
		{
			name: "binary missing", pid: 1, build: func(ctx context.Context, _ int) *exec.Cmd {
				return exec.CommandContext(ctx, "/nonexistent/ps-1533")
			},
			wantTTY: "", wantProbed: false,
			why: "a fork/exec failure never reached the question",
		},
		{
			name: "pid <= 0 — nothing was asked", pid: 0, build: shell("echo unreachable"),
			wantTTY: "", wantProbed: true,
			why: "an empty input is not a failed probe, the same reading bundleIDVia gives an empty appPath",
		},
	}

	for _, tc := range cases {
		tty, probed := processTTYVia(tc.pid, tc.build)
		if tty != tc.wantTTY || probed != tc.wantProbed {
			t.Errorf("%s: processTTYVia = (%q, %v), want (%q, %v) — %s",
				tc.name, tty, probed, tc.wantTTY, tc.wantProbed, tc.why)
		}
	}
}

// TestHostIdentity_UnreadableTTYIsNotACompleteRead is #1533's consumer half,
// and the reason the primitive above is not enough on its own: processTTY
// already returned "" for an unreadable ps before this change, and hostIdentity
// reported the result as COMPLETE anyway — `l.TTY = processTTY(pid)` ran after
// complete was finalised and was never folded into it. A tri-state nobody reads
// is the same bug with more code.
//
// The vacuity guard is the second half: a working ps must still produce a
// complete read, or a hostIdentity hard-wired to incomplete would pass.
func TestHostIdentity_UnreadableTTYIsNotACompleteRead(t *testing.T) {
	answered := func(int) (string, bool) { return "/dev/ttys004", true }
	unanswered := func(int) (string, bool) { return "", false }

	if l, complete := hostIdentityVia(os.Getpid(), answered); !complete || l.TTY != "/dev/ttys004" {
		t.Errorf("a ps that answered: got (TTY %q, complete %v); want the read to complete", l.TTY, complete)
	}
	if _, complete := hostIdentityVia(os.Getpid(), unanswered); complete {
		t.Error("a ps that never answered was reported as a complete read of this process (#1533)")
	}
}

// TestProcessTTY_CeilingIsANonAnswer is the mid-run-kill row the table above
// cannot reach: its failures are a normal exit or a pre-Start error, and a
// child SIGKILLed by the ceiling is a third error shape (*exec.ExitError
// "signal: killed", which does not wrap the context's error). It costs the real
// shelloutTimeout, and the elapsed assertion is what proves it did not pass on
// the cheap branch — the same guard TestBundleIDVia_CeilingActuallyFires uses.
func TestProcessTTY_CeilingIsANonAnswer(t *testing.T) {
	stalled := func(ctx context.Context, _ int) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sleep", "30")
	}

	start := time.Now()
	tty, probed := processTTYVia(1, stalled)
	elapsed := time.Since(start)

	if probed {
		t.Errorf("a ps killed by its own ceiling reported tty %q as an answer (#1533)", tty)
	}
	if elapsed < shelloutTimeout/2 {
		t.Errorf("returned after %v, well under the %v ceiling: this is passing on the pre-start branch, not on the mid-run kill", elapsed, shelloutTimeout)
	}
}
