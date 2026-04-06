package pi

import "testing"

func TestStatePolicy_DisableStaleToolTimer(t *testing.T) {
	p := StatePolicy()
	if p.EnableStaleToolTimer {
		t.Fatal("expected EnableStaleToolTimer=false for Pi")
	}
}
