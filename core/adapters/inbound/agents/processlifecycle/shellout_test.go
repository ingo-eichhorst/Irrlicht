package processlifecycle

import (
	"os/exec"
	"testing"

	"irrlicht/core/pkg/shellout"
)

// TestProbeAnsweredForwardsToTheSharedPredicate is a LOCK, and a thin one on
// purpose: #1543 moved the corpus that grades this behaviour to
// core/pkg/shellout, where the implementation now lives. What stays here is
// the one claim that is local — that this package's spelling still reaches
// that implementation, in both the empty and the allowlist form — so a
// forwarder quietly rewritten to its own logic fails here rather than nowhere.
func TestProbeAnsweredForwardsToTheSharedPredicate(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		codes []int
	}{
		{name: "nil"},
		{name: "exit 1", err: exec.Command("/bin/sh", "-c", "exit 1").Run()},
		{name: "exit 1, allowlisted", err: exec.Command("/bin/sh", "-c", "exit 1").Run(), codes: []int{1}},
		{name: "exit 152 against an allowlist of 1", err: exec.Command("/bin/sh", "-c", "exit 152").Run(), codes: []int{1}},
		{name: "binary missing", err: exec.Command("/nonexistent/irrlicht-1543").Run()},
	}
	for _, tc := range cases {
		if got, want := probeAnswered(tc.err, tc.codes...), shellout.Answered(tc.err, tc.codes...); got != want {
			t.Errorf("%s: probeAnswered = %v, shellout.Answered = %v — the local spelling no longer "+
				"forwards to the shared predicate", tc.name, got, want)
		}
	}
}
