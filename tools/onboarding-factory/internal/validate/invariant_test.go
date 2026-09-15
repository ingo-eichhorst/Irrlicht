package validate

import (
	"strings"
	"testing"
	"time"
)

// The invariant DSL had no tests at all before this file, which is how a
// silent-skip path shipped: checkInvariant returned ok=true for a string it
// could not parse, and checkPhaseInvariants discarded the reason on the
// success path, so an unevaluable invariant and a satisfied one produced the
// identical note ("invariant ok: …").
//
// Measured blast radius when this was found, by applying the two live regexes
// to every invariant in every committed cell: 68 of 632 invariants (10%),
// across 10 of the 13 adapters, were reported as passing without ever being
// checked.

var invTestBase = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

// invEvents builds a tiny recording: the matched anchor, then one
// state_transition to `state` for `sid` 100ms later.
func invEvents(sid, state string) (events []recordedEvent, matched *recordedEvent) {
	events = []recordedEvent{
		{Ts: invTestBase, Kind: "state_transition", SessionID: sid, NewState: "working"},
		{Ts: invTestBase.Add(100 * time.Millisecond), Kind: "state_transition", SessionID: sid, NewState: state},
	}
	return events, &events[0]
}

// TestCheckInvariant_unknownFormIsReportedAsSkipped pins the core rule from
// AGENTS.md: absence of a finding and inability to look must never produce the
// same output. An invariant the DSL cannot parse is not evidence, so it must
// not be reported as satisfied.
func TestCheckInvariant_unknownFormIsReportedAsSkipped(t *testing.T) {
	// Real strings taken from committed cells. "no function_call event" appears
	// verbatim in at least seven muse cells; function_call is not an
	// events.jsonl kind at all (it is a parser-internal EventType), so it can
	// never be reworded into the DSL — it has to surface and be removed.
	for _, inv := range []string{
		"no function_call event",
		"no tool_use events",
		"no child sessions spawned",
		"no state_transition to ready after this phase",
	} {
		t.Run(inv, func(t *testing.T) {
			events, matched := invEvents("s1", "ready")
			p := ExpectedPhase{Phase: "p", Invariants: []string{inv}}
			r := &ExpectedResult{Phase: "p"}
			_, fail := checkPhaseInvariants(p, events, matched, r)

			// The skip is visible but not fatal. Failing the phase was tried
			// and reverted: see the DSL doc block in expected.go. What must
			// never happen is it reading as a pass.
			if fail {
				t.Errorf("unparseable invariant %q failed the phase; it should be recorded as skipped", inv)
			}
			if len(r.Notes) != 1 {
				t.Fatalf("notes = %v, want exactly one note", r.Notes)
			}
			if strings.HasPrefix(r.Notes[0], "invariant ok:") {
				t.Errorf("note %q reports an unevaluable invariant as ok", r.Notes[0])
			}
			if !strings.Contains(r.Notes[0], "SKIPPED") {
				t.Errorf("note %q does not say the invariant was skipped", r.Notes[0])
			}
		})
	}
}

// TestCheckInvariant_trailingNounFormIsDeliberatelyNotParsed pins the revert.
//
// "no state_transition to ready for parent" reads as if it scopes to the
// parent. It does not, and could not: both DSL forms scope to
// matched.SessionID — whichever session the PHASE matched. Measured on
// claudecode's own 3-1_foreground-subagent recording, that phase matches the
// CHILD, so parsing this string would check the child while the cell claims
// the parent. Widening the grammar was tried and reverted for exactly that
// reason; 51 strings of this shape stay skipped-and-counted instead.
func TestCheckInvariant_trailingNounFormIsDeliberatelyNotParsed(t *testing.T) {
	for _, inv := range []string{
		"no state_transition to ready for parent",
		"no state_transition to waiting for session",
	} {
		t.Run(inv, func(t *testing.T) {
			if _, _, known := checkInvariant(inv, nil, &recordedEvent{}, time.Time{}); known {
				t.Errorf("%q parsed; it must stay unparsed until the DSL can really scope to the named session", inv)
			}
		})
	}
}

// TestCheckInvariant_knownFormsStillBehave guards the two shapes that already
// worked, so the grammar change cannot quietly break them.
func TestCheckInvariant_knownFormsStillBehave(t *testing.T) {
	t.Run("bare state form still fires", func(t *testing.T) {
		events, matched := invEvents("s1", "ready")
		p := ExpectedPhase{Phase: "p", Invariants: []string{"no state_transition to ready"}}
		if _, fail := checkPhaseInvariants(p, events, matched, &ExpectedResult{}); !fail {
			t.Error("bare state form did not fire against a violating recording")
		}
	})
	t.Run("kind form still fires", func(t *testing.T) {
		events := []recordedEvent{
			{Ts: invTestBase, Kind: "state_transition", SessionID: "s1", NewState: "working"},
			{Ts: invTestBase.Add(50 * time.Millisecond), Kind: "transcript_removed", SessionID: "s1"},
		}
		p := ExpectedPhase{Phase: "p", Invariants: []string{"no transcript_removed for primary session"}}
		if _, fail := checkPhaseInvariants(p, events, &events[0], &ExpectedResult{}); !fail {
			t.Error("kind form did not fire against a violating recording")
		}
	})
}
