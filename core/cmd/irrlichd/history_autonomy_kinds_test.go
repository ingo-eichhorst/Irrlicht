package main

import (
	"encoding/json"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// The API half of the run-kind classification (#1905 subagents), retargeted at
// the FIELD after the filter was removed (#1905 recording).
//
// The classification is still real data and every row still carries it. What no
// longer exists is a mode: subagent runs are always counted, so a payload has
// nothing to declare about which runs it chose, and there is no request the
// caller can make that changes the answer.

// The section counts every run, whatever the request says. An old client still
// sending ?include_subagents= gets the same payload as one that does not —
// answered, never rejected, because a parameter that no longer exists is not a
// bad request.
func TestAutonomy_CountsEveryRunWhateverTheQueryAsksFor(t *testing.T) {
	queries := []string{
		"chart=autonomy_duration&window=30d",
		"chart=autonomy_duration&window=30d&include_subagents=false",
		"chart=autonomy_duration&window=30d&include_subagents=true",
		"chart=autonomy_spans&window=24h",
		"chart=autonomy_spans&window=24h&include_subagents=false",
		"chart=autonomy_spans&window=24h&include_subagents=true",
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			store := &fakeAutonomyStore{
				spans: []outbound.AutonomySpan{
					{Start: 100, End: 200, Project: "p", Session: "child",
						Kind: session.AutonomyKindSubagent, Parent: "top-1"},
				},
				kinds: outbound.AutonomySpanKinds{Subagent: 1},
			}
			rec := getAutonomy(t, store, q)
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			// The mutation this pins: reintroducing any per-request subagent
			// filter would have to reach the store as a query field, and the
			// query the handler builds carries none.
			if store.lastQuery != (outbound.AutonomySpanQuery{
				Start: store.lastQuery.Start,
				End:   store.lastQuery.End,
				Limit: store.lastQuery.Limit,
			}) {
				t.Fatalf("query = %+v — it carries something beyond the window and the limit, "+
					"which is how a per-request filter would come back", store.lastQuery)
			}
		})
	}
}

// The payload STATES the window's census on both elements, so a client can say
// how much of a window was subagent work without recomputing it from rows it
// does not have (the duration chart carries no rows at all).
func TestAutonomyPayloadsCarryTheCensus(t *testing.T) {
	kinds := outbound.AutonomySpanKinds{TopLevel: 7, Subagent: 5, Unknown: 3}

	t.Run("duration", func(t *testing.T) {
		store := &fakeAutonomyStore{kinds: kinds}
		rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")
		var got historyAutonomyDurationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Kinds.Subagent != 5 || got.Kinds.Unknown != 3 || got.Kinds.TopLevel != 7 {
			t.Fatalf("kinds = %+v, want the store's census verbatim", got.Kinds)
		}
	})

	t.Run("spans", func(t *testing.T) {
		store := &fakeAutonomyStore{kinds: kinds}
		rec := getAutonomy(t, store, "chart=autonomy_spans&window=24h")
		var got historyAutonomySpansResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
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
	rec := getAutonomy(t, store, "chart=autonomy_spans&window=24h")
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
