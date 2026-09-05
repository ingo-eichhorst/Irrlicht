package session

import (
	"slices"
	"testing"
)

// THE TRAP THIS WHOLE VOCABULARY EXISTS FOR (#1905 subagents).
//
// There are rows on disk written before a run carried a kind at all. If a blank
// resolved to "top-level", the default view — which counts top-level runs —
// would claim to exclude subagent runs while silently including every
// historical one, and nothing on screen would say so.
//
// So: absent is UNKNOWN. So is a value this build has never heard of, for the
// same reason — an unfamiliar classification is not a claim this build gets to
// make on the writer's behalf.
func TestAutonomyKindOrUnknown_AbsenceIsNeverTopLevel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a row written before the field existed", "", AutonomyKindUnknown},
		{"a kind a newer build wrote", "background-agent", AutonomyKindUnknown},
		{"explicitly unknown", AutonomyKindUnknown, AutonomyKindUnknown},
		{"a top-level run", AutonomyKindTopLevel, AutonomyKindTopLevel},
		{"a subagent run", AutonomyKindSubagent, AutonomyKindSubagent},
		// Not a normalizer: `Top` is not `top`. A near-miss resolving to
		// top-level would be the same silent claim by a different route.
		{"a near-miss spelling", "Top", AutonomyKindUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AutonomyKindOrUnknown(tc.in); got != tc.want {
				t.Fatalf("AutonomyKindOrUnknown(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Only a run established to be a subagent's is excludable. An unknown row is
// NOT: nothing established it, and dropping it would delete history to make a
// figure look tidy.
func TestIsAutonomySubagentRun_OnlyAnEstablishedSubagent(t *testing.T) {
	if !IsAutonomySubagentRun(AutonomyKindSubagent) {
		t.Fatalf("IsAutonomySubagentRun(%q) = false, want true", AutonomyKindSubagent)
	}
	for _, kind := range []string{"", AutonomyKindTopLevel, AutonomyKindUnknown, "background-agent"} {
		if IsAutonomySubagentRun(kind) {
			t.Fatalf("IsAutonomySubagentRun(%q) = true — an unestablished kind must never be excluded", kind)
		}
	}
}

// The LIVE producer is looking at the session state itself, where an empty
// ParentSessionID means "no parent", not "nobody looked". So it never yields
// unknown: the unknown bucket belongs to rows nothing classified.
func TestAutonomyKindForParent_NeverUnknown(t *testing.T) {
	if got := AutonomyKindForParent(""); got != AutonomyKindTopLevel {
		t.Fatalf("AutonomyKindForParent(\"\") = %q, want %q", got, AutonomyKindTopLevel)
	}
	if got := AutonomyKindForParent("parent-1"); got != AutonomyKindSubagent {
		t.Fatalf("AutonomyKindForParent(\"parent-1\") = %q, want %q", got, AutonomyKindSubagent)
	}
}

// The kind vocabulary is NOT the session-state vocabulary, and `unknown` is a
// MEMBER of it — unlike AutonomyReasonUnknown, which is deliberately outside
// its own. A build that dropped `unknown` from the list would leave every
// consumer without a name for the third bucket and invite folding it into one
// of the other two.
func TestAutonomyKinds_HoldsAllThreeAndNoSessionState(t *testing.T) {
	kinds := AutonomyKinds()
	for _, want := range []string{AutonomyKindTopLevel, AutonomyKindSubagent, AutonomyKindUnknown} {
		if !slices.Contains(kinds, want) {
			t.Fatalf("AutonomyKinds() = %v, missing %q", kinds, want)
		}
	}
	for _, state := range CanonicalStates() {
		if slices.Contains(kinds, state) {
			t.Fatalf("AutonomyKinds() contains the session state %q — the two vocabularies are orthogonal", state)
		}
	}
}
