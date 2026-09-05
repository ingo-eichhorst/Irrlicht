package main

import (
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// The API half of the run-kind classification (#1905 subagents), retargeted
// twice: at the FIELD after the filter was removed (#1905 recording), and now at
// the CONCURRENCY SPLIT, which is the first thing that actually renders it.
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
		"chart=autonomy_projects&window=30d",
		"chart=autonomy_projects&window=30d&include_subagents=false",
		"chart=autonomy_projects&window=30d&include_subagents=true",
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
			}) {
				t.Fatalf("query = %+v — it carries something beyond the window, which is how a "+
					"per-request filter would come back", store.lastQuery)
			}
		})
	}
}

// The payload STATES the window's census, so a client can say how much of a
// window was subagent work without recomputing it from rows it does not have —
// and the panels carry no rows at all.
func TestAutonomyPayloadCarriesTheCensus(t *testing.T) {
	kinds := outbound.AutonomySpanKinds{TopLevel: 7, Subagent: 5, Unknown: 3}
	got := decodeAutonomy(t, &fakeAutonomyStore{kinds: kinds}, "chart=autonomy_projects&window=30d")
	if got.Kinds.Subagent != 5 || got.Kinds.Unknown != 3 || got.Kinds.TopLevel != 7 {
		t.Fatalf("kinds = %+v, want the store's census verbatim", got.Kinds)
	}
}

// A blank `kind` on a row read out of a pre-classification log RESOLVES to
// unknown, never to top-level — the one thing it must not silently mean. The
// resolution used to be visible as a per-row field on the strip; the strip is
// gone, so the place it now shows is the concurrency split, which must refuse to
// split a peak that a blank row was alive for.
func TestAutonomy_BlankKindResolvesToUnknownNotTopLevel(t *testing.T) {
	base := int64(1_700_000_000)
	spans := []outbound.AutonomySpan{
		{Start: base, End: base + 1000, Project: "p", Session: "legacy"}, // no Kind at all
		{Start: base + 100, End: base + 900, Project: "p", Session: "top",
			Kind: session.AutonomyKindTopLevel},
	}
	got := autonomyWindowPeak(spans, base, base+86400)
	if got.peak != 2 {
		t.Fatalf("peak = %d, want 2", got.peak)
	}
	if got.splitKnown {
		t.Fatalf("a blank kind was treated as classifiable (%d + %d sub) — reading absence as "+
			"top-level is the one failure session.AutonomyKindOrUnknown exists to prevent",
			got.topLevel, got.subagent)
	}
	// And the resolution itself, stated where a reader can check it.
	if session.AutonomyKindOrUnknown("") != session.AutonomyKindUnknown {
		t.Fatalf("a blank kind resolves to %q, want %q",
			session.AutonomyKindOrUnknown(""), session.AutonomyKindUnknown)
	}
}
