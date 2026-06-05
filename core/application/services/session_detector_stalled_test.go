package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestMarkStalledEditTool covers the transcript-based stalled-edit-tool
// fallback (#488): an open permission-gated edit tool that lingers past the
// stale-refresh interval is flagged OpenToolStalled, while a fresh one, a
// non-edit tool, or one already covered by the hook is not. White-box so it
// can exercise the unexported method + editToolOpenSince map directly,
// injecting `now` instead of sleeping out the real 5s window.
func TestMarkStalledEditTool(t *testing.T) {
	threshold := int64(staleWorkingRefreshInterval.Seconds())

	editOpen := func() *session.SessionMetrics {
		return &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}
	}

	t.Run("fresh edit tool is not flagged, window recorded", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{}}
		m := editOpen()
		d.markStalledEditTool("s", m, 1000)
		if m.OpenToolStalled {
			t.Fatal("fresh edit tool must not be flagged stalled")
		}
		if got, ok := d.editToolOpenSince["s"]; !ok || got != 1000 {
			t.Fatalf("open-since window: got (%d,%v), want (1000,true)", got, ok)
		}
	})

	t.Run("edit tool open past window is flagged", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		m := editOpen()
		d.markStalledEditTool("s", m, 1000+threshold)
		if !m.OpenToolStalled {
			t.Fatalf("edit tool open for %ds must be flagged stalled", threshold)
		}
	})

	// kiro-cli's pending write-approval picker holds an open lowercase `write`
	// tool; it must flag stalled just like claudecode's PascalCase Write (#588).
	t.Run("lowercase write (kiro) open past window is flagged", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		m := &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"write"}}
		d.markStalledEditTool("s", m, 1000+threshold)
		if !m.OpenToolStalled {
			t.Fatalf("lowercase write open for %ds must be flagged stalled", threshold)
		}
	})

	t.Run("edit tool just under window is not flagged", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		m := editOpen()
		d.markStalledEditTool("s", m, 1000+threshold-1)
		if m.OpenToolStalled {
			t.Fatal("edit tool under the window must not be flagged stalled")
		}
	})

	t.Run("permission-pending edit tool defers to the hook", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		m := editOpen()
		m.PermissionPending = true
		d.markStalledEditTool("s", m, 1000+threshold+100)
		if m.OpenToolStalled {
			t.Fatal("must not set OpenToolStalled when PermissionPending already fired")
		}
	})

	t.Run("non-edit tool is never flagged and clears the window", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		m := &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}
		d.markStalledEditTool("s", m, 1000+threshold+100)
		if m.OpenToolStalled {
			t.Fatal("a long-running Bash must not be flagged stalled")
		}
		if _, ok := d.editToolOpenSince["s"]; ok {
			t.Fatal("non-edit tool must clear the open-since window")
		}
	})

	t.Run("closing the tool clears the window", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		m := &session.SessionMetrics{HasOpenToolCall: false}
		d.markStalledEditTool("s", m, 1000+threshold+100)
		if m.OpenToolStalled {
			t.Fatal("a closed tool must not be flagged stalled")
		}
		if _, ok := d.editToolOpenSince["s"]; ok {
			t.Fatal("closing the tool must clear the open-since window")
		}
	})

	t.Run("nil metrics clears the window safely", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{"s": 1000}}
		d.markStalledEditTool("s", nil, 9999)
		if _, ok := d.editToolOpenSince["s"]; ok {
			t.Fatal("nil metrics must clear the open-since window")
		}
	})

	// Realistic sequence: a held Edit prompt observed across two passes flips
	// to stalled on the second (a stale-refresh) pass; an arriving tool_result
	// then clears the window.
	t.Run("held prompt sequence", func(t *testing.T) {
		d := &SessionDetector{editToolOpenSince: map[string]int64{}}
		start := time.Now().Unix()

		m1 := editOpen()
		d.markStalledEditTool("s", m1, start) // live tool_use write
		if m1.OpenToolStalled {
			t.Fatal("first observation must not be stalled")
		}

		m2 := editOpen()
		d.markStalledEditTool("s", m2, start+threshold) // stale-refresh
		if !m2.OpenToolStalled {
			t.Fatal("second (stale) observation must be stalled")
		}

		m3 := &session.SessionMetrics{HasOpenToolCall: false} // approval → tool_result
		d.markStalledEditTool("s", m3, start+threshold+1)
		if _, ok := d.editToolOpenSince["s"]; ok {
			t.Fatal("approval must clear the window")
		}
	})
}
