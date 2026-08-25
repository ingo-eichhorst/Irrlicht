package session

import (
	"encoding/json"
	"testing"
)

// TestComputeSubagentSummary_BucketsSumToTotal is a CHECK THIS CHANGE ADDS, so
// it has no "before the fix" to run red against — its evidence is the mutation
// battery below its own claim: delete the Error bucket from subagentSummary, or
// drop the `summary.Error = byState[StateError]` readout in grouped.go, and it
// goes red on the error row.
//
// What it pins is an invariant, not a field list: Total counts every file
// child, so the per-state buckets must account for every one of them. Pre-#1801
// they did not — ComputeSubagentSummary switched over three states while Total
// counted four, so an errored child inflated the total and landed nowhere, and
// the parent's subagent badge said "3 children, 2 accounted for" with no way to
// tell which one was red.
//
// It iterates CanonicalStates rather than naming states, so a fifth state fails
// here the day it is added rather than the day someone notices the arithmetic.
func TestComputeSubagentSummary_BucketsSumToTotal(t *testing.T) {
	parent := &SessionState{SessionID: "p"}
	var children []*SessionState
	want := map[string]int{}
	for i, st := range CanonicalStates() {
		// A different count per state, so a readout wired to the wrong bucket
		// (Error reading StateReady's tally, say) cannot coincidentally pass.
		n := i + 1
		want[st] = n
		for j := 0; j < n; j++ {
			children = append(children, &SessionState{
				SessionID:       st,
				ParentSessionID: "p",
				State:           st,
			})
		}
	}

	got := ComputeSubagentSummary(parent, children)
	if got == nil {
		t.Fatal("ComputeSubagentSummary returned nil for a parent with children")
	}

	buckets := map[string]int{
		StateWorking: got.Working,
		StateWaiting: got.Waiting,
		StateReady:   got.Ready,
		StateError:   got.Error,
	}
	// Assert the readout map itself covers the vocabulary. Without this the
	// loop below would silently check fewer states if one were dropped from
	// both production and this map — inability to look reading as a clean pass
	// (AGENTS.md).
	for _, st := range CanonicalStates() {
		if _, ok := buckets[st]; !ok {
			t.Fatalf("subagentSummary has no bucket for canonical state %q — every state a child can be in needs one, or Total stops summing", st)
		}
	}

	sum := 0
	for _, st := range CanonicalStates() {
		if buckets[st] != want[st] {
			t.Errorf("bucket %q: got %d, want %d", st, buckets[st], want[st])
		}
		sum += buckets[st]
	}
	if sum != got.Total {
		t.Errorf("buckets sum to %d but Total is %d — every child must land in exactly one bucket", sum, got.Total)
	}
}

// TestSubagentSummary_ErrorBucketIsAlwaysOnTheWire pins that the new bucket
// serializes unconditionally, like the three beside it. With `omitempty` a
// healthy parent and a parent from a daemon that predates the field would ship
// identical JSON, so a client could never tell "no errored children" from "this
// build cannot tell me".
func TestSubagentSummary_ErrorBucketIsAlwaysOnTheWire(t *testing.T) {
	b, err := json.Marshal(&subagentSummary{Total: 1, Working: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["error"]; !ok {
		t.Errorf("subagents payload has no \"error\" key at zero: %s", b)
	}
}

// TestSubagentSummary_EqualSeesTheErrorBucket is the guard on the re-broadcast
// suppression at session_detector_activity.go: Equal compares the struct by
// value, so a new field is picked up for free — but "for free" is a property of
// the current implementation, not a promise. If Equal is ever rewritten to
// compare fields by hand and the error bucket is missed, a parent whose child
// just went red would stop being re-broadcast and the badge would freeze.
func TestSubagentSummary_EqualSeesTheErrorBucket(t *testing.T) {
	a := &subagentSummary{Total: 1, Error: 0}
	b := &subagentSummary{Total: 1, Error: 1}
	if a.Equal(b) {
		t.Error("summaries differing only in the error bucket compared equal — a child going red would not re-broadcast")
	}
}
