package main

import (
	"encoding/json"
	"net/http/httptest"
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

func decodeAutonomyDuration(t *testing.T, rec *httptest.ResponseRecorder) historyAutonomyDurationResponse {
	t.Helper()
	var resp historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode duration payload: %v", err)
	}
	return resp
}

func decodeAutonomySpans(t *testing.T, rec *httptest.ResponseRecorder) historyAutonomySpansResponse {
	t.Helper()
	var resp historyAutonomySpansResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode spans payload: %v", err)
	}
	return resp
}

// Both Autonomy payloads must carry the provenance block, and the SAME one:
// the section draws two elements over one data source, and a strip claiming
// "9 reconstructed" under a chart claiming something else is worse than
// neither claiming anything.
func TestAutonomyProvenance_IsEchoedByBothElements(t *testing.T) {
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

	duration := decodeAutonomyDuration(t, getAutonomy(t, store, "chart=autonomy_duration&window=30d"))
	spans := decodeAutonomySpans(t, getAutonomy(t, store, "chart=autonomy_spans&window=24h"))

	if !reflect.DeepEqual(duration.Provenance, spans.Provenance) {
		t.Fatalf("the two elements disagree about provenance: %+v vs %+v", duration.Provenance, spans.Provenance)
	}
	if duration.Provenance.Reconstructed != 9 || duration.Provenance.CostDerived != 4 {
		t.Fatalf("provenance = %+v, want the store's counts echoed unchanged", duration.Provenance)
	}
	if duration.Provenance.LiveSince != now-7200 {
		t.Fatalf("LiveSince = %d, want %d", duration.Provenance.LiveSince, now-7200)
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
	rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["provenance"]; !ok {
		t.Fatal("the payload omits `provenance` entirely — an absent block and a zero one must not look alike")
	}
	duration := decodeAutonomyDuration(t, rec)
	if duration.Provenance.Reconstructed != 0 || duration.Provenance.CostDerived != 0 {
		t.Fatalf("provenance = %+v, want zeroes on an all-live window", duration.Provenance)
	}
}

// A span reconstructed from the cost log reaches the strip carrying `unknown`,
// and `unknown` must survive the trip unchanged — neither normalized into a
// real end reason on the way out, nor blanked (which would make it
// indistinguishable from an old row whose reason was never recorded).
func TestAutonomySpans_UnknownReasonSurvivesTheWire(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeAutonomyStore{
		spans:      []outbound.AutonomySpan{spanEndingAt(now-3600, 300, "proj", session.AutonomyReasonUnknown)},
		total:      1,
		provenance: outbound.AutonomySpanProvenance{Reconstructed: 1, CostDerived: 1},
	}
	spans := decodeAutonomySpans(t, getAutonomy(t, store, "chart=autonomy_spans&window=24h"))

	if len(spans.Spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans.Spans))
	}
	if spans.Spans[0].Reason != session.AutonomyReasonUnknown {
		t.Fatalf("reason = %q, want %q", spans.Spans[0].Reason, session.AutonomyReasonUnknown)
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
	duration := decodeAutonomyDuration(t, getAutonomy(t, store, "chart=autonomy_duration&window=30d"))
	if len(duration.Provenance.Boundaries) != 2 {
		t.Fatalf("boundaries = %+v, want 2", duration.Provenance.Boundaries)
	}
	if duration.Provenance.Boundaries[1].To != autonomyEraLive {
		t.Fatalf("the measured era is named %q on the wire, want %q",
			duration.Provenance.Boundaries[1].To, autonomyEraLive)
	}
	if duration.Provenance.LiveSince != now-100000 {
		t.Fatalf("LiveSince = %d, want the measured era's start", duration.Provenance.LiveSince)
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
	if err := json.Unmarshal(getAutonomy(t, store, "chart=autonomy_spans&window=24h").Body.Bytes(), &raw); err != nil {
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

// `unknown` ranks below every MEASURED reason on the strip's collapse ladder,
// so one reconstructed span can never grey out a column that also holds a real
// error. It is deliberately not a session state, so this falls out of
// AutonomyReasonPriority's unrecognized-reason branch rather than needing a
// case of its own — and that is what is being pinned.
func TestAutonomyUnknown_RanksBelowEveryMeasuredReason(t *testing.T) {
	reasons := session.AutonomyEndReasons()
	if len(reasons) == 0 {
		t.Fatal("session.AutonomyEndReasons() is empty — cannot verify anything")
	}
	for _, r := range reasons {
		if session.AutonomyReasonPriority(session.AutonomyReasonUnknown) >= session.AutonomyReasonPriority(r) {
			t.Fatalf("`unknown` outranks or ties %q on the collapse ladder", r)
		}
	}
	if session.IsAutonomyEndReason(session.AutonomyReasonUnknown) {
		t.Fatal("`unknown` reads back as a real end reason")
	}
}
