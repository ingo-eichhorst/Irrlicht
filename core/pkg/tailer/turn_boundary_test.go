package tailer

import (
	"os"
	"path/filepath"
	"testing"
)

// turnBoundaryErrParser reports a session error of the configured phase on a
// line whose "kind" is "boom", and nothing on any other line.
type turnBoundaryErrParser struct{ phase ErrorPhase }

func (p turnBoundaryErrParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	kind, _ := raw["kind"].(string)
	ev := &ParsedEvent{EventType: kind, Timestamp: ParseTimestamp(raw)}
	switch kind {
	case "boom":
		ev.SessionError = &SessionError{
			Phase:   p.phase,
			Class:   "provider",
			Message: "the provider failed the call",
		}
	case "user":
		// A genuine new prompt: ClearToolNames with no tool results, which is
		// what StartsNewUserTurn matches.
		ev.ClearToolNames = true
	case "toolresult":
		// Claude Code delivers tool results on USER-ROLE lines, so this raises
		// ClearToolNames too — the shape #1798's len(ToolResultIDs) == 0 guard
		// exists to tell apart from a real prompt.
		ev.ClearToolNames = true
		ev.ToolResultIDs = []string{"tu_1"}
	}
	return ev
}

// writeTurnBoundaryTranscript writes raw JSONL and returns its path.
func writeTurnBoundaryTranscript(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const boomLine = `{"kind":"boom","timestamp":"2026-08-05T10:00:00Z"}` + "\n"

// TestIngestTurnBoundary_AppliesTheSameClearingRuleAsTheTranscript is the
// central property: an out-of-band turn boundary (the Stop hook) retires a
// session error under EXACTLY the clearing rule a transcript turn boundary
// applies — retrying errors only.
//
// The hook and the transcript describe the same event, so a rule that differs
// between them is decided by delivery latency rather than by what happened.
// That is what #1799's first draft got wrong: it cleared unconditionally,
// which erased a terminal failure the transcript path had just been taught to
// preserve — through a different channel, on the same turn boundary, in the
// same change.
func TestIngestTurnBoundary_AppliesTheSameClearingRuleAsTheTranscript(t *testing.T) {
	for _, tc := range []struct {
		phase   ErrorPhase
		wantNil bool
		why     string
	}{
		{
			phase:   ErrorPhaseRetrying,
			wantNil: true,
			why: "the agent said another attempt was coming and the turn then " +
				"completed, so the retry window ended in success",
		},
		{
			phase:   ErrorPhaseTerminal,
			wantNil: false,
			why: "the boundary that follows a give-up IS the failed turn's own " +
				"epilogue — claudecode writes system/turn_duration on the line " +
				"after its API-error message, and its Stop hook fires for that " +
				"same turn",
		},
		{
			phase:   ErrorPhaseUnknown,
			wantNil: false,
			why: "the agent did not say whether another attempt was coming, and " +
				"an unknown phase must not be read as a recovery — copilot's " +
				"session.error is exactly this, and it lands AFTER its turn_end",
		},
	} {
		t.Run(string(tc.phase)+"|"+map[bool]string{true: "cleared", false: "stands"}[tc.wantNil], func(t *testing.T) {
			path := writeTurnBoundaryTranscript(t, boomLine)
			tt := NewTranscriptTailer(path, turnBoundaryErrParser{phase: tc.phase}, "test")

			m, err := tt.TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if m.SessionError == nil {
				t.Fatal("precondition failed: the fixture parser produced no error, " +
					"so this case cannot observe a clear")
			}

			tt.IngestTurnBoundary()

			// A pass that reads no new bytes: surfaceSporadicMetrics still runs,
			// which is how an errored-but-idle session keeps reporting its error.
			m, err = tt.TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess after the boundary: %v", err)
			}
			if got := m.SessionError == nil; got != tc.wantNil {
				t.Errorf("after IngestTurnBoundary, SessionError == nil is %v, want %v (%+v)\n%s",
					got, tc.wantNil, m.SessionError, tc.why)
			}
		})
	}
}

// TestIngestTurnBoundary_MatchesTheTranscriptArm is the agreement check the
// case above argues for: for every phase, driving a transcript `turn_done`
// line and calling IngestTurnBoundary must leave the tailer in the same state.
//
// Written as a comparison rather than as two hard-coded expectations so the two
// paths cannot drift apart silently — which is the failure this whole pair
// exists to prevent.
func TestIngestTurnBoundary_MatchesTheTranscriptArm(t *testing.T) {
	for _, phase := range []ErrorPhase{ErrorPhaseRetrying, ErrorPhaseTerminal, ErrorPhaseUnknown} {
		t.Run(string(phase), func(t *testing.T) {
			// Transcript arm: the boundary arrives as a parsed line.
			viaTranscript := NewTranscriptTailer(
				writeTurnBoundaryTranscript(t, boomLine+
					`{"kind":"turn_done","timestamp":"2026-08-05T10:00:01Z"}`+"\n"),
				turnBoundaryErrParser{phase: phase}, "test")
			mT, err := viaTranscript.TailAndProcess()
			if err != nil {
				t.Fatalf("transcript arm: %v", err)
			}

			// Hook arm: the same boundary arrives out of band.
			viaHook := NewTranscriptTailer(
				writeTurnBoundaryTranscript(t, boomLine),
				turnBoundaryErrParser{phase: phase}, "test")
			if _, err := viaHook.TailAndProcess(); err != nil {
				t.Fatalf("hook arm first pass: %v", err)
			}
			viaHook.IngestTurnBoundary()
			mH, err := viaHook.TailAndProcess()
			if err != nil {
				t.Fatalf("hook arm second pass: %v", err)
			}

			if (mT.SessionError == nil) != (mH.SessionError == nil) {
				t.Errorf("the two channels disagree for phase %q: transcript nil=%v, hook nil=%v — "+
					"a Stop hook and the transcript line it precedes describe the SAME turn "+
					"boundary, so a difference here is decided by delivery latency",
					phase, mT.SessionError == nil, mH.SessionError == nil)
			}
		})
	}
}

// TestIngestTurnBoundary_DoesNotEraseAnErrorItNeverSaw pins the ordering half
// of the same defect.
//
// SessionDetector.HandleStopHook calls this BEFORE the classify pass that tails
// the transcript, so on the common ordering the hook lands while the tailer has
// not yet read the error line at all. A clear must therefore be a no-op on an
// empty slot rather than any kind of latch — otherwise the error recorded a
// moment later would be suppressed by a boundary that preceded it.
func TestIngestTurnBoundary_DoesNotEraseAnErrorItNeverSaw(t *testing.T) {
	path := writeTurnBoundaryTranscript(t, boomLine)
	tt := NewTranscriptTailer(path, turnBoundaryErrParser{phase: ErrorPhaseTerminal}, "test")

	// The hook arrives first — nothing has been tailed yet.
	tt.IngestTurnBoundary()

	m, err := tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.SessionError == nil {
		t.Fatal("an error recorded AFTER an out-of-band turn boundary was suppressed by it — " +
			"the boundary describes the turn before, not the failure that follows")
	}
}

// TestClearSessionErrorOnRecovery_WhatEachEventDoes pins the whole transcript
// clearing rule as a table, across every phase, so the two checks inside it
// cannot be reordered or re-guarded without a failure.
//
// #1799 reordered them (StartsNewUserTurn moved above the turn_done arm) and
// added the phase gate to the turn_done arm alone. The properties that must
// hold afterwards:
//
//   - a new user prompt clears EVERY phase — a user who typed again has moved
//     on, and holding the old turn's failure against the new one would pin a
//     terminal error red for the rest of the session;
//   - a turn boundary clears ONLY a retrying error (ClearedByTurnBoundary);
//   - a TOOL RESULT clears nothing, at any phase. It arrives on a user-role
//     line and raises ClearToolNames, so a bare check would retire the error on
//     the next tool round-trip — about a second, i.e. never visible. That is
//     the defect #1798 fixed and this reorder must not reintroduce.
func TestClearSessionErrorOnRecovery_WhatEachEventDoes(t *testing.T) {
	for _, phase := range []ErrorPhase{ErrorPhaseRetrying, ErrorPhaseTerminal, ErrorPhaseUnknown} {
		for _, ev := range []struct {
			kind      string
			wantClear bool
			why       string
		}{
			{"user", true, "a genuine new prompt starts the next turn"},
			{"turn_done", phase == ErrorPhaseRetrying, "only a retrying error is retired by a completed turn"},
			{"toolresult", false, "a tool round-trip is not a recovery (#1798)"},
			{"assistant", false, "ordinary activity retires nothing"},
		} {
			t.Run(string(phase)+"/"+ev.kind, func(t *testing.T) {
				path := writeTurnBoundaryTranscript(t, boomLine+
					`{"kind":"`+ev.kind+`","timestamp":"2026-08-05T10:00:01Z"}`+"\n")
				m, err := NewTranscriptTailer(path, turnBoundaryErrParser{phase: phase}, "test").TailAndProcess()
				if err != nil {
					t.Fatalf("TailAndProcess: %v", err)
				}
				if got := m.SessionError == nil; got != ev.wantClear {
					t.Errorf("after a %q event with a %q error, cleared = %v, want %v — %s",
						ev.kind, phase, got, ev.wantClear, ev.why)
				}
			})
		}
	}
}
