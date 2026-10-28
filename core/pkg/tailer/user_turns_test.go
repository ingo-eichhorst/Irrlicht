package tailer

import (
	"encoding/json"
	"os"
	"testing"
)

// TestTailer_UserTurnsCountsGenuineUserTurns pins SessionMetrics.UserTurns
// (#2109 P5-0), the count the detector compares to tell the tail of a
// hook-ended turn from a new turn. It reuses sessionErrorTestParser because
// that parser models the two shapes that must NOT count the way the adapters
// emit them: a tool result is a user-role line that raises ClearToolNames AND
// carries ToolResultIDs (claudecode), and an agent-written turn opener is the
// isMeta:true line claudecode's handleUserEvent short-circuits before
// ClearToolNames is raised (#1858).
//
// A new field, so the red half of this test is the mutation fixture's: counting
// on a bare ClearToolNames counts the tool result too and reads 3.
func TestTailer_UserTurnsCountsGenuineUserTurns(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
		{"kind": "assistant", "timestamp": ts(1)},
		{"kind": "tool_result", "timestamp": ts(2)},
		{"kind": "resume", "timestamp": ts(3)},
		{"kind": "user", "timestamp": ts(4)},
	})
	tl := newSessionErrorTailer(path, true)

	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.UserTurns != 2 {
		t.Fatalf("UserTurns = %d, want 2 — two prompts; the tool result and the agent-written opener are not user turns", m.UserTurns)
	}

	// Cumulative across passes: a later pass adds to it.
	appendTranscriptLine(t, path, map[string]interface{}{"kind": "user", "timestamp": ts(5)})
	if m, err = tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	if m.UserTurns != 3 {
		t.Fatalf("after a third prompt in a later pass: UserTurns = %d, want 3", m.UserTurns)
	}

	// A pass that reads no user turn keeps the count.
	appendTranscriptLine(t, path, map[string]interface{}{"kind": "turn_done", "timestamp": ts(6)})
	if m, err = tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	if m.UserTurns != 3 {
		t.Fatalf("after a turn end: UserTurns = %d, want 3 (unchanged)", m.UserTurns)
	}
}

// TestTailer_UserTurnsRestartsOnRewrite: a rewrite re-reads from byte 0, so the
// count restarts with the other accumulators instead of counting the
// surviving prompts a second time.
func TestTailer_UserTurnsRestartsOnRewrite(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
		{"kind": "assistant", "timestamp": ts(1)},
		{"kind": "user", "timestamp": ts(2)},
		{"kind": "assistant", "timestamp": ts(3)},
	})
	tl := newSessionErrorTailer(path, true)
	before, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if before.UserTurns != 2 {
		t.Fatalf("before the rewrite: UserTurns = %d, want 2", before.UserTurns)
	}

	// Shrink the file to one prompt: the size check sees it and resets.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(map[string]interface{}{"kind": "user", "timestamp": ts(4)}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.UserTurns != 1 {
		t.Fatalf("after a rewrite to one prompt: UserTurns = %d, want 1", m.UserTurns)
	}
}
