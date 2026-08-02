package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestArmStalledEditTool covers the producer half of the stalled-edit-tool
// fallback (#488): the detector observes that a permission-gated edit tool is
// open and arms the SignalOpenToolStalled hold. Everything past that — the
// stalledEditToolThreshold, the deference to a PermissionPending hook, dropping
// the hold when the tool closes — is policy behaviour and is covered in
// core/domain/session's signal_hold_stalled_test.go, where the mechanism lives.
//
// Before #1319 this rule was a hand-rolled editToolOpenSince map on
// SessionDetector and its whole behaviour table had to be tested here, through
// the unexported method and the map. Restating that table one layer up now
// would just duplicate the domain suite — the same split TestHasPendingIdlePrompt
// records for the idle-prompt hold.
func TestArmStalledEditTool(t *testing.T) {
	editOpen := func() *session.SessionMetrics {
		return &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}
	}
	armed := func(d *SessionDetector) bool {
		return d.signals.Held("s", session.SignalOpenToolStalled)
	}

	t.Run("an open edit tool arms the hold", func(t *testing.T) {
		d := &SessionDetector{signals: session.NewSignalHolds()}
		d.armStalledEditTool(&session.SessionState{SessionID: "s", Metrics: editOpen()}, holdT0)
		if !armed(d) {
			t.Fatal("an open permission-gated edit tool must arm the hold")
		}
	})

	t.Run("a non-edit tool arms nothing", func(t *testing.T) {
		d := &SessionDetector{signals: session.NewSignalHolds()}
		m := &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}
		d.armStalledEditTool(&session.SessionState{SessionID: "s", Metrics: m}, holdT0)
		if armed(d) {
			t.Fatal("a non-edit tool must not arm the hold")
		}
	})

	t.Run("nil metrics is safe and arms nothing", func(t *testing.T) {
		d := &SessionDetector{signals: session.NewSignalHolds()}
		d.armStalledEditTool(&session.SessionState{SessionID: "s"}, holdT0)
		if armed(d) {
			t.Fatal("a pass with no metrics must not arm the hold")
		}
	})

	// The reason the producer calls HoldIfAbsent rather than Hold. This runs on
	// every classify pass, so re-arming a still-open tool must not restart the
	// threshold clock — otherwise the deadline moves out by one poll interval
	// every poll and the flag can never come due.
	//
	// Deliberately framed without naming stalledEditToolThreshold, which is
	// unexported in core/domain/session: the re-arm happens a day after the
	// first observation and the pass runs a nanosecond after the re-arm, so the
	// elapsed time is either ~24h (arm-once — ripe under any sane threshold) or
	// ~1ns (re-armed — ripe under none). Retuning the threshold cannot make
	// this test lie in either direction.
	t.Run("re-observing an open tool does not restart the clock", func(t *testing.T) {
		d := &SessionDetector{signals: session.NewSignalHolds()}
		state := &session.SessionState{SessionID: "s", Metrics: editOpen()}

		d.armStalledEditTool(state, holdT0)
		state.Metrics = editOpen()
		d.armStalledEditTool(state, holdT0.Add(24*time.Hour))

		m := editOpen()
		d.signals.Overlay("s", m, holdT0.Add(24*time.Hour+time.Nanosecond))
		if !m.OpenToolStalled {
			t.Fatal("re-arming must not restart the threshold clock")
		}
	})
}
