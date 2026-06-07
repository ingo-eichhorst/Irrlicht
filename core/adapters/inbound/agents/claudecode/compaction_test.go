package claudecode

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

// --- Parser + tailer end-to-end: manual /compact tail (issue #641) ---

func compactionTS(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func appendTranscriptLine(t *testing.T, path string, line map[string]interface{}) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(line); err != nil {
		t.Fatalf("append line: %v", err)
	}
}

// TestTailer_ManualCompactTail_NoSubstantiveActivity is the issue #641
// regression at the parser+tailer integration level. A manual /compact
// writes a synthetic tail — compact_boundary, the isCompactSummary user
// summary, the isMeta caveat, and the command wrappers — with no assistant
// event or turn_done following. Every entry must be skipped so the pass
// reports NoSubstantiveActivity=true and LastEventType stays at the prior
// turn_done; the detector then ignores the pass and the session stays
// ready (that half of the chain is pinned by
// session_detector_no_substantive_test.go). Pre-fix, the summary leaked
// through as a real user turn and stranded the session in working.
// Entry shapes mirror the live session bb9f6ebf that surfaced the bug.
func TestTailer_ManualCompactTail_NoSubstantiveActivity(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": compactionTS(0), "message": map[string]interface{}{
			"role": "user", "content": "do the thing",
		}},
		{"type": "assistant", "timestamp": compactionTS(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "done."}},
		}},
		{"type": "system", "subtype": "turn_duration", "timestamp": compactionTS(2)},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if m.LastEventType != "turn_done" {
		t.Fatalf("pass 1 LastEventType = %q, want turn_done", m.LastEventType)
	}

	for _, ln := range []map[string]interface{}{
		{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted",
			"timestamp": compactionTS(120)},
		{"type": "user", "isCompactSummary": true, "isVisibleInTranscriptOnly": true,
			"timestamp": compactionTS(120), "message": map[string]interface{}{
				"role":    "user",
				"content": "This session is being continued from a previous conversation that ran out of context.",
			}},
		{"type": "user", "isMeta": true, "timestamp": compactionTS(120), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-caveat>Caveat: …</local-command-caveat>",
		}},
		{"type": "user", "timestamp": compactionTS(120), "message": map[string]interface{}{
			"role": "user", "content": "<command-name>/compact</command-name>",
		}},
		{"type": "user", "timestamp": compactionTS(121), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-stdout>Compacted (ctrl+o to see full summary)</local-command-stdout>",
		}},
	} {
		appendTranscriptLine(t, path, ln)
	}

	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if !m.NoSubstantiveActivity {
		t.Errorf("pass 2 NoSubstantiveActivity = false, want true (every compaction-tail entry must be skipped)")
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("pass 2 LastEventType = %q, want turn_done (compact summary must not register as a user turn)", m.LastEventType)
	}
}
