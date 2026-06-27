package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

type fakeYieldLister struct{ sessions []*session.SessionState }

func (f fakeYieldLister) ListAll() ([]*session.SessionState, error) { return f.sessions, nil }

func yieldSession(id, project, state string, cost float64, ts int64) *session.SessionState {
	return &session.SessionState{
		SessionID:   id,
		State:       session.StateReady,
		ProjectName: project,
		YieldState:  state,
		UpdatedAt:   ts,
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: cost},
	}
}

func TestBuildYieldResponse(t *testing.T) {
	now := time.Now().Unix()
	lister := fakeYieldLister{sessions: []*session.SessionState{
		yieldSession("s1", "alpha", session.YieldProductive, 3.0, now-100),
		yieldSession("s2", "alpha", session.YieldReverted, 1.0, now-100),
		yieldSession("s3", "beta", session.YieldProductive, 2.0, now-100),
		yieldSession("s4", "beta", session.YieldUnknown, 5.0, now-100),          // unattributed (non-git)
		yieldSession("s5", "alpha", session.YieldProductive, 9.0, now-10*86400), // out of the day window
		// A still-working session (no YieldState) must be excluded entirely.
		{SessionID: "s6", State: session.StateWorking, ProjectName: "alpha", UpdatedAt: now - 100, Metrics: &session.SessionMetrics{EstimatedCostUSD: 99}},
	}}

	resp := buildYieldResponse("day", "project", now-86400, now+1, lister)

	approx := func(name string, got, want float64) {
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	approx("productive", resp.ProductiveCost, 5.0) // s1 + s3
	approx("reverted", resp.RevertedCost, 1.0)     // s2
	approx("unknown", resp.UnknownCost, 5.0)       // s4
	approx("total", resp.TotalCost, 6.0)           // productive + reverted
	approx("yield", resp.Yield, 5.0/6.0)

	if len(resp.Projects) != 2 {
		t.Fatalf("want 2 projects, got %d", len(resp.Projects))
	}
	// Ordered by attributable total desc: alpha (4) before beta (2).
	if resp.Projects[0].Project != "alpha" {
		t.Errorf("first project = %q, want alpha", resp.Projects[0].Project)
	}
	approx("alpha yield", resp.Projects[0].Yield, 0.75)
	if resp.Projects[0].RevertedCount != 1 {
		t.Errorf("alpha reverted_count = %d, want 1", resp.Projects[0].RevertedCount)
	}
}

func TestBuildYieldResponse_NilLister(t *testing.T) {
	resp := buildYieldResponse("day", "project", 0, 1, nil)
	if resp.TotalCost != 0 || len(resp.Projects) != 0 {
		t.Errorf("nil lister: want empty, got %+v", resp)
	}
	if resp.Chart != "yield" {
		t.Errorf("chart = %q, want yield", resp.Chart)
	}
}

func TestHandleGetHistory_YieldEndpoint(t *testing.T) {
	now := time.Now().Unix()
	lister := fakeYieldLister{sessions: []*session.SessionState{
		yieldSession("s1", "alpha", session.YieldProductive, 2.0, now-100),
		yieldSession("s2", "alpha", session.YieldReverted, 2.0, now-100),
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=yield&range=day", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, lister)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp historyYieldResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if resp.Chart != "yield" {
		t.Errorf("chart = %q, want yield", resp.Chart)
	}
	if math.Abs(resp.Yield-0.5) > 1e-9 {
		t.Errorf("yield = %v, want 0.5", resp.Yield)
	}
}
