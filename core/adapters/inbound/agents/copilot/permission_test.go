package copilot

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// writeTranscript writes lines to a session-shaped transcript and returns its
// path: <tmp>/<session-id>/events.jsonl, matching the layout Copilot uses.
func writeTranscript(t *testing.T, lines string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "8f2c1d90-0000-4000-8000-000000000001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, transcriptFilename)
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Real payload shapes, captured from a live CLI 1.0.77 session that was left
// sitting on its confirmation prompt (issue #1256 research pass). The request
// and its resolution pair on requestId — NOT on toolCallId, which is also
// present but identifies the tool call rather than the prompt.
const (
	permHeader = `{"type":"session.start","timestamp":"2026-08-02T17:00:00.000Z","data":{"sessionId":"8f2c1d90-0000-4000-8000-000000000001","copilotVersion":"1.0.77","context":{"cwd":"/tmp/proj"}}}` + "\n" +
		`{"type":"user.message","timestamp":"2026-08-02T17:00:01.000Z","data":{"content":"write a file"}}` + "\n" +
		`{"type":"assistant.turn_start","timestamp":"2026-08-02T17:00:02.000Z","data":{"turnId":"0"}}` + "\n" +
		`{"type":"tool.execution_start","timestamp":"2026-08-02T17:00:03.000Z","data":{"toolCallId":"call_kXL","toolName":"bash"}}` + "\n"

	permRequested = `{"type":"permission.requested","timestamp":"2026-08-02T17:00:04.000Z","data":{"requestId":"504409c4-aaaa-bbbb-cccc-000000000001","permissionRequest":{"kind":"shell","toolCallId":"call_kXL","fullCommandText":"echo written > out.txt","hasWriteFileRedirection":true}}}` + "\n"

	permCompleted = `{"type":"permission.completed","timestamp":"2026-08-02T17:00:09.000Z","data":{"requestId":"504409c4-aaaa-bbbb-cccc-000000000001","toolCallId":"call_kXL","result":{"kind":"approved"}}}` + "\n"
)

// TestPermissionRequested_OpensTranscriptPermission asserts the transcript's
// own permission prompt reaches SessionMetrics. This is what lets a Copilot
// session reach `waiting` without a hook — the finding that removed the hook
// tier from #1256's scope.
func TestPermissionRequested_OpensTranscriptPermission(t *testing.T) {
	path := writeTranscript(t, permHeader+permRequested)

	tl := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if !m.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = false, want true — the agent is blocked on an unanswered prompt")
	}
}

// TestPermissionCompleted_ClosesTranscriptPermission asserts the resolution
// clears it, so answering the prompt releases the session back to working
// rather than pinning it in waiting for the rest of the session.
func TestPermissionCompleted_ClosesTranscriptPermission(t *testing.T) {
	path := writeTranscript(t, permHeader+permRequested+permCompleted)

	tl := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = true, want false — the prompt was answered")
	}
}

// TestPermissionPairsByRequestID pins the pairing key. Two prompts are opened
// and only the FIRST is answered; a bool-based implementation (or one keyed on
// toolCallId, which both requests here share) would report the session as
// unblocked while the agent is still waiting on the second.
func TestPermissionPairsByRequestID(t *testing.T) {
	second := `{"type":"permission.requested","timestamp":"2026-08-02T17:00:05.000Z","data":{"requestId":"504409c4-aaaa-bbbb-cccc-000000000002","permissionRequest":{"kind":"shell","toolCallId":"call_kXL"}}}` + "\n"
	path := writeTranscript(t, permHeader+permRequested+second+permCompleted)

	tl := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if !m.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = false, want true — the second prompt is still unanswered")
	}
}
