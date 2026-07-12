package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// TestHandleGetHistory_StateChart: chart=state (#981) returns a per-project,
// per-state grid reconstructed from recordings — working/waiting/ready kept
// separate, unlike chart=agents' merged "active" count.
func TestHandleGetHistory_StateChart(t *testing.T) {
	recDir := filepath.Join(t.TempDir(), "recordings")
	now := time.Now().Unix()
	at := func(sec int64) time.Time { return time.Unix(now-sec, 0) }
	seedRecording(t, recDir, "run.jsonl", []lifecycle.Event{
		{Seq: 1, Timestamp: at(3600), Kind: lifecycle.KindTranscriptNew, SessionID: "s1", CWD: "/home/me/projX"},
		{Seq: 2, Timestamp: at(3500), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: session.StateWorking},
		{Seq: 3, Timestamp: at(3400), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: session.StateWaiting},
		{Seq: 4, Timestamp: at(1800), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: session.StateReady},
	})
	conc := filesystem.NewConcurrencyTrackerWithDir(recDir)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=state&granularity=24h", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, nil, conc, nil)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chart=state: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp historyStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Chart != "state" || resp.Group != "project" || resp.Range != "24h" {
		t.Errorf("envelope: %+v", resp)
	}
	if resp.BucketSeconds != 86400 {
		t.Errorf("24h granularity should bucket at 86400s, got %d", resp.BucketSeconds)
	}
	if len(resp.Projects) != 1 || resp.Projects[0] != "projX" {
		t.Errorf("projects: want [projX], got %+v", resp.Projects)
	}
	if resp.ByState == nil || resp.ByState[session.StateWorking] == nil || resp.ByState[session.StateWaiting] == nil || resp.ByState[session.StateReady] == nil {
		t.Fatalf("by_state must carry all three canonical states, got %+v", resp.ByState)
	}
	if v := sum(resp.ByState[session.StateWorking]["projX"]); v <= 0 {
		t.Errorf("working[projX] should have positive activity, got series %v", resp.ByState[session.StateWorking]["projX"])
	}
	if v := sum(resp.ByState[session.StateWaiting]["projX"]); v <= 0 {
		t.Errorf("waiting[projX] should have positive activity, got series %v", resp.ByState[session.StateWaiting]["projX"])
	}
	if v := sum(resp.ByState[session.StateReady]["projX"]); v != 1 {
		t.Errorf("ready[projX] should count exactly one transition, got %v (series %v)", v, resp.ByState[session.StateReady]["projX"])
	}
	if resp.Concurrency == nil || resp.Concurrency.Peak != 1 {
		t.Errorf("concurrency summary: want peak 1, got %+v", resp.Concurrency)
	}
}

// TestHandleGetHistory_StateChartEmpty: no recordings (the common case —
// --record is opt-in) returns a clean empty payload, not an error.
func TestHandleGetHistory_StateChartEmpty(t *testing.T) {
	conc := filesystem.NewConcurrencyTrackerWithDir(filepath.Join(t.TempDir(), "recordings"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=state", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, nil, conc, nil)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty state chart: want 200, got %d", rec.Code)
	}
	var resp historyStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Range != "24h" {
		t.Errorf("default granularity: want 24h, got %q", resp.Range)
	}
	if len(resp.Projects) != 0 {
		t.Errorf("want no projects, got %+v", resp.Projects)
	}
	if resp.Concurrency == nil || resp.Concurrency.Peak != 0 {
		t.Errorf("want zero concurrency summary, got %+v", resp.Concurrency)
	}
}

// TestHandleGetHistory_StateChartBadGranularity: an unrecognized ?granularity=
// is a client error, matching every other malformed history query param.
func TestHandleGetHistory_StateChartBadGranularity(t *testing.T) {
	conc := filesystem.NewConcurrencyTrackerWithDir(filepath.Join(t.TempDir(), "recordings"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=state&granularity=fortnight", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, nil, conc, nil)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad granularity: want 400, got %d", rec.Code)
	}
}

// TestHandleGetHistory_StateChartGranularitySteps: every one of the nine named
// granularities resolves to its own fixed bucket width.
func TestHandleGetHistory_StateChartGranularitySteps(t *testing.T) {
	conc := filesystem.NewConcurrencyTrackerWithDir(filepath.Join(t.TempDir(), "recordings"))
	want := map[string]int64{
		"1m": 60, "10m": 600, "60m": 3600, "8h": 8 * 3600, "24h": 86400,
		"7d": 7 * 86400, "1mo": 30 * 86400, "6mo": 182 * 86400, "1y": 365 * 86400,
	}
	for granularity, bucketSeconds := range want {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=state&granularity="+granularity, nil)
		rec := httptest.NewRecorder()
		handleGetHistory(nil, nil, conc, nil)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("granularity=%s: want 200, got %d", granularity, rec.Code)
		}
		var resp historyStateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("granularity=%s: decode: %v", granularity, err)
		}
		if resp.BucketSeconds != bucketSeconds {
			t.Errorf("granularity=%s: want bucket_seconds=%d, got %d", granularity, bucketSeconds, resp.BucketSeconds)
		}
	}
}

func sum(vs []float64) float64 {
	var s float64
	for _, v := range vs {
		s += v
	}
	return s
}
