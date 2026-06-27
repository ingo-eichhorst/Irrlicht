package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
)

// seedCostRows appends raw snapshot rows to <dir>/<project>.jsonl in the
// on-disk JSONL shape the cost tracker reads.
func seedCostRows(t *testing.T, dir, project string, rows []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, project+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	for _, r := range rows {
		b, _ := json.Marshal(r)
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func doHistory(t *testing.T, tracker *filesystem.CostTracker, query string) (*httptest.ResponseRecorder, historyResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?"+query, nil)
	rec := httptest.NewRecorder()
	handleGetHistory(tracker)(rec, req)
	var resp historyResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode body %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

func TestHandleGetHistory_CostByProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cost")
	now := time.Now().Unix()
	// proj-a: both rows in-window → first seeds baseline, second is a 0.50 delta.
	seedCostRows(t, dir, "proj-a", []map[string]any{
		{"ts": now - 12*3600, "project": "proj-a", "session": "s1", "cost": 1.00},
		{"ts": now - 1*3600, "project": "proj-a", "session": "s1", "cost": 1.50},
	})
	// proj-b: 0.20 delta.
	seedCostRows(t, dir, "proj-b", []map[string]any{
		{"ts": now - 2*3600, "project": "proj-b", "session": "s2", "cost": 0.20},
		{"ts": now - 1*3600, "project": "proj-b", "session": "s2", "cost": 0.40},
	})
	tr := filesystem.NewCostTrackerWithDir(dir)

	rec, resp := doHistory(t, tr, "range=day&chart=cost")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if resp.Range != "day" || resp.Chart != "cost" || resp.Group != "project" {
		t.Errorf("envelope: %+v", resp)
	}
	if resp.BucketSeconds != 60 {
		t.Errorf("day should bucket at 60s, got %d", resp.BucketSeconds)
	}
	if d := resp.Total - 0.70; d > 1e-9 || d < -1e-9 {
		t.Errorf("total: want 0.70, got %v", resp.Total)
	}
	if len(resp.TopContributors) != 2 || resp.TopContributors[0].Label != "proj-a" {
		t.Errorf("top contributors: %+v", resp.TopContributors)
	}
	if len(resp.Series) != 2 {
		t.Errorf("want 2 sparse series points, got %d: %+v", len(resp.Series), resp.Series)
	}
	if resp.Forecast == nil || resp.Forecast.Basis != "linear" {
		t.Errorf("forecast: %+v", resp.Forecast)
	}
	if resp.Forecast != nil && resp.Forecast.Projected < resp.Total {
		t.Errorf("projected %v must be >= total %v", resp.Forecast.Projected, resp.Total)
	}
}

func TestHandleGetHistory_CustomRange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cost")
	seedCostRows(t, dir, "proj-a", []map[string]any{
		{"ts": 1000, "project": "proj-a", "session": "s1", "cost": 1.00},
		{"ts": 1500, "project": "proj-a", "session": "s1", "cost": 1.25},
	})
	tr := filesystem.NewCostTrackerWithDir(dir)

	rec, resp := doHistory(t, tr, "start=900&end=2000&bucket=100&forecast=false")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if resp.Range != "custom" || resp.Start != 900 || resp.End != 2000 || resp.BucketSeconds != 100 {
		t.Errorf("custom envelope: %+v", resp)
	}
	if d := resp.Total - 0.25; d > 1e-9 || d < -1e-9 {
		t.Errorf("total: want 0.25, got %v", resp.Total)
	}
	if resp.Forecast != nil {
		t.Errorf("forecast=false should omit forecast, got %+v", resp.Forecast)
	}
}

func TestHandleGetHistory_PhaseStubs(t *testing.T) {
	tr := filesystem.NewCostTrackerWithDir(filepath.Join(t.TempDir(), "cost"))
	cases := []struct {
		query string
		phase float64
	}{
		{"chart=tokens", 2},
		{"chart=models", 2},
		{"chart=providers", 2},
		{"chart=agents", 3},
		{"group=branch", 2},
		{"group=session", 2},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/history?range=day&"+c.query, nil)
		rec := httptest.NewRecorder()
		handleGetHistory(tr)(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: want 501, got %d", c.query, rec.Code)
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: decode: %v", c.query, err)
			continue
		}
		if body["phase"] != c.phase {
			t.Errorf("%s: want phase %v, got %v", c.query, c.phase, body["phase"])
		}
	}
}

func TestHandleGetHistory_BadRequests(t *testing.T) {
	tr := filesystem.NewCostTrackerWithDir(filepath.Join(t.TempDir(), "cost"))
	for _, q := range []string{"chart=bogus", "group=bogus", "range=bogus", "start=abc&end=def", "start=2000&end=1000"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/history?"+q, nil)
		rec := httptest.NewRecorder()
		handleGetHistory(tr)(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: want 400, got %d", q, rec.Code)
		}
	}
}

func TestHandleGetHistory_NilTrackerEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?range=week", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil tracker: want 200, got %d", rec.Code)
	}
	var resp historyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 || len(resp.Series) != 0 {
		t.Errorf("want empty payload, got %+v", resp)
	}
	// Empty payload must still serialize series as [] (not null) so the chart
	// renders cleanly on a fresh install.
	if resp.Series == nil {
		t.Error("series should be a non-nil empty slice")
	}
}
