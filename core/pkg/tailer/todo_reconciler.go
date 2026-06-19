// todo_reconciler.go translates an agent's authoritative todo-list snapshot into
// the task-progress deltas the tailer folds into session metrics. Agents whose
// "rewrite the whole list every call" todo tool (opencode `todowrite`,
// gemini-cli `write_todos`, codex `update_plan`) carries no stable per-todo ID
// share this: each adapter extracts its format-specific todos and hands them
// over keyed by the user-visible label.
package tailer

import "strconv"

// Todo is one entry of an agent's todo-list snapshot, normalized across formats.
// Key is the stable-enough identity — the user-visible label (opencode's
// `content`, gemini-cli's `description`); Status is the agent's raw status
// string ("" | pending | in_progress | completed | …).
type Todo struct {
	Key    string
	Status string
}

// TodoReconciler turns successive whole-list todo snapshots into the minimal
// TaskCreate/TaskUpdate delta sequence plus a TaskSnapshot, assigning each
// distinct Key a synthetic monotonic ID that matches the tailer's Create-time
// numbering. Hold one per Parser — its zero value is ready to use.
//
// Two todos sharing a Key collapse into one tracked task: a silent, acceptable
// trade-off, since the Key is the label the agent's own UI shows.
type TodoReconciler struct {
	idByKey map[string]string
	nextID  int
}

// Reconcile appends the Create/Update deltas for todos to ev.TaskDeltas and sets
// ev.TaskSnapshot to the full tracked list. A Create starts a task at pending, so
// a non-pending status emits an Update to move it forward; reversions back to
// pending are left to the tailer's snapshot reconcile (the delta path skips them
// by design). Empty-Key todos are skipped; an empty slice is a no-op.
func (r *TodoReconciler) Reconcile(todos []Todo, ev *ParsedEvent) {
	if len(todos) == 0 {
		return
	}
	if r.idByKey == nil {
		r.idByKey = make(map[string]string)
	}
	snapshot := make([]TaskSnapshotEntry, 0, len(todos))
	for _, todo := range todos {
		if todo.Key == "" {
			continue
		}
		id, seen := r.idByKey[todo.Key]
		if !seen {
			r.nextID++
			id = strconv.Itoa(r.nextID)
			r.idByKey[todo.Key] = id
			ev.TaskDeltas = append(ev.TaskDeltas, TaskDelta{
				Op:      TaskOpCreate,
				Subject: todo.Key,
			})
		}
		if todo.Status != "" && todo.Status != TaskStatusPending {
			ev.TaskDeltas = append(ev.TaskDeltas, TaskDelta{
				Op:     TaskOpUpdate,
				ID:     id,
				Status: todo.Status,
			})
		}
		snapshot = append(snapshot, TaskSnapshotEntry{
			ID:      id,
			Subject: todo.Key,
			Status:  todo.Status,
		})
	}
	if len(snapshot) > 0 {
		ev.TaskSnapshot = &snapshot
	}
}
