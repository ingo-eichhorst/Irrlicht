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

// TestClassifyState_TaskNotificationStartsAgentContinuation is the issue #1899
// regression. Claude Code writes the completed background command as a
// user-role task-notification, then starts a new inference turn. The
// notification must replace the preceding turn_done lifecycle anchor while it
// removes the completed process from the background ledger. Otherwise the
// classifier sees the stale turn_done and emits a false ready transition.
func TestClassifyState_TaskNotificationStartsAgentContinuation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}

	tail := tailer.NewTranscriptTailer(path, &claudecode.Parser{}, "claude-code")
	appendEvents := func(events ...map[string]interface{}) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open transcript: %v", err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				t.Fatalf("append transcript event: %v", err)
			}
		}
	}
	readMetrics := func() *session.SessionMetrics {
		t.Helper()
		m, err := tail.TailAndProcess()
		if err != nil {
			t.Fatalf("TailAndProcess: %v", err)
		}
		return replayengine.TailerToDomain(m)
	}
	classify := func(m *session.SessionMetrics, want string) {
		t.Helper()
		got, _ := services.ClassifyState(session.StateWorking, m)
		if got != want {
			t.Fatalf("ClassifyState() = %q, want %q (last event %q, background count %d)",
				got, want, m.LastEventType, m.BackgroundProcessCount)
		}
	}

	const (
		bashID    = "bkymfen4l"
		toolUseID = "toolu_background"
	)
	appendEvents(
		map[string]interface{}{
			"type": "assistant",
			"message": map[string]interface{}{
				"stop_reason": "tool_use",
				"content": []interface{}{map[string]interface{}{
					"type": "tool_use", "id": toolUseID, "name": "Bash",
					"input": map[string]interface{}{"command": "sleep 100", "run_in_background": true},
				}},
			},
		},
		map[string]interface{}{
			"type":          "user",
			"toolUseResult": map[string]interface{}{"backgroundTaskId": bashID},
			"message": map[string]interface{}{
				"content": []interface{}{map[string]interface{}{
					"type": "tool_result", "tool_use_id": toolUseID,
					"content": "Command running in background with ID: " + bashID + ". Output is being written to: /tmp/tasks/" + bashID + ".output. You will be notified when it completes.",
				}},
			},
		},
	)
	spawned := readMetrics()
	if spawned.BackgroundProcessCount != 1 {
		t.Fatalf("spawn pass background count = %d, want 1", spawned.BackgroundProcessCount)
	}

	appendEvents(map[string]interface{}{"type": "system", "subtype": "turn_duration"})
	priorTurnDone := readMetrics()
	priorTurnDone.HasLiveBackgroundProcess = true // the detector's liveness-probe overlay
	classify(priorTurnDone, session.StateWorking)

	appendEvents(map[string]interface{}{
		"type":   "user",
		"origin": map[string]interface{}{"kind": "task-notification"},
		"message": map[string]interface{}{
			"role": "user",
			"content": "<task-notification><task-id>" + bashID + "</task-id>" +
				"<tool-use-id>" + toolUseID + "</tool-use-id><status>completed</status></task-notification>",
		},
	})
	continued := readMetrics()
	if continued.BackgroundProcessCount != 0 {
		t.Fatalf("notification pass background count = %d, want 0", continued.BackgroundProcessCount)
	}
	if len(continued.SubagentCompletions) != 1 {
		t.Fatalf("notification pass completions = %d, want 1", len(continued.SubagentCompletions))
	}
	classify(continued, session.StateWorking)

	appendEvents(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"stop_reason": "tool_use",
			"content": []interface{}{map[string]interface{}{
				"type": "tool_use", "id": "toolu_resumed", "name": "Bash",
				"input": map[string]interface{}{"command": "git status --short"},
			}},
		},
	})
	classify(readMetrics(), session.StateWorking)

	appendEvents(map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"content": []interface{}{map[string]interface{}{
				"type": "tool_result", "tool_use_id": "toolu_resumed", "content": "clean",
			}},
		},
	})
	classify(readMetrics(), session.StateWorking)

	appendEvents(map[string]interface{}{"type": "system", "subtype": "turn_duration"})
	classify(readMetrics(), session.StateReady)
}
