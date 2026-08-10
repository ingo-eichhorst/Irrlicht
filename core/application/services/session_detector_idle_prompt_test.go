package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestHasPendingIdlePrompt covers the guard forceReadyToWorkingIfActive reads to
// skip the ready→working bounce on the idle-prompt hook's synthetic reclassify
// (#1173) — the one part of the idle-prompt path that is detector code rather
// than signal-policy behaviour.
//
// The policies themselves (consume-once vs persistent, each staleness rule, the
// load-bearing application order) are covered in core/domain/session's
// signal_hold_test.go, where the mechanism lives. Before #1288 they could only
// be reached through the detector, so their tests lived here; driving a domain
// type through an inert SessionDetector shell would now just duplicate that
// suite one layer up.
func TestHasPendingIdlePrompt(t *testing.T) {
	d := newSessionDetector()
	d.signals.Hold("has", session.SignalIdlePrompt, session.SignalPayload{}, holdT0)

	if !d.hasPendingIdlePrompt("has") {
		t.Error("hasPendingIdlePrompt must report true for a session with a pending signal")
	}
	if d.hasPendingIdlePrompt("none") {
		t.Error("hasPendingIdlePrompt must report false for a session with no signal")
	}
}
