package tailer

import (
	"testing"
	"time"

	"irrlicht/core/pkg/capacity"
)

// taskStampTestParser lifts synthetic "task" / "snap" fields off the line
// into ParsedEvent task deltas and snapshots, so these tests exercise only
// the tailer's stamping of CompletedAt (#604) — real TaskCreate/TaskUpdate
// parsing is covered in the claudecode adapter package.
type taskStampTestParser struct{}

func (p *taskStampTestParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{Timestamp: ParseTimestamp(raw), EventType: "assistant_message"}
	if v, ok := raw["task"].(map[string]interface{}); ok {
		d := TaskDelta{}
		d.Op, _ = v["op"].(string)
		d.ID, _ = v["id"].(string)
		d.Subject, _ = v["subject"].(string)
		d.Status, _ = v["status"].(string)
		ev.TaskDeltas = []TaskDelta{d}
	}
	if v, ok := raw["snap"].([]interface{}); ok {
		snap := make([]TaskSnapshotEntry, 0, len(v))
		for _, e := range v {
			m, _ := e.(map[string]interface{})
			id, _ := m["id"].(string)
			status, _ := m["status"].(string)
			snap = append(snap, TaskSnapshotEntry{ID: id, Status: status})
		}
		ev.TaskSnapshot = &snap
	}
	return ev
}

func newTaskStampTestTailer(path string) *TranscriptTailer {
	tl := NewTranscriptTailer(path, &taskStampTestParser{}, "claude-code")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

// tsUnix parses the ts(offset)-formatted string back to unix seconds so the
// expected stamp matches what the parser saw.
func tsUnix(t *testing.T, stamp string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Unix()
}

func TestTailer_TaskCompletedAt_StampedOnDeltaEdge(t *testing.T) {
	doneAt := ts(5)
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "task": map[string]interface{}{"op": "create", "subject": "build"}},
		{"timestamp": ts(2), "task": map[string]interface{}{"op": "update", "id": "1", "status": "in_progress"}},
		{"timestamp": doneAt, "task": map[string]interface{}{"op": "update", "id": "1", "status": "completed"}},
	})
	m, err := newTaskStampTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tasks) != 1 || m.Tasks[0].Status != TaskStatusCompleted {
		t.Fatalf("tasks = %+v, want one completed task", m.Tasks)
	}
	if got, want := m.Tasks[0].CompletedAt, tsUnix(t, doneAt); got != want {
		t.Errorf("CompletedAt = %d, want %d (event time of the completed transition)", got, want)
	}
}

func TestTailer_TaskCompletedAt_EdgeOnly(t *testing.T) {
	// A re-asserted completed status must not move the original stamp, and
	// an in_progress transition must not stamp at all.
	doneAt := ts(3)
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "task": map[string]interface{}{"op": "create", "subject": "build"}},
		{"timestamp": doneAt, "task": map[string]interface{}{"op": "update", "id": "1", "status": "completed"}},
		{"timestamp": ts(9), "task": map[string]interface{}{"op": "update", "id": "1", "status": "completed"}},
	})
	tl := newTaskStampTestTailer(path)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Tasks[0].CompletedAt, tsUnix(t, doneAt); got != want {
		t.Errorf("CompletedAt = %d, want %d (re-assert must keep the first stamp)", got, want)
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"timestamp": ts(10), "task": map[string]interface{}{"op": "create", "subject": "test"},
	})
	appendTranscriptLine(t, path, map[string]interface{}{
		"timestamp": ts(11), "task": map[string]interface{}{"op": "update", "id": "2", "status": "in_progress"},
	})
	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.Tasks[1].CompletedAt != 0 {
		t.Errorf("CompletedAt = %d, want 0 (in_progress must not stamp)", m.Tasks[1].CompletedAt)
	}
}

// AppliedTaskDeltas surfaces the deltas the tailer folded in DURING a pass, and
// is reset each pass — so a re-scan that reads no new bytes surfaces none. That
// per-pass contract is what lets the detector record each task_delta lifecycle
// event exactly once (#662).
func TestTailer_AppliedTaskDeltas_SurfacedPerPass(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "task": map[string]interface{}{"op": "create", "subject": "build"}},
		{"timestamp": ts(2), "task": map[string]interface{}{"op": "update", "id": "1", "status": "completed"}},
	})
	tl := newTaskStampTestTailer(path)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.AppliedTaskDeltas) != 2 {
		t.Fatalf("AppliedTaskDeltas = %+v, want 2 (create + update)", m.AppliedTaskDeltas)
	}
	if d := m.AppliedTaskDeltas[0]; d.Op != "create" || d.Subject != "build" || d.Status != TaskStatusPending {
		t.Errorf("create delta = %+v", d)
	}
	if d := m.AppliedTaskDeltas[1]; d.Op != "update" || d.Status != TaskStatusCompleted {
		t.Errorf("update delta = %+v", d)
	}

	// Second pass, no new transcript bytes → no deltas surfaced.
	m2, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.AppliedTaskDeltas) != 0 {
		t.Errorf("AppliedTaskDeltas on no-new-bytes pass = %+v, want empty", m2.AppliedTaskDeltas)
	}
}

func TestTailer_TaskCompletedAt_StampedOnSnapshotReconcile(t *testing.T) {
	// A task_reminder snapshot flipping a task to completed stamps the
	// reconciling event's time (issue #282 path).
	snapAt := ts(7)
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "task": map[string]interface{}{"op": "create", "subject": "build"}},
		{"timestamp": snapAt, "snap": []interface{}{map[string]interface{}{"id": "1", "status": "completed"}}},
	})
	m, err := newTaskStampTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tasks) != 1 || m.Tasks[0].Status != TaskStatusCompleted {
		t.Fatalf("tasks = %+v, want one completed task", m.Tasks)
	}
	if got, want := m.Tasks[0].CompletedAt, tsUnix(t, snapAt); got != want {
		t.Errorf("CompletedAt = %d, want %d (snapshot reconcile must stamp)", got, want)
	}
}
