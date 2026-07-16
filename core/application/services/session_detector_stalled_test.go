package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// markStalledEditToolCase is one TestMarkStalledEditTool table row.
type markStalledEditToolCase struct {
	name      string
	openSince map[string]int64
	metrics   *session.SessionMetrics
	now       int64

	wantStalled bool

	// The open-since-window assertion is opt-in per case (most of the
	// original subtests didn't check it): checkOpenSince false means skip
	// it entirely; true + wantOpenSinceGone means the key must be absent;
	// true + !wantOpenSinceGone means it must equal wantOpenSince.
	checkOpenSince    bool
	wantOpenSinceGone bool
	wantOpenSince     int64
}

// markStalledEditToolCases builds TestMarkStalledEditTool's table. Split out
// of the test function itself so TestMarkStalledEditTool stays under
// CodeScene's Large Method line threshold (the table's fixtures are the bulk
// of its length, not test logic).
func markStalledEditToolCases(threshold int64) []markStalledEditToolCase {
	editOpen := func() *session.SessionMetrics {
		return &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}
	}
	editOpenPending := func() *session.SessionMetrics {
		m := editOpen()
		m.PermissionPending = true
		return m
	}

	return []markStalledEditToolCase{
		{
			name:           "fresh edit tool is not flagged, window recorded",
			openSince:      map[string]int64{},
			metrics:        editOpen(),
			now:            1000,
			wantStalled:    false,
			checkOpenSince: true,
			wantOpenSince:  1000,
		},
		{
			name:        "edit tool open past window is flagged",
			openSince:   map[string]int64{"s": 1000},
			metrics:     editOpen(),
			now:         1000 + threshold,
			wantStalled: true,
		},
		{
			// kiro-cli's pending write-approval picker holds an open lowercase
			// `write` tool; it must flag stalled just like claudecode's
			// PascalCase Write (#588).
			name:        "lowercase write (kiro) open past window is flagged",
			openSince:   map[string]int64{"s": 1000},
			metrics:     &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"write"}},
			now:         1000 + threshold,
			wantStalled: true,
		},
		{
			name:        "edit tool just under window is not flagged",
			openSince:   map[string]int64{"s": 1000},
			metrics:     editOpen(),
			now:         1000 + threshold - 1,
			wantStalled: false,
		},
		{
			name:        "permission-pending edit tool defers to the hook",
			openSince:   map[string]int64{"s": 1000},
			metrics:     editOpenPending(),
			now:         1000 + threshold + 100,
			wantStalled: false,
		},
		{
			name:              "non-edit tool is never flagged and clears the window",
			openSince:         map[string]int64{"s": 1000},
			metrics:           &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}},
			now:               1000 + threshold + 100,
			wantStalled:       false,
			checkOpenSince:    true,
			wantOpenSinceGone: true,
		},
		{
			name:              "closing the tool clears the window",
			openSince:         map[string]int64{"s": 1000},
			metrics:           &session.SessionMetrics{HasOpenToolCall: false},
			now:               1000 + threshold + 100,
			wantStalled:       false,
			checkOpenSince:    true,
			wantOpenSinceGone: true,
		},
	}
}

// TestMarkStalledEditTool covers the transcript-based stalled-edit-tool
// fallback (#488): an open permission-gated edit tool that lingers past
// stalledEditToolThreshold is flagged OpenToolStalled, while a fresh one, a
// non-edit tool, or one already covered by the hook is not. White-box so it
// can exercise the unexported method + editToolOpenSince map directly,
// injecting `now` instead of sleeping out the real window.
func TestMarkStalledEditTool(t *testing.T) {
	threshold := int64(stalledEditToolThreshold.Seconds())

	for _, tt := range markStalledEditToolCases(threshold) {
		t.Run(tt.name, func(t *testing.T) {
			d := &SessionDetector{editToolOpenSince: tt.openSince}
			d.markStalledEditTool("s", tt.metrics, tt.now)
			assertOpenToolStalled(t, tt.metrics, tt.wantStalled)
			if !tt.checkOpenSince {
				return
			}
			if tt.wantOpenSinceGone {
				assertOpenSinceCleared(t, d, "s")
				return
			}
			assertOpenSinceEquals(t, d, "s", tt.wantOpenSince)
		})
	}

	t.Run("nil metrics clears the window safely", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		d.markStalledEditTool("s", nil, 9999)
		assertOpenSinceCleared(t, d, "s")
	})
}

// assertOpenToolStalled fails the test if m.OpenToolStalled doesn't match want.
func assertOpenToolStalled(t *testing.T, m *session.SessionMetrics, want bool) {
	t.Helper()
	if m.OpenToolStalled != want {
		t.Fatalf("OpenToolStalled = %v, want %v", m.OpenToolStalled, want)
	}
}

// assertOpenSinceCleared fails the test if d.editToolOpenSince[key] is set.
func assertOpenSinceCleared(t *testing.T, d *SessionDetector, key string) {
	t.Helper()
	if got, ok := d.editToolOpenSince[key]; ok {
		t.Fatalf("expected open-since window cleared, got %d", got)
	}
}

// assertOpenSinceEquals fails the test if d.editToolOpenSince[key] isn't
// exactly want.
func assertOpenSinceEquals(t *testing.T, d *SessionDetector, key string, want int64) {
	t.Helper()
	if got, ok := d.editToolOpenSince[key]; !ok || got != want {
		t.Fatalf("open-since window: got (%d,%v), want (%d,true)", got, ok, want)
	}
}

// TestMarkStalledEditTool_HeldPromptSequence covers a realistic sequence: a
// held Edit prompt observed across two passes flips to stalled on the second
// (a stale-refresh) pass; an arriving tool_result then clears the window.
func TestMarkStalledEditTool_HeldPromptSequence(t *testing.T) {
	threshold := int64(stalledEditToolThreshold.Seconds())
	d := &SessionDetector{editToolOpenSince: map[string]int64{}}
	start := time.Now().Unix()

	m1 := &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}
	d.markStalledEditTool("s", m1, start) // live tool_use write
	if m1.OpenToolStalled {
		t.Fatal("first observation must not be stalled")
	}

	m2 := &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}
	d.markStalledEditTool("s", m2, start+threshold) // stale-refresh
	if !m2.OpenToolStalled {
		t.Fatal("second (stale) observation must be stalled")
	}

	m3 := &session.SessionMetrics{HasOpenToolCall: false} // approval → tool_result
	d.markStalledEditTool("s", m3, start+threshold+1)
	if _, ok := d.editToolOpenSince["s"]; ok {
		t.Fatal("approval must clear the window")
	}
}

// TestMarkStalledEditTool_SlowEditNotFlagged reproduces issue #1130: a
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
func TestMarkStalledEditTool_SlowEditNotFlagged(t *testing.T) {
	editOpen := func() *session.SessionMetrics {
		return &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}
	}

	t.Run("slow-but-executing edit never flags across its lifetime", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{}}
		const start = int64(0)

		// T+0: tool_use observed, the open-since window is recorded.
		m0 := editOpen()
		d.markStalledEditTool("s", m0, start)
		assertOpenToolStalled(t, m0, false)

		// T+11s: a stale-refresh re-reads the still-open edit. This is the
		// exact instant the daemon misfired in the report; it must not flag.
		m11 := editOpen()
		d.markStalledEditTool("s", m11, start+11)
		assertOpenToolStalled(t, m11, false)

		// T+16.2s (17s whole): tool_result lands (is_error unset), the tool
		// closes. Still under threshold and now resolved, so the window clears
		// and nothing was ever flagged.
		m16 := &session.SessionMetrics{HasOpenToolCall: false}
		d.markStalledEditTool("s", m16, start+17)
		assertOpenToolStalled(t, m16, false)
		assertOpenSinceCleared(t, d, "s")
	})

	t.Run("genuinely stalled edit past threshold still flags (#488)", func(t *testing.T) {
		threshold := int64(stalledEditToolThreshold.Seconds())
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 0}}
		m := editOpen()
		d.markStalledEditTool("s", m, threshold) // open past the window, no tool_result
		assertOpenToolStalled(t, m, true)
	})
}
