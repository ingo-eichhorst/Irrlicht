package session

import "testing"

// The back-fill's vocabulary (#1905): a span carries a `source` when it was
// reconstructed rather than measured, and a source that cannot say how a run
// ended carries `unknown`.

// `unknown` is NOT a session state, and every derived vocabulary must keep
// refusing it. This is the guard that stops a fifth state from arriving
// through the back door: the day someone adds `unknown` to canonicalStates,
// every consumer of that list — the activity matrix's stack, the history
// strip, the state-vocabulary linter's own corpus — silently gains a state
// nothing ever transitions into.
func TestAutonomyReasonUnknownIsNotASessionState(t *testing.T) {
	if IsCanonicalState(AutonomyReasonUnknown) {
		t.Fatal("AutonomyReasonUnknown is a canonical state — it must never be one")
	}
	if IsAutonomyEndReason(AutonomyReasonUnknown) {
		t.Fatal("AutonomyReasonUnknown reads back as an end reason")
	}
	for _, s := range CanonicalStates() {
		if s == AutonomyReasonUnknown {
			t.Fatal("CanonicalStates() yields `unknown`")
		}
	}
	for _, r := range AutonomyEndReasons() {
		if r == AutonomyReasonUnknown {
			t.Fatal("AutonomyEndReasons() yields `unknown`")
		}
	}
}

// It ranks below every measured reason on the strip's collapse ladder, so one
// reconstructed span can never grey out a column that also holds a real error.
func TestAutonomyReasonUnknownRanksLowest(t *testing.T) {
	reasons := AutonomyEndReasons()
	if len(reasons) == 0 {
		t.Fatal("AutonomyEndReasons() is empty — cannot verify anything")
	}
	unknown := AutonomyReasonPriority(AutonomyReasonUnknown)
	for _, r := range reasons {
		if unknown >= AutonomyReasonPriority(r) {
			t.Fatalf("`unknown` (%d) outranks or ties %q (%d)", unknown, r, AutonomyReasonPriority(r))
		}
	}
	// And it ranks the same as any other reason this build cannot name: the
	// neutral column both clients already draw.
	if unknown != AutonomyReasonPriority("") {
		t.Fatalf("`unknown` (%d) ranks differently from an unnamed reason (%d); the clients draw both "+
			"in the same neutral colour and the ladder must agree", unknown, AutonomyReasonPriority(""))
	}
}

func TestIsAutonomyReconstructed(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   bool
	}{
		{"absent is the live case", "", false},
		{"the event log", AutonomySourceLog, true},
		{"the cost log", AutonomySourceCost, true},
		// A row written by a NEWER back-fill must not read back as measured
		// just because this build does not recognize its source. That is the
		// one direction of this field that silently turns a reconstruction
		// into a measurement.
		{"an unrecognized source is still reconstructed", "some-future-source", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAutonomyReconstructed(tc.source); got != tc.want {
				t.Fatalf("IsAutonomyReconstructed(%q) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

// The two sources are distinct and neither collides with the live case.
func TestAutonomySourcesAreDistinctAndNonEmpty(t *testing.T) {
	sources := AutonomySources()
	if len(sources) != 2 {
		t.Fatalf("AutonomySources() = %v, want two", sources)
	}
	seen := map[string]bool{}
	for _, s := range sources {
		if s == "" {
			t.Fatal("a source is the empty string, which is the LIVE case")
		}
		if seen[s] {
			t.Fatalf("AutonomySources() repeats %q", s)
		}
		seen[s] = true
		if !IsAutonomyReconstructed(s) {
			t.Fatalf("source %q does not read back as reconstructed", s)
		}
	}
}
