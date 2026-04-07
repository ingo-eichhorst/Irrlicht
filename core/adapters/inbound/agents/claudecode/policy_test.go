package claudecode

import "testing"

// TestStatePolicy_DisablesStaleToolTimer pins the Claude Code adapter's
// decision to opt out of the stale-tool heuristic (see issue #102). If this
// is ever flipped back to true without a new permission-pending signal,
// long-running Bash tools will flicker the session to waiting again.
func TestStatePolicy_DisablesStaleToolTimer(t *testing.T) {
	p := StatePolicy()
	if p.EnableStaleToolTimer {
		t.Fatal("expected EnableStaleToolTimer=false for Claude Code (issue #102)")
	}
}
