// task_fold.go re-exports the tailer's TaskDelta/TaskSnapshot fold for
// adapters that never run the tailer.
//
// A ProcessOwnedStore adapter (opencode, hermes) reads rows out of a SQLite
// store and drives Parser.ParseLine itself, so TranscriptTailer — and the
// fold it performs at tailer.go's scan loop — is bypassed entirely. Each such
// adapter still has to turn the parser's deltas into session.Task values, and
// each one arriving at its own copy is how the two drift: the rules here
// (Create starts at pending; a snapshot is authoritative for BOTH pruning and
// the status reversions the delta path skips by design) are subtle enough
// that a second, independently maintained implementation is a latent bug.
package tailer

import (
	"strconv"

	"irrlicht/core/domain/session"
)

// ApplyTaskDeltas folds Create/Update deltas into tasks and taskByID.
//
// Create assigns the next monotonic id and starts the task at pending, which
// is what makes TodoReconciler's synthetic ids line up with these. Update is
// ignored for an unknown id or an empty status rather than inventing a task —
// an out-of-order delta must not fabricate one.
func ApplyTaskDeltas(deltas []TaskDelta, tasks *[]session.Task, taskByID map[string]int) {
	for _, d := range deltas {
		switch d.Op {
		case TaskOpCreate:
			id := strconv.Itoa(len(*tasks) + 1)
			*tasks = append(*tasks, session.Task{
				ID:          id,
				Subject:     d.Subject,
				Description: d.Description,
				ActiveForm:  d.ActiveForm,
				Status:      TaskStatusPending,
			})
			taskByID[id] = len(*tasks) - 1
		case TaskOpUpdate:
			if idx, ok := taskByID[d.ID]; ok && d.Status != "" {
				(*tasks)[idx].Status = d.Status
			}
		}
	}
}

// ReconcileTaskSnapshot applies a whole-list snapshot to tasks and taskByID.
//
// Whole-list-replace todo tools make the snapshot authoritative twice over:
// a task absent from it has been deleted upstream and is pruned, and a status
// that moved BACKWARDS (completed → pending) is honoured here, because the
// delta path deliberately skips reversions. taskByID is rebuilt afterwards so
// indices stay valid across the prune.
//
// A nil snapshot or an empty task list is a no-op — a read-mode todo call
// must not wipe the list it was only reading.
func ReconcileTaskSnapshot(snapshot *[]TaskSnapshotEntry, tasks *[]session.Task, taskByID *map[string]int) {
	if snapshot == nil || len(*tasks) == 0 {
		return
	}
	snapByID := make(map[string]TaskSnapshotEntry, len(*snapshot))
	for _, entry := range *snapshot {
		snapByID[entry.ID] = entry
	}
	kept := make([]session.Task, 0, len(*tasks))
	for i := range *tasks {
		entry, present := snapByID[(*tasks)[i].ID]
		if !present {
			continue
		}
		if entry.Status != "" && entry.Status != (*tasks)[i].Status {
			(*tasks)[i].Status = entry.Status
		}
		kept = append(kept, (*tasks)[i])
	}
	*tasks = kept
	newByID := make(map[string]int, len(*tasks))
	for i := range *tasks {
		newByID[(*tasks)[i].ID] = i
	}
	*taskByID = newByID
}
