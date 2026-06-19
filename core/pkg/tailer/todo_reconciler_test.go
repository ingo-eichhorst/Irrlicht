package tailer

import (
	"reflect"
	"testing"
)

func TestTodoReconciler_CreateThenStableIDs(t *testing.T) {
	var r TodoReconciler

	ev1 := &ParsedEvent{}
	r.Reconcile([]Todo{
		{Key: "write tests", Status: "pending"},
		{Key: "ship it", Status: "in_progress"},
	}, ev1)

	// Two Creates (each starts at pending); the in_progress one also gets an
	// Update to move it forward. A pending todo gets no Update.
	want1 := []TaskDelta{
		{Op: TaskOpCreate, Subject: "write tests"},
		{Op: TaskOpCreate, Subject: "ship it"},
		{Op: TaskOpUpdate, ID: "2", Status: "in_progress"},
	}
	if !reflect.DeepEqual(ev1.TaskDeltas, want1) {
		t.Errorf("deltas =\n %+v\nwant\n %+v", ev1.TaskDeltas, want1)
	}
	if ev1.TaskSnapshot == nil || len(*ev1.TaskSnapshot) != 2 {
		t.Fatalf("snapshot = %v, want 2 entries", ev1.TaskSnapshot)
	}

	// Second call: keys keep their first-seen IDs; only status changes emit Updates.
	ev2 := &ParsedEvent{}
	r.Reconcile([]Todo{
		{Key: "write tests", Status: "completed"},
		{Key: "ship it", Status: "in_progress"},
	}, ev2)
	want2 := []TaskDelta{
		{Op: TaskOpUpdate, ID: "1", Status: "completed"},
		{Op: TaskOpUpdate, ID: "2", Status: "in_progress"},
	}
	if !reflect.DeepEqual(ev2.TaskDeltas, want2) {
		t.Errorf("second-call deltas =\n %+v\nwant\n %+v", ev2.TaskDeltas, want2)
	}
}

func TestTodoReconciler_DedupAndEmptyKeySkip(t *testing.T) {
	var r TodoReconciler
	ev := &ParsedEvent{}
	r.Reconcile([]Todo{
		{Key: "dup", Status: "pending"},
		{Key: "", Status: "in_progress"},  // empty key → skipped
		{Key: "dup", Status: "completed"}, // same key collapses to one task
	}, ev)

	want := []TaskDelta{
		{Op: TaskOpCreate, Subject: "dup"},
		{Op: TaskOpUpdate, ID: "1", Status: "completed"},
	}
	if !reflect.DeepEqual(ev.TaskDeltas, want) {
		t.Errorf("deltas =\n %+v\nwant\n %+v", ev.TaskDeltas, want)
	}
	// Snapshot keeps both "dup" rows (empty-key one skipped), sharing id "1".
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 2 {
		t.Fatalf("snapshot = %v, want 2 entries (empty-key skipped)", ev.TaskSnapshot)
	}
	for _, e := range *ev.TaskSnapshot {
		if e.ID != "1" {
			t.Errorf("snapshot entry id = %q, want 1 (deduped)", e.ID)
		}
	}
}

func TestTodoReconciler_EmptyIsNoop(t *testing.T) {
	var r TodoReconciler
	ev := &ParsedEvent{}
	r.Reconcile(nil, ev)
	r.Reconcile([]Todo{}, ev)
	if ev.TaskDeltas != nil || ev.TaskSnapshot != nil {
		t.Errorf("empty reconcile mutated ev: deltas=%v snapshot=%v", ev.TaskDeltas, ev.TaskSnapshot)
	}
}
