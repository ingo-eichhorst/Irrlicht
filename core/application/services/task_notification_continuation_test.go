package services_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

type taskNotificationContinuationFixture struct {
	t    *testing.T
	path string
	tail *tailer.TranscriptTailer
}

func newTaskNotificationContinuationFixture(t *testing.T) *taskNotificationContinuationFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	return &taskNotificationContinuationFixture{
		t:    t,
		path: path,
		tail: tailer.NewTranscriptTailer(path, &claudecode.Parser{}, "claude-code"),
	}
}

func (f *taskNotificationContinuationFixture) append(events ...map[string]interface{}) {
	f.t.Helper()
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		f.t.Fatalf("open transcript: %v", err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			f.t.Fatalf("append transcript event: %v", err)
		}
	}
}

func (f *taskNotificationContinuationFixture) metrics() *session.SessionMetrics {
	f.t.Helper()
	metrics, err := f.tail.TailAndProcess()
	if err != nil {
		f.t.Fatalf("TailAndProcess: %v", err)
	}
	return replayengine.TailerToDomain(metrics)
}

func assertTaskNotificationState(t *testing.T, metrics *session.SessionMetrics, want string) {
	t.Helper()
	got, _ := services.ClassifyState(session.StateWorking, metrics)
	if got != want {
		t.Fatalf("ClassifyState() = %q, want %q (last event %q, background count %d)",
			got, want, metrics.LastEventType, metrics.BackgroundProcessCount)
	}
}

func assertTaskNotificationCounts(t *testing.T, metrics *session.SessionMetrics, wantBackground, wantCompletions int) {
	t.Helper()
	if metrics.BackgroundProcessCount != wantBackground {
		t.Errorf("background count = %d, want %d", metrics.BackgroundProcessCount, wantBackground)
	}
	if len(metrics.SubagentCompletions) != wantCompletions {
		t.Errorf("completion count = %d, want %d", len(metrics.SubagentCompletions), wantCompletions)
	}
}

func backgroundCommandEvents(bashID, toolUseID string) []map[string]interface{} {
	return []map[string]interface{}{
		{
			"type": "assistant",
			"message": map[string]interface{}{
				"stop_reason": "tool_use",
				"content": []interface{}{map[string]interface{}{
					"type": "tool_use", "id": toolUseID, "name": "Bash",
					"input": map[string]interface{}{"command": "sleep 100", "run_in_background": true},
				}},
			},
		},
		{
			"type":          "user",
			"toolUseResult": map[string]interface{}{"backgroundTaskId": bashID},
			"message": map[string]interface{}{
				"content": []interface{}{map[string]interface{}{
					"type": "tool_result", "tool_use_id": toolUseID,
					"content": "Command running in background with ID: " + bashID + ". Output is being written to: /tmp/tasks/" + bashID + ".output. You will be notified when it completes.",
				}},
			},
		},
	}
}

func taskNotificationEvent(bashID, toolUseID string) map[string]interface{} {
	return map[string]interface{}{
		"type":   "user",
		"origin": map[string]interface{}{"kind": "task-notification"},
		"message": map[string]interface{}{
			"role": "user",
			"content": "<task-notification><task-id>" + bashID + "</task-id>" +
				"<tool-use-id>" + toolUseID + "</tool-use-id><status>completed</status></task-notification>",
		},
	}
}

func assistantToolUseEvent() map[string]interface{} {
	return map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"stop_reason": "tool_use",
			"content": []interface{}{map[string]interface{}{
				"type": "tool_use", "id": "toolu_resumed", "name": "Bash",
				"input": map[string]interface{}{"command": "git status --short"},
			}},
		},
	}
}

func toolResultEvent() map[string]interface{} {
	return map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"content": []interface{}{map[string]interface{}{
				"type": "tool_result", "tool_use_id": "toolu_resumed", "content": "clean",
			}},
		},
	}
}

// TestClassifyState_TaskNotificationStartsAgentContinuation is the issue #1899
// regression. Claude Code writes the completed background command as a
// user-role task-notification, then starts a new inference turn. The
// notification must replace the preceding turn_done lifecycle anchor while it
// removes the completed process from the background ledger. Otherwise the
// classifier sees the stale turn_done and emits a false ready transition.
func TestClassifyState_TaskNotificationStartsAgentContinuation(t *testing.T) {
	const (
		bashID    = "bkymfen4l"
		toolUseID = "toolu_background"
	)
	fixture := newTaskNotificationContinuationFixture(t)
	fixture.append(backgroundCommandEvents(bashID, toolUseID)...)
	spawned := fixture.metrics()
	assertTaskNotificationCounts(t, spawned, 1, 0)

	fixture.append(map[string]interface{}{"type": "system", "subtype": "turn_duration"})
	priorTurnDone := fixture.metrics()
	priorTurnDone.HasLiveBackgroundProcess = true // the detector's liveness-probe overlay
	assertTaskNotificationState(t, priorTurnDone, session.StateWorking)

	fixture.append(taskNotificationEvent(bashID, toolUseID))
	continued := fixture.metrics()
	assertTaskNotificationCounts(t, continued, 0, 1)
	assertTaskNotificationState(t, continued, session.StateWorking)

	fixture.append(assistantToolUseEvent())
	assertTaskNotificationState(t, fixture.metrics(), session.StateWorking)

	fixture.append(toolResultEvent())
	assertTaskNotificationState(t, fixture.metrics(), session.StateWorking)

	fixture.append(map[string]interface{}{"type": "system", "subtype": "turn_duration"})
	assertTaskNotificationState(t, fixture.metrics(), session.StateReady)
}
