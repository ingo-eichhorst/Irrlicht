package main

import (
	"encoding/json"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// The API half of the run-kind classification (#1905 subagents).
//
// Two things have to be true of every Autonomy payload: the store was asked for
// the mode the caller wanted, and the payload says which mode produced it. The
// second is what keeps "42 runs" from meaning two different things depending on
// a control the reader may have moved after the request went out.

// THE DEFAULT IS TOP-LEVEL RUNS. Absent means false, and so does anything that
// is not an affirmative — guessing "true" would drag the headline median down
// with short nested runs that were already counted inside their parents'.
func TestAutonomyIncludeSubagents_DefaultsToTopLevelRunsOnly(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"chart=autonomy_duration&window=30d", false},
		{"chart=autonomy_duration&window=30d&include_subagents=false", false},
		{"chart=autonomy_duration&window=30d&include_subagents=", false},
		{"chart=autonomy_duration&window=30d&include_subagents=maybe", false},
		{"chart=autonomy_duration&window=30d&include_subagents=true", true},
		{"chart=autonomy_duration&window=30d&include_subagents=1", true},
		{"chart=autonomy_spans&window=24h&include_subagents=true", true},
		{"chart=autonomy_spans&window=24h", false},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			store := &fakeAutonomyStore{}
			if rec := getAutonomy(t, store, tc.query); rec.Code != 200 {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if store.lastQuery.IncludeSubagents != tc.want {
				t.Fatalf("IncludeSubagents = %v, want %v — the store, not the client, is what filters",
					store.lastQuery.IncludeSubagents, tc.want)
			}
		})
	}
}

// The payload STATES the mode and the window's census, on both elements, so a
// client can say what it counted and what it left out without recomputing
// either from rows it does not have.
func TestAutonomyPayloadsCarryTheModeAndTheCensus(t *testing.T) {
	kinds := outbound.AutonomySpanKinds{TopLevel: 7, Subagent: 5, Unknown: 3}

	t.Run("duration, default mode", func(t *testing.T) {
		store := &fakeAutonomyStore{kinds: kinds}
		rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")
		var got historyAutonomyDurationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Kinds.Mode != autonomyModeTopLevel {
			t.Fatalf("mode = %q, want %q", got.Kinds.Mode, autonomyModeTopLevel)
		}
		if got.Kinds.Subagent != 5 || got.Kinds.Unknown != 3 || got.Kinds.TopLevel != 7 {
			t.Fatalf("kinds = %+v, want the store's census verbatim", got.Kinds)
		}
	})

	t.Run("spans, include mode", func(t *testing.T) {
		store := &fakeAutonomyStore{kinds: kinds}
		rec := getAutonomy(t, store, "chart=autonomy_spans&window=24h&include_subagents=true")
		var got historyAutonomySpansResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Kinds.Mode != autonomyModeAll {
			t.Fatalf("mode = %q, want %q", got.Kinds.Mode, autonomyModeAll)
		}
		// The census does NOT change with the mode: the same window holds the
		// same runs whichever ones were asked for.
		if got.Kinds.Subagent != 5 {
			t.Fatalf("subagent count = %d, want 5", got.Kinds.Subagent)
		}
	})
}

// Every span row on the wire carries a RESOLVED kind — never a blank the client
// would have to interpret. A row the store read out of a pre-classification log
// ships as the literal word "unknown", so no client has to decide for itself
// what an absent field means, and none can decide differently from the other.
func TestAutonomySpanRowsCarryAResolvedKind(t *testing.T) {
	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{
			{Start: 100, End: 200, Project: "p", Session: "legacy", Reason: session.StateReady},
			{Start: 300, End: 400, Project: "p", Session: "child", Reason: session.StateReady,
				Kind: session.AutonomyKindSubagent, Parent: "top-1"},
			{Start: 500, End: 600, Project: "p", Session: "top", Reason: session.StateReady,
				Kind: session.AutonomyKindTopLevel},
		},
	}
	rec := getAutonomy(t, store, "chart=autonomy_spans&window=24h&include_subagents=true")
	var got historyAutonomySpansResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(got.Spans))
	}
	want := []string{session.AutonomyKindUnknown, session.AutonomyKindSubagent, session.AutonomyKindTopLevel}
	for i, row := range got.Spans {
		if row.Kind != want[i] {
			t.Fatalf("span %d (%s) kind = %q, want %q — a blank must reach no client",
				i, row.Session, row.Kind, want[i])
		}
	}
	if got.Spans[1].Parent != "top-1" {
		t.Fatalf("subagent row parent = %q, want %q", got.Spans[1].Parent, "top-1")
	}
	// A raw check on the JSON: `kind` is not omitempty, so even the unknown row
	// carries the key. A client testing for presence must never see a hole.
	var raw struct {
		Spans []map[string]any `json:"spans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for i, row := range raw.Spans {
		if _, present := row["kind"]; !present {
			t.Fatalf("span %d has no `kind` key on the wire: %v", i, row)
		}
	}
}
