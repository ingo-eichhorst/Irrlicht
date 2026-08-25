package tailer

import (
	"os"
	"path/filepath"
	"testing"
)

// clearErrParser reports a terminal session error on a line whose "kind" is
// "boom", and nothing on any other line. Terminal on purpose: the phase that
// clearSessionErrorOnRecovery deliberately refuses to retire on a transcript
// turn boundary is the one that most needs the hook path to be able to.
type clearErrParser struct{}

func (clearErrParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	kind, _ := raw["kind"].(string)
	ev := &ParsedEvent{EventType: kind, Timestamp: ParseTimestamp(raw)}
	if kind == "boom" {
		ev.SessionError = &SessionError{
			Phase:   ErrorPhaseTerminal,
			Class:   "provider",
			Message: "the provider gave up",
		}
	}
	return ev
}

// writeClearErrTranscript writes raw JSONL and returns its path.
func writeClearErrTranscript(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestClearSessionError_RetiresAStandingError is the blip fix's direct test.
//
// It cannot be run red against "before the fix" — ClearSessionError is what the
// change ADDS. It was verified by mutation instead: with the method's body
// emptied, this test fails on the "still standing" assertion below. See
// AGENTS.md's rule for checks a change adds.
func TestClearSessionError_RetiresAStandingError(t *testing.T) {
	path := writeClearErrTranscript(t,
		`{"kind":"boom","timestamp":"2026-08-05T10:00:00Z"}`+"\n")

	tt := NewTranscriptTailer(path, clearErrParser{}, "test")
	m, err := tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.SessionError == nil {
		t.Fatal("precondition failed: the fixture parser did not produce an error, " +
			"so this test cannot observe a clear")
	}

	tt.ClearSessionError()

	// A pass that reads no new bytes: surfaceSporadicMetrics still runs, which
	// is exactly how an errored-but-idle session keeps reporting its error.
	m, err = tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess after clear: %v", err)
	}
	if m.SessionError != nil {
		t.Errorf("SessionError is still standing after ClearSessionError: %+v — "+
			"the hook path cannot retire the failure, so a Stop hook produces "+
			"error → ready → error", m.SessionError)
	}
}

// TestClearSessionError_RetiresATerminalError pins the one behavioural
// difference between the hook clear and the transcript clear: a transcript
// turn boundary must NOT retire a terminal error (it may be the failed turn's
// own epilogue), while a Stop hook must, because it is an out-of-band assertion
// arriving after everything the transcript has written.
//
// A lock as much as a test: it fails if a future change teaches
// ClearSessionError to consult the phase.
func TestClearSessionError_RetiresATerminalError(t *testing.T) {
	path := writeClearErrTranscript(t,
		`{"kind":"boom","timestamp":"2026-08-05T10:00:00Z"}`+"\n"+
			`{"kind":"turn_done","timestamp":"2026-08-05T10:00:01Z"}`+"\n")

	tt := NewTranscriptTailer(path, clearErrParser{}, "test")
	m, err := tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	// The transcript's own turn boundary followed the terminal error and left
	// it standing — that is clearSessionErrorOnRecovery's terminal exemption.
	if m.SessionError == nil {
		t.Fatal("a terminal error was retired by a transcript turn boundary; the " +
			"failed turn's own epilogue is not a recovery")
	}

	tt.ClearSessionError()
	m, err = tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess after clear: %v", err)
	}
	if m.SessionError != nil {
		t.Errorf("a Stop hook must retire a terminal error too; got %+v", m.SessionError)
	}
}
