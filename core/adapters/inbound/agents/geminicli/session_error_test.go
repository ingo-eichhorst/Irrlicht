package geminicli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// The `content` string is verbatim from
// replaydata/agents/gemini-cli/scenarios/2-14_turn-aborted-by-error/recordings/
// 2026-06-12-14-25-05_irrlichd-0.5.1+0e2d7e4/transcript.jsonl:5 — the only
// shape gemini-cli has ever been recorded writing for a failed turn.
const recordedGeminiErrorContent = `[API Error: {"error":{"code":400,"message":"mid-turn non-retryable failure (mock-gemini-5xx)","status":"INVALID_ARGUMENT"}}]`

func TestParser_TopLevelError_IsASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":    "error",
		"content": recordedGeminiErrorContent,
	})
	if ev == nil {
		t.Fatal("ParseLine returned nil for a type:\"error\" line")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want \"turn_done\" (unchanged from #665)", ev.EventType)
	}
	if ev.SessionError == nil {
		t.Fatal("SessionError is nil — the failure reaches the UI only as prose today")
	}
	if ev.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal — gemini emits no retry ladder", ev.SessionError.Phase)
	}
	if ev.SessionError.HTTPStatus == nil || *ev.SessionError.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %v, want 400 — the code is inside the embedded JSON", ev.SessionError.HTTPStatus)
	}
	if ev.IsError {
		t.Error("IsError must be false: it means a TOOL RESULT failed. #1798 moved this " +
			"signal onto SessionError precisely so the two stay distinct")
	}
}

// A tool call that failed is NOT a session error — the other half of the
// distinction, pinned so a future edit cannot collapse them.
func TestParser_ToolCallError_IsNotASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "gemini",
		"toolCalls": []interface{}{
			map[string]interface{}{"id": "c1", "name": "run_shell_command", "status": "error"},
		},
	})
	if ev == nil {
		t.Fatal("ParseLine returned nil")
	}
	if !ev.IsError {
		t.Error("IsError must stay true for a failed tool call")
	}
	if ev.SessionError != nil {
		t.Errorf("SessionError must stay nil for a tool failure, got %+v", ev.SessionError)
	}
}

// TestTailer_GeminiErrorPairKeepsTheSessionError is the regression test for the
// defect the two tests above CANNOT see, and the reason they cannot is the
// point: they feed the error line in isolation, so they prove the parser emits
// a signal and say nothing about whether it survives.
//
// It did not. gemini writes a failure as a PAIR — a type:"error" line carrying
// the API body, then a type:"info" "This request failed…" notice. Both parse to
// turn_done, and the tailer's clearing rule fires on a turn boundary, so the
// notice wiped the error the line before it had just recorded. The session
// settled GREEN, and the committed 2-14 golden proved it: it read
// `working → ready` and was completely unmoved by the promotion, while pi's and
// opencode's goldens moved.
//
// Driven through the REAL tailer with the REAL parser over the REAL recorded
// bytes. A purpose-built fixture parser would have encoded the author's model
// of gemini rather than gemini — the #1809 trap, where a hand-written double
// raised a flag on shapes the actual adapter does not.
func TestTailer_GeminiErrorPairKeepsTheSessionError(t *testing.T) {
	const recorded = "../../../../../replaydata/agents/gemini-cli/scenarios/" +
		"2-14_turn-aborted-by-error/recordings/" +
		"2026-06-12-14-25-05_irrlichd-0.5.1+0e2d7e4/transcript.jsonl"

	raw, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatalf("read recorded transcript: %v", err)
	}
	// Precondition: the fixture really does contain the pair this test is
	// about. Without this the test would still pass on a transcript that had
	// been trimmed to just the error line — i.e. it would silently stop
	// covering the defect.
	if !bytes.Contains(raw, []byte(`"type":"error"`)) ||
		!bytes.Contains(raw, []byte("This request failed")) {
		t.Fatalf("fixture no longer contains the error+info pair this test covers: %s", recorded)
	}

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "gemini-cli")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m == nil {
		t.Fatal("no metrics produced")
	}

	if m.SessionError == nil {
		t.Fatal("the session error did not survive the transcript: gemini's " +
			"\"This request failed\" notice parses to turn_done, and a turn boundary " +
			"clears the standing error — so the failure recorded one line earlier was " +
			"wiped and the session settles GREEN")
	}
	if m.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal", m.SessionError.Phase)
	}
	// The RICH error must win, not the notice's own prose. The tailer keeps the
	// latest error recorded, so an info notice that built a fresh one would
	// silently drop the status code — passing every single-line test while
	// downgrading every real failure.
	if m.SessionError.HTTPStatus == nil {
		t.Error("HTTPStatus is nil — the info notice overwrote the API line's error " +
			"with a poorer one built from its own prose")
	} else if *m.SessionError.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", *m.SessionError.HTTPStatus)
	}
}

// A user ESC must NOT be promoted to a session error, even though it takes the
// same terminal-info path. LOCK on the other half of the marker split.
func TestParser_RequestCancelledIsNotASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "info", "content": "Request cancelled.",
	})
	if ev == nil || ev.EventType != "turn_done" {
		t.Fatalf("precondition: a cancel notice must settle the turn, got %+v", ev)
	}
	if ev.SessionError != nil {
		t.Errorf("a user cancel must not turn the session red, got %+v", ev.SessionError)
	}
}

// TestGeminiErrorClass_QuotaIsReachable pins the ordering bug that made the
// quota branch dead code: Google returns RESOURCE_EXHAUSTED as HTTP 429, so a
// code-first switch answered "rate_limit" and the status was never consulted.
// A spent quota and a momentary rate limit are the one pair a user acts on
// differently.
func TestGeminiErrorClass_QuotaIsReachable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		status string
		want   string
	}{
		{"quota exhausted arrives as 429", 429, "RESOURCE_EXHAUSTED", "quota"},
		{"plain 429 is a rate limit", 429, "", "rate_limit"},
		{"invalid argument is a query error", 400, "INVALID_ARGUMENT", "query"},
		{"unauthenticated is auth", 401, "UNAUTHENTICATED", "auth"},
		{"permission denied is auth", 403, "PERMISSION_DENIED", "auth"},
		{"5xx is the provider", 503, "UNAVAILABLE", "provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := geminiErrorClass(tc.code, tc.status); got != tc.want {
				t.Errorf("geminiErrorClass(%d, %q) = %q, want %q", tc.code, tc.status, got, tc.want)
			}
		})
	}
}

// TestParser_StandaloneFailureNotice is what the isError split is still
// load-bearing for after #1799.
//
// A "This request failed…" notice can arrive with NO type:"error" line before
// it (#676's shape). ClearedByTurnBoundary cannot help there — there is no
// standing error to preserve — so without the flag the notice settles the turn
// and reports nothing at all, and the session goes green on a failure.
func TestParser_StandaloneFailureNotice(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":    "info",
		"content": "This request failed. Press F12 for diagnostics.",
	})
	if ev == nil || ev.EventType != "turn_done" {
		t.Fatalf("precondition: the notice must settle the turn, got %+v", ev)
	}
	if ev.SessionError == nil {
		t.Fatal("a failure notice with no preceding error line reported nothing — " +
			"the turn ends and the session goes green on a failure")
	}
	if ev.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal", ev.SessionError.Phase)
	}
}
