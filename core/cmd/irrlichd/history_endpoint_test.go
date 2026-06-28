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
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
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
	handleGetHistory(tracker, nil, nil)(rec, req)
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

// seedRecording writes lifecycle events to <dir>/<name> in the recorder's JSONL
// shape, so the concurrency reader reconstructs concurrency from them.
func seedRecording(t *testing.T, dir, name string, events []lifecycle.Event) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

// TestHandleGetHistory_AgentsConcurrency: chart=agents now reconstructs a
// concurrent-agents series from recordings (no more 501), with the peak/average/
// current summary and top concurrent projects in the side panel.
func TestHandleGetHistory_AgentsConcurrency(t *testing.T) {
	recDir := filepath.Join(t.TempDir(), "recordings")
	now := time.Now().Unix()
	at := func(sec int64) time.Time { return time.Unix(now-sec, 0) }
	seedRecording(t, recDir, "run.jsonl", []lifecycle.Event{
		{Seq: 1, Timestamp: at(3600), Kind: lifecycle.KindTranscriptNew, SessionID: "s1", CWD: "/home/me/projX"},
		{Seq: 2, Timestamp: at(3500), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: session.StateWorking},
		{Seq: 3, Timestamp: at(1800), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: session.StateReady},
	})
	cost := filesystem.NewCostTrackerWithDir(filepath.Join(t.TempDir(), "cost"))
	conc := filesystem.NewConcurrencyTrackerWithDir(recDir)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?range=day&chart=agents", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(cost, nil, conc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chart=agents: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp historyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Chart != "agents" || resp.Group != "project" {
		t.Errorf("envelope: chart=%q group=%q", resp.Chart, resp.Group)
	}
	if resp.Concurrency == nil || resp.Concurrency.Peak != 1 {
		t.Errorf("concurrency summary: want peak 1, got %+v", resp.Concurrency)
	}
	if len(resp.TopContributors) != 1 || resp.TopContributors[0].Label != "projX" {
		t.Errorf("top concurrent projects: want [projX], got %+v", resp.TopContributors)
	}
	if resp.Forecast != nil || resp.TokenSplit != nil {
		t.Errorf("agents chart should carry neither forecast nor token_split, got %+v / %+v", resp.Forecast, resp.TokenSplit)
	}
	if len(resp.Series) == 0 {
		t.Error("want a non-empty concurrency series")
	}
}

// TestHandleGetHistory_AgentsEmpty: no recordings (the common case — --record is
// opt-in) returns a clean empty payload, not an error.
func TestHandleGetHistory_AgentsEmpty(t *testing.T) {
	conc := filesystem.NewConcurrencyTrackerWithDir(filepath.Join(t.TempDir(), "recordings"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?range=day&chart=agents", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, nil, conc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty agents: want 200, got %d", rec.Code)
	}
	var resp historyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Concurrency == nil || resp.Concurrency.Peak != 0 {
		t.Errorf("want zero concurrency summary, got %+v", resp.Concurrency)
	}
	if len(resp.Series) != 0 || resp.Series == nil {
		t.Errorf("want non-nil empty series, got %+v", resp.Series)
	}
}

// seedPhase2 lays down one project with two sessions differing on every group
// axis (branch/provider/model), with cumulative tokens, over [1000,1400).
func seedPhase2(t *testing.T, dir string) {
	t.Helper()
	seedCostRows(t, dir, "proj-a", []map[string]any{
		{"ts": 1050, "project": "proj-a", "branch": "main", "model": "opus", "provider": "anthropic", "session": "s1", "cost": 1.00, "cum_in": 100, "cum_out": 10},
		{"ts": 1150, "project": "proj-a", "branch": "main", "model": "opus", "provider": "anthropic", "session": "s1", "cost": 1.40, "cum_in": 300, "cum_out": 30, "cum_read": 20},
		{"ts": 1050, "project": "proj-a", "branch": "feat", "model": "sonnet", "provider": "openai", "session": "s2", "cost": 2.00, "cum_in": 50, "cum_out": 5},
		{"ts": 1150, "project": "proj-a", "branch": "feat", "model": "sonnet", "provider": "openai", "session": "s2", "cost": 2.50, "cum_in": 150, "cum_out": 25},
	})
}

// TestHandleGetHistory_Phase2Combos: the previously-501 chart/group combos now
// return real data, and chart=models|providers pin the effective group.
func TestHandleGetHistory_Phase2Combos(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cost")
	seedPhase2(t, dir)
	tr := filesystem.NewCostTrackerWithDir(dir)
	const base = "start=1000&end=1400&bucket=100&forecast=false"

	for _, group := range []string{"branch", "provider", "model", "session"} {
		rec, resp := doHistory(t, tr, base+"&group="+group)
		if rec.Code != http.StatusOK {
			t.Fatalf("group=%s: want 200, got %d (%s)", group, rec.Code, rec.Body.String())
		}
		if resp.Group != group {
			t.Errorf("group=%s: echoed %q", group, resp.Group)
		}
		if len(resp.TopContributors) != 2 {
			t.Errorf("group=%s: want 2 contributors, got %+v", group, resp.TopContributors)
		}
	}

	for _, tc := range []struct{ chart, group string }{{"models", "model"}, {"providers", "provider"}} {
		rec, resp := doHistory(t, tr, base+"&chart="+tc.chart)
		if rec.Code != http.StatusOK || resp.Chart != tc.chart || resp.Group != tc.group {
			t.Errorf("chart=%s: want 200 chart=%s group=%s, got %d/%q/%q", tc.chart, tc.chart, tc.group, rec.Code, resp.Chart, resp.Group)
		}
	}
}

// TestHandleGetHistory_TokensSplit: chart=tokens returns token counts + the
// in/out/cache split, and no USD forecast.
func TestHandleGetHistory_TokensSplit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cost")
	seedPhase2(t, dir)
	tr := filesystem.NewCostTrackerWithDir(dir)

	rec, resp := doHistory(t, tr, "start=1000&end=1400&bucket=100&chart=tokens")
	if rec.Code != http.StatusOK || resp.Chart != "tokens" {
		t.Fatalf("want 200 chart=tokens, got %d/%q", rec.Code, resp.Chart)
	}
	if resp.TokenSplit == nil {
		t.Fatal("chart=tokens must carry token_split")
	}
	// s1 delta: in 200, out 20, cache 20; s2 delta: in 100, out 20, cache 0.
	if resp.TokenSplit.Input != 300 || resp.TokenSplit.Output != 40 || resp.TokenSplit.Cache != 20 {
		t.Errorf("split: want in=300 out=40 cache=20, got %+v", resp.TokenSplit)
	}
	if resp.Forecast != nil {
		t.Errorf("tokens chart should not forecast USD, got %+v", resp.Forecast)
	}
}

// TestHandleGetHistory_UnknownBucketRule: the unknown bucket is surfaced only at
// ≥10% of the window total, else dropped.
func TestHandleGetHistory_UnknownBucketRule(t *testing.T) {
	const base = "start=1000&end=1400&bucket=100&group=branch&forecast=false"
	hasUnknown := func(resp historyResponse) bool {
		for _, c := range resp.TopContributors {
			if c.Label == "unknown" {
				return true
			}
		}
		return false
	}

	// Kept: missing-branch session is 0.30 / 1.20 = 25% (well above 10%).
	dirA := filepath.Join(t.TempDir(), "cost")
	seedCostRows(t, dirA, "proj-a", []map[string]any{
		{"ts": 1050, "project": "proj-a", "branch": "main", "session": "s1", "cost": 1.00},
		{"ts": 1150, "project": "proj-a", "branch": "main", "session": "s1", "cost": 1.90},
		{"ts": 1050, "project": "proj-a", "session": "s2", "cost": 5.00},
		{"ts": 1150, "project": "proj-a", "session": "s2", "cost": 5.30},
	})
	if _, resp := doHistory(t, filesystem.NewCostTrackerWithDir(dirA), base); !hasUnknown(resp) {
		t.Errorf("≥10%% unknown should be surfaced, got %+v", resp.TopContributors)
	}

	// Dropped: missing-branch session is 0.05 / 0.95 ≈ 5%.
	dirB := filepath.Join(t.TempDir(), "cost")
	seedCostRows(t, dirB, "proj-a", []map[string]any{
		{"ts": 1050, "project": "proj-a", "branch": "main", "session": "s1", "cost": 1.00},
		{"ts": 1150, "project": "proj-a", "branch": "main", "session": "s1", "cost": 1.90},
		{"ts": 1050, "project": "proj-a", "session": "s2", "cost": 5.00},
		{"ts": 1150, "project": "proj-a", "session": "s2", "cost": 5.05},
	})
	_, resp := doHistory(t, filesystem.NewCostTrackerWithDir(dirB), base)
	if hasUnknown(resp) {
		t.Errorf("<10%% unknown should be dropped, got %+v", resp.TopContributors)
	}
	if d := resp.Total - 0.90; d > 1e-9 || d < -1e-9 {
		t.Errorf("dropped unknown excluded from total: want 0.90, got %v", resp.Total)
	}
}

// TestHandleGetHistory_Drilldown: scope filters rows to one contributor and is
// echoed back for the breadcrumb.
func TestHandleGetHistory_Drilldown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cost")
	seedCostRows(t, dir, "proj-a", []map[string]any{
		{"ts": 1050, "project": "proj-a", "branch": "main", "session": "s1", "cost": 1.00},
		{"ts": 1150, "project": "proj-a", "branch": "main", "session": "s1", "cost": 1.30},
	})
	seedCostRows(t, dir, "proj-b", []map[string]any{
		{"ts": 1050, "project": "proj-b", "branch": "x", "session": "s2", "cost": 2.00},
		{"ts": 1150, "project": "proj-b", "branch": "x", "session": "s2", "cost": 2.50},
	})
	tr := filesystem.NewCostTrackerWithDir(dir)

	rec, resp := doHistory(t, tr, "start=1000&end=1400&bucket=100&group=branch&scope=project:proj-a&forecast=false")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if resp.Scope != "project:proj-a" {
		t.Errorf("scope echo: want project:proj-a, got %q", resp.Scope)
	}
	if d := resp.Total - 0.30; d > 1e-9 || d < -1e-9 {
		t.Errorf("scoped total: want 0.30 (proj-a only), got %v", resp.Total)
	}
	if len(resp.TopContributors) != 1 || resp.TopContributors[0].Label != "main" {
		t.Errorf("scoped contributors: want [main], got %+v", resp.TopContributors)
	}

	// A malformed scope is ignored (no filter, empty echo).
	_, resp2 := doHistory(t, tr, "start=1000&end=1400&bucket=100&group=branch&scope=bogus")
	if resp2.Scope != "" {
		t.Errorf("malformed scope should echo empty, got %q", resp2.Scope)
	}
}

func TestHandleGetHistory_BadRequests(t *testing.T) {
	tr := filesystem.NewCostTrackerWithDir(filepath.Join(t.TempDir(), "cost"))
	for _, q := range []string{"chart=bogus", "group=bogus", "range=bogus", "start=abc&end=def", "start=2000&end=1000", "chart=cost&group=token_type", "chart=tokens&token_type=bogus"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/history?"+q, nil)
		rec := httptest.NewRecorder()
		handleGetHistory(tr, nil, nil)(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: want 400, got %d", q, rec.Code)
		}
	}
}

// TestHandleGetHistory_GroupByTokenType: chart=tokens&group=token_type stacks
// the four token kinds as series keys (cache_creation absent here, zero delta).
func TestHandleGetHistory_GroupByTokenType(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cost")
	seedPhase2(t, dir)
	tr := filesystem.NewCostTrackerWithDir(dir)

	rec, resp := doHistory(t, tr, "start=1000&end=1400&bucket=100&chart=tokens&group=token_type")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if resp.Group != "token_type" {
		t.Errorf("group: want token_type, got %q", resp.Group)
	}
	got := map[string]float64{}
	for _, c := range resp.TopContributors {
		got[c.Label] = c.Value
	}
	// input = 200+100, output = 20+20, cache_read = 20+0.
	for k, v := range map[string]float64{"input": 300, "output": 40, "cache_read": 20} {
		if got[k] != v {
			t.Errorf("band %s: want %v, got %v (all=%+v)", k, v, got[k], got)
		}
	}
}

// TestHandleGetHistory_CrossFilter: a non-grouped filter narrows the data, while
// a filter on the active group dimension is ignored (never both axis & filter).
func TestHandleGetHistory_CrossFilter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cost")
	seedPhase2(t, dir)
	tr := filesystem.NewCostTrackerWithDir(dir)
	const base = "start=1000&end=1400&bucket=100&forecast=false"

	// provider=anthropic keeps only s1 (s2 is openai): proj-a total = s1's 0.40.
	rec, resp := doHistory(t, tr, base+"&chart=cost&group=project&provider=anthropic")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if d := resp.Total - 0.40; len(resp.TopContributors) != 1 || resp.TopContributors[0].Label != "proj-a" || d > 1e-9 || d < -1e-9 {
		t.Errorf("provider=anthropic: want proj-a total 0.40, got total=%v contribs=%+v", resp.Total, resp.TopContributors)
	}

	// Filtering on the active group dimension is dropped: group=provider with a
	// provider filter still shows both providers.
	rec, resp = doHistory(t, tr, base+"&group=provider&provider=anthropic")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(resp.TopContributors) != 2 {
		t.Errorf("provider filter on grouped axis should be ignored, want 2 providers, got %+v", resp.TopContributors)
	}
}

func TestHandleGetHistory_NilTrackerEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?range=week", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, nil, nil)(rec, req)
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
