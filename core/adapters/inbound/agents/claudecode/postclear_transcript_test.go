package claudecode

import (
	"testing"

	"irrlicht/core/pkg/tailer"
)

// TestTailer_PostClearTranscript_HasNoSubstantiveEvent is a lock for the
// adapter half of #2034. After `/clear`, Claude Code starts the new transcript
// with the five lines below (copied from live session fd0a7643, Claude Code
// 2.1.280, text trimmed). The parser skips every one of them, so the tailer
// reports no substantive event: LastEventType stays empty.
// services.ClassifyStateOnEvidence keys on exactly that, so a parser change that
// starts treating one of these lines as activity must fail here first.
func TestTailer_PostClearTranscript_HasNoSubstantiveEvent(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "mode", "mode": "normal", "sessionId": "fd0a7643"},
		{"type": "file-history-snapshot", "messageId": "e0100b86", "isSnapshotUpdate": false},
		{"type": "user", "isMeta": true, "timestamp": "2026-09-22T20:46:53.168Z", "message": map[string]interface{}{
			"role": "user", "content": "<local-command-caveat>Caveat: …</local-command-caveat>",
		}},
		{"type": "user", "timestamp": "2026-09-22T20:46:53.043Z", "message": map[string]interface{}{
			"role": "user", "content": "<command-name>/clear</command-name>\n            <command-message>clear</command-message>\n            <command-args></command-args>",
		}},
		{"type": "system", "subtype": "local_command", "timestamp": "2026-09-22T20:46:53.167Z", "isMeta": false,
			"content": "<local-command-stdout></local-command-stdout>", "commandRun": map[string]interface{}{"command": "clear", "args": ""}},
	})

	m, err := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code").TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.LastEventType != "" {
		t.Errorf("LastEventType = %q, want empty — a post-/clear transcript carries no agent activity", m.LastEventType)
	}
	if len(m.MessageHistory) != 0 {
		t.Errorf("MessageHistory has %d events, want 0", len(m.MessageHistory))
	}
}
