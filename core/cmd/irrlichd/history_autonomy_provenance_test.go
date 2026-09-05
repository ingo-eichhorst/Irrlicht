package main

import (
	"encoding/json"
	"net/http/httptest"
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
			LiveSince:     now - 7200,
		},
	}

	duration := decodeAutonomyDuration(t, getAutonomy(t, store, "chart=autonomy_duration&window=30d"))
	spans := decodeAutonomySpans(t, getAutonomy(t, store, "chart=autonomy_spans&window=24h"))

	if duration.Provenance != spans.Provenance {
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
