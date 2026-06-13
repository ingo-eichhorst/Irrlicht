package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// findTaskDelta returns the first recorded task_delta event for sid with the
// given op, or nil.
func findTaskDelta(rec *mockRecorder, sid, op string) *lifecycle.Event {
	for _, e := range rec.snapshot() {
		if e.Kind == lifecycle.KindTaskDelta && e.SessionID == sid && e.TaskOp == op {
			ev := e
			return &ev
		}
	}
	return nil
}

// The detector records a task_delta lifecycle event for each task-list delta
// the tailer folds in this pass, carrying op/id/subject/status — making task
// tracking an assertable observable in onboarding recordings (#662). The
// per-pass idempotency (each delta recorded once) is a tailer/MergeMetrics
// property, covered in those packages; here we pin the detector's emission.
func TestSessionDetector_TaskDelta_RecordsAppliedDeltas(t *testing.T) {
	const sid = "td1"
	const path = "/home/.claude/projects/-Users-test/td1.jsonl"

	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType: "turn_done",
			AppliedTaskDeltas: []session.AppliedTaskDelta{
				{Op: "create", ID: "1", Subject: "build it", Status: "pending"},
				{Op: "update", ID: "1", Subject: "build it", Status: "completed"},
			},
		}, nil
	}}

	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	repo.states[sid] = &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Terminal bypasses the debounce window so the single activity drives one
	// processActivity pass deterministically.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sid,
		ProjectDir:     "-Users-test",
		TranscriptPath: path,
		Terminal:       true,
	}
	time.Sleep(250 * time.Millisecond)
	cancel()
	<-done

	create := findTaskDelta(rec, sid, "create")
	if create == nil {
		t.Fatal("no task_delta create event recorded")
	}
	if create.TaskID != "1" || create.TaskSubject != "build it" || create.TaskStatus != "pending" {
		t.Errorf("create event = %+v, want id=1 subject=\"build it\" status=pending", *create)
	}
	update := findTaskDelta(rec, sid, "update")
	if update == nil {
		t.Fatal("no task_delta update event recorded")
	}
	if update.TaskStatus != "completed" {
		t.Errorf("update event status = %q, want completed", update.TaskStatus)
	}
}
