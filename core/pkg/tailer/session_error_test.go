package tailer

import (
	"testing"
	"time"

	"irrlicht/core/pkg/capacity"
)

// sessionErrorTestParser is a parser written for these tests rather than a
// reuse of testParser, and that is deliberate.
//
// testParser's handleTestSystemEvent is a stub: it reads a fixed handful of
// subtypes and knows nothing about session errors, so a test routed through it
// would drive the tailer with a ParsedEvent whose SessionError is always nil
// and would pass no matter what the production code did. That is the #1076
// failure exactly — seven acceptance criteria written against this package
// whose stub never read the field under test, so every one of them would have
// passed on main while the bug shipped.
//
// This parser maps the transcript shapes below onto the two things the
// clearing rule turns on — a session error, and a turn boundary — and nothing
// else:
//
//	{"kind":"error", ...}  → SessionError, Skip as configured
//	{"kind":"turn_done"}   → EventType "turn_done"
//	{"kind":"user"}        → a real user turn boundary (ClearToolNames)
//	{"kind":"assistant"}   → ordinary mid-turn activity
type sessionErrorTestParser struct {
	// skipErrors mirrors claudecode's real routing: its `system`/`api_error`
	// event is Skip=true, so it reaches applySkippedEvent and never
	// processParsedEvent. Making this configurable is the whole point of the
	// fixture — see TestSessionError_RecordedOnBothRoutingPaths.
	skipErrors bool
}

func (p *sessionErrorTestParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{Timestamp: ParseTimestamp(raw)}
	kind, _ := raw["kind"].(string)

	switch kind {
	case "error":
		ev.EventType = "system"
		ev.Skip = p.skipErrors
		se := &SessionError{Class: "rate_limit", Message: "slow down"}
		if phase, ok := raw["phase"].(string); ok {
			se.Phase = ErrorPhase(phase)
		}
		if status, ok := raw["status"].(float64); ok {
			s := int(status)
			se.HTTPStatus = &s
		}
		if attempt, ok := raw["attempt"].(float64); ok {
			a := int(attempt)
			se.Attempt = &a
		}
		if retryMs, ok := raw["retry_in_ms"].(float64); ok {
			d := time.Duration(retryMs * float64(time.Millisecond))
			se.RetryIn = &d
		}
		ev.SessionError = se
	case "turn_done":
		ev.EventType = "turn_done"
	case "user":
		ev.EventType = "user"
		ev.ClearToolNames = true
	default:
		ev.EventType = "assistant"
	}
	return ev
}

func newSessionErrorTailer(path string, skipErrors bool) *TranscriptTailer {
	tl := NewTranscriptTailer(path, &sessionErrorTestParser{skipErrors: skipErrors}, "test-adapter")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

// TestSessionError_RecordedOnBothRoutingPaths is the #1256-shaped defect this
// design exists to avoid, made explicit.
//
// scanParsedLine routes an event down exactly one of two mutually exclusive
// paths — applySkippedEvent for Skip=true, processParsedEvent otherwise — and
// the two error shapes that matter land on OPPOSITE sides of that fork:
// claudecode's `system`/`api_error` is Skip=true (it falls through
// handleSystemEvent's catch-all), while copilot's `session.error` is an
// ordinary event. Folding the error in processParsedEvent alone would work for
// one adapter, silently drop the other, and pass every test written against
// the adapter that happened to work.
//
// So this runs the identical transcript twice, differing only in Skip.
func TestSessionError_RecordedOnBothRoutingPaths(t *testing.T) {
	for _, tc := range []struct {
		name       string
		skipErrors bool
	}{
		{"skipped-event (claudecode system/api_error)", true},
		{"ordinary event (copilot session.error)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTranscriptLines(t, []map[string]interface{}{
				{"kind": "user", "timestamp": ts(0)},
				{"kind": "assistant", "timestamp": ts(1)},
				{"kind": "error", "phase": "retrying", "status": 429, "attempt": 1, "retry_in_ms": 616.45, "timestamp": ts(2)},
			})

			m, err := newSessionErrorTailer(path, tc.skipErrors).TailAndProcess()
			if err != nil {
				t.Fatal(err)
			}
			if m.SessionError == nil {
				t.Fatalf("SessionError is nil — the error was dropped on the %s path. "+
					"The fold must live in applyMetadata, the only function BOTH routing "+
					"paths call.", tc.name)
			}
			if m.SessionError.Phase != ErrorPhaseRetrying {
				t.Errorf("Phase = %q, want retrying", m.SessionError.Phase)
			}
			if m.SessionError.HTTPStatus == nil || *m.SessionError.HTTPStatus != 429 {
				t.Errorf("HTTPStatus = %v, want 429", m.SessionError.HTTPStatus)
			}
			// The fractional retry delay must survive: claudecode writes
			// retryInMs as a float (616.4520045919932 in the recordings), so a
			// millisecond-int field would silently truncate it.
			if m.SessionError.RetryIn == nil {
				t.Fatal("RetryIn is nil")
			}
			if got := *m.SessionError.RetryIn; got <= 616*time.Millisecond || got >= 617*time.Millisecond {
				t.Errorf("RetryIn = %v, want a fractional ~616.45ms — an int-milliseconds "+
					"field would have truncated this", got)
			}
		})
	}
}

// TestSessionError_ClearedByTurnDone is the settled clearing rule's retry half
// (#1796): the session sits in error for the whole retry window and settles on
// the eventual turn boundary. This is what makes provider-overloaded-retry end
// green instead of staying red forever.
func TestSessionError_ClearedByTurnDone(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
		{"kind": "error", "phase": "retrying", "attempt": 1, "timestamp": ts(1)},
		{"kind": "error", "phase": "retrying", "attempt": 2, "timestamp": ts(2)},
		{"kind": "error", "phase": "retrying", "attempt": 3, "timestamp": ts(3)},
		{"kind": "assistant", "timestamp": ts(4)}, // the retry finally succeeded
		{"kind": "turn_done", "timestamp": ts(5)},
	})

	m, err := newSessionErrorTailer(path, true).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.SessionError != nil {
		t.Fatalf("the turn completed, so the error must be cleared; got %+v", m.SessionError)
	}
}

// TestSessionError_ClearedByNextUserTurn is the other half: error→working when
// the next turn starts.
//
// It is the ONLY thing that clears a terminal give-up, because a give-up
// produces no turn boundary of its own — so without it a session that failed
// once would read red for the rest of its life.
func TestSessionError_ClearedByNextUserTurn(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
		{"kind": "error", "phase": "terminal", "timestamp": ts(1)},
		{"kind": "user", "timestamp": ts(2)}, // the user tries again
	})

	m, err := newSessionErrorTailer(path, true).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.SessionError != nil {
		t.Fatalf("a new user turn must clear the previous turn's failure; got %+v", m.SessionError)
	}
}

// TestSessionError_SurvivesMidTurnActivity is the guard against clearing too
// eagerly, and the reason clearSessionErrorOnRecovery keys on ClearToolNames
// rather than on a user-role event type.
//
// Tool results arrive on user-role events in several transcript formats. If
// those counted as "the next turn started", a mid-turn error would be cleared
// by the next tool round-trip — roughly one second later — which is
// indistinguishable from never having shown it at all.
func TestSessionError_SurvivesMidTurnActivity(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
		{"kind": "error", "phase": "retrying", "attempt": 1, "timestamp": ts(1)},
		{"kind": "assistant", "timestamp": ts(2)},
		{"kind": "assistant", "timestamp": ts(3)},
	})

	m, err := newSessionErrorTailer(path, true).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.SessionError == nil {
		t.Fatal("ordinary mid-turn activity must NOT clear a standing error — " +
			"only a completed turn or a new user turn does")
	}
}

// TestSessionError_SurvivesEmptyMessageHistory is why the surfacing lives in
// surfaceSporadicMetrics rather than in the body of computeMetrics.
//
// computeMetrics returns early when MessageHistory is empty, and everything
// below that return is skipped. surfaceSporadicMetrics is called ABOVE it,
// alongside the other "must run even on an empty pass" bookkeeping.
//
// The case where that distinction bites is not exotic — it is the headline
// provider-overloaded shape. Skipped events never reach addMessageEvent, and
// claudecode's `system`/`api_error` is Skip=true, so a session whose newly
// observed transcript content is nothing but retry errors has an EMPTY
// MessageHistory and takes that early return. Surfacing the error below it
// would drop it on exactly the session that has one.
//
// The fixture is therefore a transcript of only skipped error events, which
// is what the recorded token-quota ladder looks like between one turn and the
// next.
func TestSessionError_SurvivesEmptyMessageHistory(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "error", "phase": "retrying", "attempt": 1, "timestamp": ts(0)},
		{"kind": "error", "phase": "retrying", "attempt": 2, "timestamp": ts(1)},
	})

	tl := newSessionErrorTailer(path, true)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// The precondition IS the test's premise: assert the early-return path was
	// actually taken, so this can never quietly become a test of the ordinary
	// path that proves nothing about the placement.
	if len(m.MessageHistory) != 0 {
		t.Fatalf("precondition: skipped events must not enter MessageHistory (got %d) — "+
			"without an empty history computeMetrics does not take its early return and "+
			"this test no longer discriminates where SessionError is surfaced",
			len(m.MessageHistory))
	}

	if m.SessionError == nil {
		t.Fatal("the error was dropped on a pass whose MessageHistory is empty — it is " +
			"surfaced from below computeMetrics' early return instead of from " +
			"surfaceSporadicMetrics, which runs above it")
	}
}

// TestSessionError_SurvivesAnIdlePoll is the everyday half: once a session has
// message history, every later poll that reads no new bytes must still report
// the standing error rather than flickering it away.
func TestSessionError_SurvivesAnIdlePoll(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
		{"kind": "error", "phase": "terminal", "timestamp": ts(1)},
	})

	tl := newSessionErrorTailer(path, true)
	first, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionError == nil {
		t.Fatal("precondition: the first pass must observe the error")
	}

	second, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionError == nil {
		t.Fatal("an idle poll dropped the standing error")
	}
}

// TestSessionError_LatestErrorWins pins that a retry ladder reports its most
// recent rung rather than its first.
//
// A user watching a red session wants "attempt 5", not "attempt 1" — and the
// recorded ladders climb (1..5 of 10 in claudecode's token-quota recording),
// so keeping the first would freeze the display at the least informative
// value.
func TestSessionError_LatestErrorWins(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
		{"kind": "error", "phase": "retrying", "attempt": 1, "timestamp": ts(1)},
		{"kind": "error", "phase": "retrying", "attempt": 2, "timestamp": ts(2)},
		{"kind": "error", "phase": "retrying", "attempt": 3, "timestamp": ts(3)},
	})

	m, err := newSessionErrorTailer(path, true).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.SessionError == nil || m.SessionError.Attempt == nil {
		t.Fatalf("expected a retrying error carrying an attempt, got %+v", m.SessionError)
	}
	if got := *m.SessionError.Attempt; got != 3 {
		t.Errorf("Attempt = %d, want 3 (the latest rung of the ladder)", got)
	}
}

// TestSessionError_TurnDoneAndErrorOnOneEventKeepsTheError covers the ordering
// inside applySessionError.
//
// A parser that reports BOTH a failure and a turn boundary on one event is
// describing a turn that ENDED IN FAILURE — claudecode's terminal
// `isApiErrorMessage` message is exactly that shape, a synthesized assistant
// message carrying a terminal stop_reason. Clearing after recording would make
// that shape self-erasing and paint the session green, which is the precise
// defect the fourth state exists to fix.
func TestSessionError_TurnDoneAndErrorOnOneEventKeepsTheError(t *testing.T) {
	tl := newSessionErrorTailer(writeTranscriptLines(t, []map[string]interface{}{
		{"kind": "user", "timestamp": ts(0)},
	}), true)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}

	// Drive one event carrying both facts straight through the fold, which is
	// the only way to construct the simultaneity — a transcript line can only
	// produce one ParsedEvent, and no shipped parser emits this pair yet.
	tl.applySessionError(&ParsedEvent{
		EventType:    "turn_done",
		SessionError: &SessionError{Phase: ErrorPhaseTerminal, Class: "provider"},
	})

	if tl.sessionError == nil {
		t.Fatal("an event reporting a failure AND a turn boundary describes a turn that " +
			"ended in failure; the error must survive it")
	}
}
