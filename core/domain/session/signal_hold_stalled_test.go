package session

import (
	"testing"
	"time"
)

// This file is the behaviour suite for the SignalOpenToolStalled policy row
// (#488, #1130, #588). Before #1319 it lived in core/application/services as
// session_detector_stalled_test.go and drove the unexported markStalledEditTool
// method plus the editToolOpenSince map directly, because the rule was a
// hand-rolled per-session overlay on SessionDetector. The rule is now a policy
// row, so its tests live with the mechanism — matching the doctrine
// TestHasPendingIdlePrompt records: policy behaviour is tested here, and the
// detector keeps only a thin wiring test for the producer that arms the hold.
//
// Every case below is a LOCK: it pins behaviour the #1319 migration must not
// change, and each is a transcription of a case the white-box suite already
// asserted.

// editOpenMetrics builds metrics with an open permission-gated edit tool.
func editOpenMetrics() *SessionMetrics {
	return &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}
}

// armStalled places the SignalOpenToolStalled hold the way the detector's
// producer does: arm-once, at the moment the open tool was first observed.
func armStalled(h *SignalHolds, at time.Time) {
	h.HoldIfAbsent(holdSID, SignalOpenToolStalled, SignalPayload{}, at)
}

// stalledCase is one TestSignalHolds_OpenToolStalled table row.
type stalledCase struct {
	name string
	// unarmed skips placing the hold. The zero value — the case for every row
	// but one — arms it at holdT0, i.e. the open tool was first observed at
	// the start of the timeline.
	unarmed bool
	metrics *SessionMetrics
	// at is when the Overlay pass runs, relative to holdT0.
	at time.Duration

	wantStalled bool
	// wantHeld is whether the hold must survive this pass.
	wantHeld bool
}

// stalledCases builds TestSignalHolds_OpenToolStalled's table. Split out of the
// test function so it stays under CodeScene's Large Method line threshold.
func stalledCases() []stalledCase {
	pending := func() *SessionMetrics {
		m := editOpenMetrics()
		m.PermissionPending = true
		return m
	}

	return []stalledCase{
		{
			// The defect the threshold exists to prevent: a tool observed open
			// for the first time must NOT be flagged on the spot. A policy row
			// with an unconditional apply fires here, which is why `ripe` had
			// to exist before this rule could be a row at all (#1319).
			name:        "fresh edit tool is not flagged, hold armed",
			metrics:     editOpenMetrics(),
			at:          0,
			wantStalled: false,
			wantHeld:    true,
		},
		{
			name:        "edit tool open past the threshold is flagged",
			metrics:     editOpenMetrics(),
			at:          stalledEditToolThreshold,
			wantStalled: true,
			wantHeld:    true,
		},
		{
			// kiro-cli's pending write-approval picker holds an open lowercase
			// `write` tool; it must flag stalled just like claudecode's
			// PascalCase Write (#588).
			name:        "lowercase write (kiro) open past the threshold is flagged",
			metrics:     &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"write"}},
			at:          stalledEditToolThreshold,
			wantStalled: true,
			wantHeld:    true,
		},
		{
			name:        "edit tool just under the threshold is not flagged",
			metrics:     editOpenMetrics(),
			at:          stalledEditToolThreshold - time.Nanosecond,
			wantStalled: false,
			wantHeld:    true,
		},
		{
			// The hook is more authoritative; the flag is redundant once
			// PermissionPending fired. The hold is kept, not dropped: the
			// prompt may be released while the tool stays open.
			name:        "permission-pending edit tool defers to the hook",
			metrics:     pending(),
			at:          stalledEditToolThreshold + 100*time.Second,
			wantStalled: false,
			wantHeld:    true,
		},
		{
			name:        "non-edit tool is never flagged and drops the hold",
			metrics:     &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}},
			at:          stalledEditToolThreshold + 100*time.Second,
			wantStalled: false,
			wantHeld:    false,
		},
		{
			name:        "closing the tool drops the hold",
			metrics:     &SessionMetrics{HasOpenToolCall: false},
			at:          stalledEditToolThreshold + 100*time.Second,
			wantStalled: false,
			wantHeld:    false,
		},
		{
			name:        "no hold means nothing is ever flagged",
			unarmed:     true,
			metrics:     editOpenMetrics(),
			at:          stalledEditToolThreshold + 100*time.Second,
			wantStalled: false,
			wantHeld:    false,
		},
	}
}

// TestSignalHolds_OpenToolStalled covers the transcript-based
// stalled-edit-tool fallback (#488) as a signalPolicies row: an open
// permission-gated edit tool that lingers past stalledEditToolThreshold is
// flagged OpenToolStalled, while a fresh one, a non-edit tool, or one already
// covered by the hook is not.
func TestSignalHolds_OpenToolStalled(t *testing.T) {
	for _, tt := range stalledCases() {
		t.Run(tt.name, func(t *testing.T) {
			h := NewSignalHolds()
			if !tt.unarmed {
				armStalled(h, holdT0)
			}

			h.Overlay(holdSID, tt.metrics, holdT0.Add(tt.at))

			if tt.metrics.OpenToolStalled != tt.wantStalled {
				t.Fatalf("OpenToolStalled = %v, want %v", tt.metrics.OpenToolStalled, tt.wantStalled)
			}
			if got := h.Held(holdSID, SignalOpenToolStalled); got != tt.wantHeld {
				t.Fatalf("Held = %v, want %v", got, tt.wantHeld)
			}
		})
	}
}

// TestSignalHolds_OpenToolStalled_ArmingIsOnce pins the property that makes the
// threshold measurable at all: the producer re-observes the same open tool on
// every classify pass, so arming must not restart the clock. With plain Hold in
// the producer's place the deadline would be pushed out by one poll interval
// every poll and the flag could never fire.
func TestSignalHolds_OpenToolStalled_ArmingIsOnce(t *testing.T) {
	h := NewSignalHolds()

	armStalled(h, holdT0)
	// Every later pass re-arms while the tool stays open.
	for _, d := range []time.Duration{time.Second, 10 * time.Second, stalledEditToolThreshold - time.Second} {
		armStalled(h, holdT0.Add(d))
	}

	m := editOpenMetrics()
	h.Overlay(holdSID, m, holdT0.Add(stalledEditToolThreshold))
	if !m.OpenToolStalled {
		t.Fatal("re-arming must not restart the threshold clock")
	}
}

// TestSignalHolds_OpenToolStalled_HeldPromptSequence covers a realistic
// sequence: a held Edit prompt observed across two passes flips to stalled on
// the second (a stale-refresh) pass; an arriving tool_result then drops the
// hold.
func TestSignalHolds_OpenToolStalled_HeldPromptSequence(t *testing.T) {
	h := NewSignalHolds()

	// Live tool_use write.
	armStalled(h, holdT0)
	m1 := editOpenMetrics()
	h.Overlay(holdSID, m1, holdT0)
	if m1.OpenToolStalled {
		t.Fatal("first observation must not be stalled")
	}

	// Stale-refresh past the threshold.
	armStalled(h, holdT0.Add(stalledEditToolThreshold))
	m2 := editOpenMetrics()
	h.Overlay(holdSID, m2, holdT0.Add(stalledEditToolThreshold))
	if !m2.OpenToolStalled {
		t.Fatal("second (stale) observation must be stalled")
	}

	// Approval → tool_result: the tool closes and the hold goes.
	m3 := &SessionMetrics{HasOpenToolCall: false}
	h.Overlay(holdSID, m3, holdT0.Add(stalledEditToolThreshold+time.Second))
	if h.Held(holdSID, SignalOpenToolStalled) {
		t.Fatal("approval must drop the hold")
	}
}

// TestSignalHolds_OpenToolStalled_SlowEditNotFlagged reproduces issue #1130: a
// permission-gated Edit that is legitimately executing (not blocked on a
// prompt) must not be flagged OpenToolStalled just because it runs longer than
// the 5s poll cadence. The fixture pins the real timings from the report: the
// Edit opens at T+0, is still open when a stale-refresh re-reads it at T+11s,
// and completes successfully (tool_result, is_error unset) at T+16.2s. Across
// that whole span nothing is ever flagged, so ClassifyState never routes to
// waiting.
//
// The paired positive case confirms the #488 fallback is intact: an edit that
// stays open past stalledEditToolThreshold with no result still flags.
func TestSignalHolds_OpenToolStalled_SlowEditNotFlagged(t *testing.T) {
	t.Run("slow-but-executing edit never flags across its lifetime", func(t *testing.T) {
		h := NewSignalHolds()

		// T+0: tool_use observed, the hold is armed.
		armStalled(h, holdT0)
		m0 := editOpenMetrics()
		h.Overlay(holdSID, m0, holdT0)
		if m0.OpenToolStalled {
			t.Fatal("T+0 must not flag")
		}

		// T+11s: a stale-refresh re-reads the still-open edit. This is the
		// exact instant the daemon misfired in the report; it must not flag.
		armStalled(h, holdT0.Add(11*time.Second))
		m11 := editOpenMetrics()
		h.Overlay(holdSID, m11, holdT0.Add(11*time.Second))
		if m11.OpenToolStalled {
			t.Fatal("T+11s must not flag (#1130)")
		}

		// T+16.2s (17s whole): tool_result lands (is_error unset), the tool
		// closes. Still under the threshold and now resolved, so the hold drops
		// and nothing was ever flagged.
		m17 := &SessionMetrics{HasOpenToolCall: false}
		h.Overlay(holdSID, m17, holdT0.Add(17*time.Second))
		if m17.OpenToolStalled {
			t.Fatal("T+17s must not flag")
		}
		if h.Held(holdSID, SignalOpenToolStalled) {
			t.Fatal("a resolved tool must drop the hold")
		}
	})

	t.Run("genuinely stalled edit past the threshold still flags (#488)", func(t *testing.T) {
		h := NewSignalHolds()
		armStalled(h, holdT0)
		m := editOpenMetrics()
		h.Overlay(holdSID, m, holdT0.Add(stalledEditToolThreshold))
		if !m.OpenToolStalled {
			t.Fatal("an edit open past the threshold with no result must flag")
		}
	})
}
