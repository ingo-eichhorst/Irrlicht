package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

// The three committed recordings this file reads. All of them are covered by
// the replaydata deletion guard (#268), so a missing one is a broken checkout
// or a retired fixture — never a reason to pass. Every helper below fails
// loudly rather than skipping, per AGENTS.md's "a verification mechanism must
// fail loudly when it cannot run": a test that silently skips when it cannot
// find its evidence reports the same green as one that looked and found the
// behaviour correct.
const (
	fixtureAPIErrorRetrying = "2-9_token-quota-exhausted/recordings/" +
		"2026-05-18-23-08-01_irrlichd-0.4.5+6898561/transcript.jsonl"
	fixtureAPIErrorTerminal = "2-14_turn-aborted-by-error/recordings/" +
		"2026-05-22-15-49-22_irrlichd-unknown/transcript.jsonl"
	fixtureESCInterrupt = "2-20_user-esc-interrupt/recordings/" +
		"2026-05-19-00-11-42_irrlichd-0.4.5+898a14f/transcript.jsonl"
)

// scenarioFixture resolves a committed claudecode scenario recording.
func scenarioFixture(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "..", "replaydata", "agents",
		"claudecode", "scenarios", filepath.FromSlash(rel))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed fixture missing at %s: %v — this recording is "+
			"deletion-guarded (#268); a missing file is a broken checkout, "+
			"not a reason to skip", path, err)
	}
	return path
}

// fixtureLines decodes every line of a committed transcript. Fails when the
// file holds no decodable line, so an unreadable fixture cannot masquerade as
// "nothing to assert".
func fixtureLines(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var out []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("decode line in %s: %v", path, err)
		}
		out = append(out, raw)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	if len(out) == 0 {
		t.Fatalf("fixture %s decoded to zero lines", path)
	}
	return out
}

// TestParser_APIError_RetryingFromRecording drives the recorded
// `system`/`api_error` ladder through the parser. Every field asserted here is
// read straight out of the committed line, so the test fails if either the
// mapping or the recording changes shape.
func TestParser_APIError_RetryingFromRecording(t *testing.T) {
	lines := fixtureLines(t, scenarioFixture(t, fixtureAPIErrorRetrying))

	p := &Parser{}
	var first *tailer.SessionError
	var firstEvent *tailer.ParsedEvent
	seen := 0
	for _, raw := range lines {
		ev := p.ParseLine(raw)
		if sub, _ := raw["subtype"].(string); sub != "api_error" {
			if ev.SessionError != nil {
				t.Fatalf("non-api_error line (type=%v subtype=%v) produced a "+
					"SessionError: %+v", raw["type"], raw["subtype"], ev.SessionError)
			}
			continue
		}
		seen++
		if ev.SessionError == nil {
			t.Fatalf("api_error line %d produced no SessionError", seen)
		}
		if first == nil {
			first, firstEvent = ev.SessionError, ev
		}
	}
	// The count is the load-bearing part: without it a mapping that never runs
	// reports the same pass as one that mapped every line correctly.
	if seen != 6 {
		t.Fatalf("expected 6 api_error lines in this recording, saw %d — "+
			"reproduce with: grep -c '\"subtype\":\"api_error\"' <fixture>", seen)
	}

	if first.Phase != tailer.ErrorPhaseRetrying {
		t.Errorf("Phase = %q, want %q — the line carries retryInMs/retryAttempt, "+
			"so another attempt is scheduled", first.Phase, tailer.ErrorPhaseRetrying)
	}
	if first.Class != "rate_limit_error" {
		t.Errorf("Class = %q, want rate_limit_error", first.Class)
	}
	if !strings.HasPrefix(first.Message, "Number of request tokens has exceeded") {
		t.Errorf("Message = %q, want the provider's verbatim rate-limit text", first.Message)
	}
	if first.HTTPStatus == nil || *first.HTTPStatus != 429 {
		t.Errorf("HTTPStatus = %v, want 429", first.HTTPStatus)
	}
	if first.Attempt == nil || *first.Attempt != 1 {
		t.Errorf("Attempt = %v, want 1", first.Attempt)
	}
	if first.MaxAttempts == nil || *first.MaxAttempts != 10 {
		t.Errorf("MaxAttempts = %v, want 10", first.MaxAttempts)
	}
	// 616.4520045919932 ms. A whole-millisecond int field would truncate it,
	// which is why RetryIn is a Duration — see session.SessionError.RetryIn.
	retryMs := 616.4520045919932
	wantRetry := time.Duration(retryMs * float64(time.Millisecond))
	if first.RetryIn == nil || *first.RetryIn != wantRetry {
		t.Errorf("RetryIn = %v, want %v (fractional ms must survive)", first.RetryIn, wantRetry)
	}

	// Scope lock: an api_error is a SESSION failure, not a tool_result failure.
	if firstEvent.IsError {
		t.Error("api_error must not set IsError — that channel is tool-result failure")
	}
	// Lock: #1798's applySessionError runs on the skipped path precisely
	// because this shape is Skip=true. Changing that silently moves the event
	// to the other routing path.
	if !firstEvent.Skip {
		t.Error("api_error is expected to stay Skip=true (handleSystemEvent's catch-all)")
	}
}

// TestParser_TerminalAPIErrorMessage_FromRecording covers the second claudecode
// shape: an ordinary assistant message flagged isApiErrorMessage, carrying no
// status code at all — its own prose quotes HTTP 200.
func TestParser_TerminalAPIErrorMessage_FromRecording(t *testing.T) {
	lines := fixtureLines(t, scenarioFixture(t, fixtureAPIErrorTerminal))

	p := &Parser{}
	var first *tailer.SessionError
	seen := 0
	for _, raw := range lines {
		ev := p.ParseLine(raw)
		flagged, _ := raw["isApiErrorMessage"].(bool)
		if !flagged {
			continue
		}
		seen++
		if ev.SessionError == nil {
			t.Fatalf("isApiErrorMessage:true line %d produced no SessionError", seen)
		}
		if first == nil {
			first = ev.SessionError
		}
	}
	if seen != 2 {
		t.Fatalf("expected 2 isApiErrorMessage:true lines, saw %d", seen)
	}

	if first.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want %q — no retry fields accompany this shape",
			first.Phase, tailer.ErrorPhaseTerminal)
	}
	if !strings.HasPrefix(first.Message, "API Error: API returned an empty or malformed response") {
		t.Errorf("Message = %q, want the agent's verbatim epilogue", first.Message)
	}
	// The recorded text says "(HTTP 200)". Deriving a status from prose would
	// record the transport's success code as the failure code.
	if first.HTTPStatus != nil {
		t.Errorf("HTTPStatus = %v, want nil — this shape carries no status field, "+
			"and the 200 in its prose is the transport's success code", *first.HTTPStatus)
	}
	if first.Attempt != nil || first.MaxAttempts != nil || first.RetryIn != nil {
		t.Errorf("retry fields must stay nil for the terminal shape: attempt=%v max=%v retryIn=%v",
			first.Attempt, first.MaxAttempts, first.RetryIn)
	}
}

// TestParser_ESCInterrupt_IsApiErrorMessageFalseIsNotAnError is the
// false-positive defect test the issue asked for.
//
// isApiErrorMessage is written on assistant messages whether or not an error
// occurred; the ESC-interrupt recording carries it with value false. A producer
// that matched on FIELD PRESENCE rather than on the VALUE would classify every
// user interrupt as a session error — the most common interaction in the
// product, painted red. Run this against a presence-matching implementation
// and it goes red.
func TestParser_ESCInterrupt_IsApiErrorMessageFalseIsNotAnError(t *testing.T) {
	lines := fixtureLines(t, scenarioFixture(t, fixtureESCInterrupt))

	p := &Parser{}
	present := 0
	for i, raw := range lines {
		ev := p.ParseLine(raw)
		v, ok := raw["isApiErrorMessage"]
		if !ok {
			continue
		}
		present++
		if b, isBool := v.(bool); !isBool || b {
			t.Fatalf("line %d: this fixture is supposed to carry "+
				"isApiErrorMessage:false, got %#v — the negative case it exists "+
				"to pin is gone", i+1, v)
		}
		if ev.SessionError != nil {
			t.Errorf("line %d: isApiErrorMessage:false was read as a session error "+
				"(%+v). Match on the VALUE, never on field presence — every ESC "+
				"interrupt carries this field.", i+1, ev.SessionError)
		}
	}
	if present == 0 {
		t.Fatal("no isApiErrorMessage field found in the ESC-interrupt fixture — " +
			"this test cannot observe the trap it exists to pin")
	}
}

// TestTailer_TerminalAPIError_SurvivesItsOwnTurnBoundary is the end-to-end
// defect test for the second half of the terminal shape.
//
// In the recording, claudecode writes `system`/`turn_duration` on the line
// immediately after the error epilogue. That maps to EventType "turn_done", and
// the tailer's clearing rule wiped a standing error on any turn boundary — so
// the session read green a fraction of a second after failing, which is the
// exact outcome the fourth state exists to prevent. ErrorPhaseTerminal's own
// doc already said "Only the next turn the user starts clears it"; the code did
// not implement it.
func TestTailer_TerminalAPIError_SurvivesItsOwnTurnBoundary(t *testing.T) {
	path := scenarioFixture(t, fixtureAPIErrorTerminal)
	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.SessionError == nil {
		t.Fatal("SessionError is nil after replaying the whole recording — the " +
			"terminal error was cleared by the turn_duration epilogue that " +
			"belongs to the very turn that failed")
	}
	if m.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal", m.SessionError.Phase)
	}
}

// TestTailer_RetryingAPIError_SurfacedThroughMetrics is the same end-to-end
// check for the retry ladder, and it is what makes the skipped-path plumbing
// observable: api_error is Skip=true, so nothing about it reaches metrics
// unless applyMetadata folds it.
func TestTailer_RetryingAPIError_SurfacedThroughMetrics(t *testing.T) {
	path := scenarioFixture(t, fixtureAPIErrorRetrying)
	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.SessionError == nil {
		t.Fatal("SessionError is nil after replaying the api_error ladder")
	}
	if m.SessionError.Phase != tailer.ErrorPhaseRetrying {
		t.Errorf("Phase = %q, want retrying", m.SessionError.Phase)
	}
	// The last api_error in this recording is attempt 6 of 10 — the ladder is
	// abandoned when the user intervenes, never exhausted. Pinned because it is
	// the evidence behind "terminal-ness is the adapter's verdict, never
	// Attempt == MaxAttempts".
	if m.SessionError.Attempt == nil || *m.SessionError.Attempt != 6 {
		t.Errorf("Attempt = %v, want 6 (the last rung of the recorded ladder)",
			m.SessionError.Attempt)
	}
	if m.SessionError.MaxAttempts == nil || *m.SessionError.MaxAttempts != 10 {
		t.Errorf("MaxAttempts = %v, want 10", m.SessionError.MaxAttempts)
	}
}

// TestTailer_APIErrorOnlyPassIsSubstantive pins the third defect the first
// producer makes reachable.
//
// claudecode's api_error is Skip=true, and applySkippedEvent decides whether a
// pass counts as substantive activity. A pass whose only new bytes are error
// lines used to report NoSubstantiveActivity, which the detector short-circuits
// on (#329) — so the session would never be re-classified and would sit green
// for the whole retry window despite the tailer holding the error.
func TestTailer_APIErrorOnlyPassIsSubstantive(t *testing.T) {
	lines := fixtureLines(t, scenarioFixture(t, fixtureAPIErrorRetrying))
	var apiErrors []map[string]interface{}
	for _, raw := range lines {
		if sub, _ := raw["subtype"].(string); sub == "api_error" {
			apiErrors = append(apiErrors, raw)
		}
	}
	if len(apiErrors) == 0 {
		t.Fatal("no api_error lines extracted from the fixture — this test " +
			"cannot observe the pass it exists to pin")
	}

	path := writeTranscript(t, apiErrors)
	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.SessionError == nil {
		t.Fatal("SessionError is nil for an api_error-only transcript")
	}
	if m.NoSubstantiveActivity {
		t.Error("a pass that read a session error reported NoSubstantiveActivity — " +
			"the detector short-circuits on that flag, so the failure would never " +
			"be classified and the session would stay green")
	}
}
