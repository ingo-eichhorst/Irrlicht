package opencode

import (
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// The opencode 2.x todowrite fold (#2109 P5b-2, the maintainer's Q3: parity
// with 1.x). testdata/v2-todowrite.rows.sql is one sandbox session of 2.1.4
// with experimental.todo on: six assistant steps, each carrying one completed
// todowrite tool item whose state.input.todos is the whole list.
const (
	v2TodowriteRows = "testdata/v2-todowrite.rows.sql"
	sesTodowrite    = "ses_e43f67908ffeKem89OZv1IwzMl"
)

// v2Tasks folds sesTodowrite's rows as they stand through ComputeMetrics — the
// 2.x store fold the daemon runs — and returns the session's tasks.
func v2Tasks(t *testing.T, holdAfterSeq int) []session.Task {
	t.Helper()
	w, db := newV2Store(t)
	loadV2Fixture(t, db, v2TodowriteRows)
	if holdAfterSeq > 0 {
		holdBackAfter(t, db, sesTodowrite, holdAfterSeq)
	}
	m, err := ComputeMetrics(w.transcriptPathFor(sesTodowrite), "")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics(%s) = %v, %v; want the session's metrics", sesTodowrite, m, err)
	}
	return m.Tasks
}

func assertTasks(t *testing.T, got []session.Task, want [][3]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Tasks = %+v (%d), want %d tasks %v — the 2.x todowrite items were not folded", got, len(got), len(want), want)
	}
	for i, w := range want {
		if got[i].ID != w[0] || got[i].Subject != w[1] || got[i].Status != w[2] {
			t.Errorf("Tasks[%d] = {ID:%q Subject:%q Status:%q}, want {ID:%q Subject:%q Status:%q}",
				i, got[i].ID, got[i].Subject, got[i].Status, w[0], w[1], w[2])
		}
	}
}

// TestComputeMetrics_V2TodowriteTasks is the fold's red-first test: before the
// 2.x branch learned todowrite, every step below folded to no task at all.
func TestComputeMetrics_V2TodowriteTasks(t *testing.T) {
	t.Run("mid_turn_snapshot_carries_all_three_statuses", func(t *testing.T) {
		// Rows up to seq 29: the third todowrite call's list is
		// completed / in_progress / pending.
		assertTasks(t, v2Tasks(t, 29), [][3]string{
			{"1", "First task", "completed"},
			{"2", "Second task", "in_progress"},
			{"3", "Third task", "pending"},
		})
	})
	t.Run("whole_session_ends_with_the_last_snapshot", func(t *testing.T) {
		assertTasks(t, v2Tasks(t, 0), [][3]string{
			{"1", "First task", "completed"},
			{"2", "Second task", "completed"},
			{"3", "Third task", "completed"},
		})
	})
}

// TestParser_V2TodowriteSnapshotReconciles drives the parser row by row, as the
// replay reads an exported transcript, and checks the per-row snapshot: the
// first call creates three tasks, a later one only updates them.
func TestParser_V2TodowriteSnapshotReconciles(t *testing.T) {
	p := &Parser{}
	first := p.ParseLine(v2Row(t, "assistant",
		`{"content":[{"type":"tool","id":"c1","name":"todowrite","state":{"status":"completed","input":{"todos":[`+
			`{"content":"First task","status":"pending"},{"content":"Second task","status":"pending"},{"content":"Third task","status":"pending"}]}}}]}`))
	if n := countOps(first, tailer.TaskOpCreate); n != 3 {
		t.Fatalf("first todowrite: %d create deltas (%+v), want 3", n, first.TaskDeltas)
	}
	if first.TaskSnapshot == nil || len(*first.TaskSnapshot) != 3 {
		t.Fatalf("first todowrite: snapshot %v, want 3 entries", first.TaskSnapshot)
	}
	second := p.ParseLine(v2Row(t, "assistant",
		`{"content":[{"type":"tool","id":"c2","name":"todowrite","state":{"status":"completed","input":{"todos":[`+
			`{"content":"First task","status":"completed"},{"content":"Second task","status":"in_progress"},{"content":"Third task","status":"pending"}]}}}]}`))
	if n := countOps(second, tailer.TaskOpCreate); n != 0 {
		t.Errorf("second todowrite: %d create deltas, want 0 — the same contents are the same tasks", n)
	}
	if n := countOps(second, tailer.TaskOpUpdate); n != 2 {
		t.Errorf("second todowrite: %d update deltas (%+v), want 2 (completed, in_progress)", n, second.TaskDeltas)
	}
	// A tool that is not todowrite folds no task.
	other := p.ParseLine(v2Row(t, "assistant",
		`{"content":[{"type":"tool","id":"c3","name":"shell","state":{"status":"completed","input":{"todos":[{"content":"x","status":"pending"}]}}}]}`))
	if len(other.TaskDeltas) != 0 || other.TaskSnapshot != nil {
		t.Errorf("a shell tool item folded tasks: %+v / %v", other.TaskDeltas, other.TaskSnapshot)
	}
}

// countOps counts ev's task deltas of one op (tailer.TaskOpCreate/Update).
func countOps(ev *tailer.ParsedEvent, op string) int {
	n := 0
	for _, d := range ev.TaskDeltas {
		if d.Op == op {
			n++
		}
	}
	return n
}
