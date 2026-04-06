package claudecode

import "testing"

func TestStatePolicy_EnableStaleToolTimer(t *testing.T) {
	p := StatePolicy()
	if !p.EnableStaleToolTimer {
		t.Fatal("expected EnableStaleToolTimer=true for Claude Code")
	}
}
