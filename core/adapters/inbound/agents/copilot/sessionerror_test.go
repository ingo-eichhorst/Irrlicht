package copilot

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// The two committed recordings that carry a `session.error`. Both are covered
// by the replaydata deletion guard (#268), so a missing one is a broken
// checkout, never a reason to pass — see the same argument in claudecode's
// sessionerror_test.go.
const (
	fixtureRateLimit = "2-9_token-quota-exhausted/recordings/" +
		"2026-08-05-17-30-40_irrlichd-0.5.9+4dbfef7/transcript.jsonl"
	fixtureQuery = "2-14_turn-aborted-by-error/recordings/" +
		"2026-08-05-17-29-03_irrlichd-0.5.9+2fc9751/transcript.jsonl"
)

func scenarioFixture(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "..", "replaydata", "agents",
		"copilot", "scenarios", filepath.FromSlash(rel))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed fixture missing at %s: %v — this recording is "+
			"deletion-guarded (#268)", path, err)
	}
	return path
}

// sessionErrorLine returns the single `session.error` record in a committed
// recording. It fails when there is none, so a fixture that stopped carrying
// the shape cannot read as "nothing to check".
func sessionErrorLine(t *testing.T, path string) map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var found []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("decode line in %s: %v", path, err)
		}
		if typ, _ := raw["type"].(string); typ == evSessionError {
			found = append(found, raw)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one %s record in %s, found %d",
			evSessionError, path, len(found))
	}
	return found[0]
}

// TestParser_SessionError_RateLimitFromRecording covers the shape that carries
// a status code.
func TestParser_SessionError_RateLimitFromRecording(t *testing.T) {
	raw := sessionErrorLine(t, scenarioFixture(t, fixtureRateLimit))

	p := &Parser{}
	ev := p.ParseLine(raw)
	if ev.SessionError == nil {
		t.Fatal("session.error produced no SessionError — the event type was " +
			"absent from eventHandlers and fell through to the default skip")
	}
	if ev.SessionError.Class != "rate_limit" {
		t.Errorf("Class = %q, want rate_limit (copilot's own errorType, verbatim)",
			ev.SessionError.Class)
	}
	if ev.SessionError.HTTPStatus == nil || *ev.SessionError.HTTPStatus != 429 {
		t.Errorf("HTTPStatus = %v, want 429", ev.SessionError.HTTPStatus)
	}
	if !strings.HasPrefix(ev.SessionError.Message, "Failed to get response from the AI model") {
		t.Errorf("Message = %q, want copilot's verbatim text", ev.SessionError.Message)
	}
	// Copilot's payload says nothing about whether another attempt is coming:
	// there is no retry counter, no phase field, and copilot emits no
	// per-attempt error event at all. Unknown is the honest answer, and it is
	// the case session.ErrorPhaseUnknown's own doc names.
	if ev.SessionError.Phase != tailer.ErrorPhaseUnknown {
		t.Errorf("Phase = %q, want the unknown zero value", ev.SessionError.Phase)
	}
	if ev.SessionError.Attempt != nil || ev.SessionError.MaxAttempts != nil ||
		ev.SessionError.RetryIn != nil {
		t.Errorf("copilot reports no retry counters; got attempt=%v max=%v retryIn=%v",
			ev.SessionError.Attempt, ev.SessionError.MaxAttempts, ev.SessionError.RetryIn)
	}
	// Scope lock: session-level failure, not a failed tool result.
	if ev.IsError {
		t.Error("session.error must not set IsError — that channel is tool-result failure")
	}
}

// TestParser_SessionError_QueryHasNoStatusCode is why HTTPStatus is a pointer.
// This recorded shape omits statusCode entirely, and a plain int would report
// the absence as HTTP 0.
func TestParser_SessionError_QueryHasNoStatusCode(t *testing.T) {
	raw := sessionErrorLine(t, scenarioFixture(t, fixtureQuery))
	if data, _ := raw["data"].(map[string]any); data != nil {
		if _, present := data["statusCode"]; present {
			t.Fatal("this fixture is supposed to omit statusCode — the absent-status " +
				"case it exists to pin is gone")
		}
	}

	p := &Parser{}
	ev := p.ParseLine(raw)
	if ev.SessionError == nil {
		t.Fatal("session.error produced no SessionError")
	}
	if ev.SessionError.Class != "query" {
		t.Errorf("Class = %q, want query", ev.SessionError.Class)
	}
	if ev.SessionError.HTTPStatus != nil {
		t.Errorf("HTTPStatus = %v, want nil — the payload carries no statusCode, "+
			"and 0 would be a fabricated code", *ev.SessionError.HTTPStatus)
	}
	if !strings.HasPrefix(ev.SessionError.Message, "Could not connect to local model provider") {
		t.Errorf("Message = %q, want copilot's verbatim text", ev.SessionError.Message)
	}
}

// TestTailer_SessionError_SurvivesToEndOfRecording is the end-to-end half: the
// error must still stand once the whole recording has been consumed.
//
// It is also the ordering check. copilot writes assistant.turn_end ~6ms BEFORE
// session.error in both recordings, and session.shutdown after it. The turn
// boundary therefore lands while there is nothing to clear, and the shutdown
// carries no boundary of its own — so the session finishes red rather than
// settling green on its own epilogue.
func TestTailer_SessionError_SurvivesToEndOfRecording(t *testing.T) {
	for name, rel := range map[string]string{
		"rate_limit": fixtureRateLimit,
		"query":      fixtureQuery,
	} {
		t.Run(name, func(t *testing.T) {
			path := scenarioFixture(t, rel)
			m, err := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName).TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if m.SessionError == nil {
				t.Fatal("SessionError is nil after replaying the whole recording")
			}
			if m.SessionError.Class != name {
				t.Errorf("Class = %q, want %q", m.SessionError.Class, name)
			}
		})
	}
}
