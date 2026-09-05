package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Autonomy back-fill provenance (#1905) — the wire half.
//
// tools/autonomy-backfill writes spans reconstructed from logs that were
// already on disk, MARKED as reconstructed. The daemon never runs it, but it
// serves what it wrote, and a reconstructed figure rendered as a measured one
// is exactly the "wrong number with nothing on screen saying so" this feature
// exists to avoid.

// The panels payload carries the provenance block verbatim: the section draws
// five panels over one data source, and a figure whose provenance is stated
// once for the whole stack cannot disagree with itself panel by panel.
func TestAutonomyProvenance_IsEchoedOnTheWire(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeAutonomyStore{
		spans:    []outbound.AutonomySpan{spanEndingAt(now-3600, 300, "proj", session.StateReady)},
		earliest: now - 86400,
		total:    12,
		provenance: outbound.AutonomySpanProvenance{
			Reconstructed: 9,
			CostDerived:   4,
			EraStarts:     map[string]int64{"": now - 7200},
		},
	}

	got := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	if got.Provenance.Reconstructed != 9 || got.Provenance.CostDerived != 4 {
		t.Fatalf("provenance = %+v, want the store's counts echoed unchanged", got.Provenance)
	}
	if got.Provenance.LiveSince != now-7200 {
		t.Fatalf("LiveSince = %d, want %d", got.Provenance.LiveSince, now-7200)
	}
}

// A window measured end to end reports zeroes, so both clients stay silent
// rather than printing a provenance note about nothing.
//
// The field is PRESENT and zero rather than omitted: an absent block and a
// zero one must not look alike to a client, or "nothing was reconstructed" and
// "this daemon predates the field" become the same answer.
func TestAutonomyProvenance_AllLiveWindowReportsZeroAndSaysSo(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeAutonomyStore{
		spans:    []outbound.AutonomySpan{spanEndingAt(now-3600, 300, "proj", session.StateReady)},
		earliest: now - 86400,
		total:    1,
	}
	rec := getAutonomy(t, store, "chart=autonomy_projects&window=30d")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["provenance"]; !ok {
		t.Fatal("the payload omits `provenance` entirely — an absent block and a zero one must not look alike")
	}
	got := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	if got.Provenance.Reconstructed != 0 || got.Provenance.CostDerived != 0 {
		t.Fatalf("provenance = %+v, want zeroes on an all-live window", got.Provenance)
	}
}

// `reason` STAYS A RECORDED FIELD even though nothing renders it any more
// (#1905 redesign). It is the only fact about a run that cannot be recovered
// after the event, so the section keeps recording it — and `unknown` in
// particular must survive unchanged, neither normalized into a real end reason
// nor blanked, which would make a cost-derived span indistinguishable from an
// old row whose reason was never recorded at all.
func TestAutonomyReason_IsStillCarriedByEveryStoredSpan(t *testing.T) {
	now := time.Now().Unix()
	spans := []outbound.AutonomySpan{
		spanEndingAt(now-3600, 300, "proj", session.AutonomyReasonUnknown),
		spanEndingAt(now-1800, 300, "proj", session.StateWaiting),
	}
	store := &fakeAutonomyStore{
		spans:      spans,
		total:      2,
		provenance: outbound.AutonomySpanProvenance{Reconstructed: 1, CostDerived: 1},
	}
	// The panels payload deliberately carries no per-run rows, so what is pinned
	// here is that the field reaches the handler intact and is neither rewritten
	// nor required by it.
	got := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	if panelFor(t, got, "proj").Runs != 2 {
		t.Fatalf("a span carrying %q was dropped — the section counts every run whatever ended it",
			session.AutonomyReasonUnknown)
	}
	if store.spans[0].Reason != session.AutonomyReasonUnknown {
		t.Fatalf("reason = %q, want %q — the handler must not rewrite what the store holds",
			store.spans[0].Reason, session.AutonomyReasonUnknown)
	}
	if session.IsAutonomyEndReason(session.AutonomyReasonUnknown) {
		t.Fatal("`unknown` reads back as a real end reason")
	}
}

// --- Source boundaries (QA-2) -----------------------------------------------

// The boundaries are derived from the per-source era starts by ONE mechanism —
// sort the eras, emit a handover between each adjacent pair — rather than by a
// case per pair. Today a back-filled machine has two (cost→log, log→live), but
// nothing here names either.
func TestAutonomyBoundaries_OneMechanismForEveryHandover(t *testing.T) {
	cases := []struct {
		name  string
		eras  map[string]int64
		want  []historyAutonomyBoundary
		about string
	}{
		{
			name:  "a machine that was never back-filled has no boundary",
			eras:  map[string]int64{"": 5000},
			want:  nil,
			about: "one era cannot hand over to anything",
		},
		{
			name: "a back-filled machine still collecting",
			eras: map[string]int64{
				session.AutonomySourceCost: 1000,
				session.AutonomySourceLog:  2000,
				"":                         3000,
			},
			want: []historyAutonomyBoundary{
				{TS: 2000, From: session.AutonomySourceCost, To: session.AutonomySourceLog},
				{TS: 3000, From: session.AutonomySourceLog, To: autonomyEraLive},
			},
		},
		{
			name: "back-filled but the daemon has measured nothing yet",
			eras: map[string]int64{
				session.AutonomySourceCost: 1000,
				session.AutonomySourceLog:  2000,
			},
			want: []historyAutonomyBoundary{
				{TS: 2000, From: session.AutonomySourceCost, To: session.AutonomySourceLog},
			},
		},
		{
			// The mechanism has to carry a source it has never heard of, or
			// "one mechanism" is a claim rather than a property.
			name: "a source this build does not know still gets its handover",
			eras: map[string]int64{
				"some-future-source": 1000,
				"":                   2000,
			},
			want: []historyAutonomyBoundary{
				{TS: 2000, From: "some-future-source", To: autonomyEraLive},
			},
		},
		{
			name:  "an era with no start on record is not an era",
			eras:  map[string]int64{session.AutonomySourceCost: 0, "": 3000},
			want:  nil,
			about: "a zero start means no span of that provenance exists",
		},
		{name: "an empty log has no boundary", eras: map[string]int64{}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := autonomyBoundariesFrom(tc.eras)
			if len(got) != len(tc.want) {
				t.Fatalf("boundaries = %+v, want %+v (%s)", got, tc.want, tc.about)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("boundary %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Map iteration order must not reach the wire: two requests against one log
// have to describe the same history.
func TestAutonomyBoundaries_AreDeterministic(t *testing.T) {
	eras := map[string]int64{
		session.AutonomySourceCost: 1000,
		session.AutonomySourceLog:  2000,
		"":                         3000,
		"another":                  2000, // ties with `log`, broken by name
	}
	first := autonomyBoundariesFrom(eras)
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(autonomyBoundariesFrom(eras), first) {
			t.Fatalf("autonomyBoundariesFrom is order-dependent: %+v vs %+v", autonomyBoundariesFrom(eras), first)
		}
	}
	// …and they come out oldest first, which is what lets a client draw them
	// without re-sorting.
	for i := 1; i < len(first); i++ {
		if first[i].TS < first[i-1].TS {
			t.Fatalf("boundaries are not ordered oldest first: %+v", first)
		}
	}
}

// The boundaries reach both payloads, and `live` is the wire's name for a
// measured row's absent source — a row never carries it.
func TestAutonomyBoundaries_ReachTheWire(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{spanEndingAt(now-3600, 300, "proj", session.StateReady)},
		total: 3,
		provenance: outbound.AutonomySpanProvenance{
			Reconstructed: 2,
			CostDerived:   1,
			EraStarts: map[string]int64{
				session.AutonomySourceCost: now - 300000,
				session.AutonomySourceLog:  now - 200000,
				"":                         now - 100000,
			},
		},
	}
	got := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	if len(got.Provenance.Boundaries) != 2 {
		t.Fatalf("boundaries = %+v, want 2", got.Provenance.Boundaries)
	}
	if got.Provenance.Boundaries[1].To != autonomyEraLive {
		t.Fatalf("the measured era is named %q on the wire, want %q",
			got.Provenance.Boundaries[1].To, autonomyEraLive)
	}
	if got.Provenance.LiveSince != now-100000 {
		t.Fatalf("LiveSince = %d, want the measured era's start", got.Provenance.LiveSince)
	}
	// `live` must never be a row's source, or a measured row would read back
	// as reconstructed.
	for _, s := range session.AutonomySources() {
		if s == autonomyEraLive {
			t.Fatalf("%q is a writable row source; it is only ever a wire label", autonomyEraLive)
		}
	}
	if !session.IsAutonomyReconstructed(autonomyEraLive) {
		t.Fatalf("guard assumption broken: %q would read as measured if written to a row", autonomyEraLive)
	}
}

// A single-era log emits an empty list rather than omitting the field: a client
// must be able to tell "no handovers" from "this daemon does not know about
// them".
func TestAutonomyBoundaries_EmptyListIsPresentOnTheWire(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeAutonomyStore{
		spans:      []outbound.AutonomySpan{spanEndingAt(now-3600, 300, "proj", session.StateReady)},
		total:      1,
		provenance: outbound.AutonomySpanProvenance{EraStarts: map[string]int64{"": now - 100000}},
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(getAutonomy(t, store, "chart=autonomy_projects&window=30d").Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var prov map[string]json.RawMessage
	if err := json.Unmarshal(raw["provenance"], &prov); err != nil {
		t.Fatalf("decode provenance: %v", err)
	}
	if _, ok := prov["boundaries"]; !ok {
		t.Fatal("the payload omits `boundaries` entirely")
	}
}
