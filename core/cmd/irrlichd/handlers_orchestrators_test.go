package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/orchestrator"
)

// staticOrchWatcher is an inbound.OrchestratorWatcher that hands every
// subscriber one fixed snapshot.
type staticOrchWatcher struct{ st orchestrator.State }

func (w staticOrchWatcher) Name() string                          { return w.st.Adapter }
func (w staticOrchWatcher) Detected() bool                        { return true }
func (w staticOrchWatcher) State() *orchestrator.State            { return &w.st }
func (w staticOrchWatcher) Watch(ctx context.Context) error       { <-ctx.Done(); return ctx.Err() }
func (w staticOrchWatcher) Unsubscribe(<-chan orchestrator.State) {}
func (w staticOrchWatcher) Subscribe() <-chan orchestrator.State {
	ch := make(chan orchestrator.State, 1)
	ch <- w.st
	return ch
}

// Issue #2148, red-first on 796a07307: /api/v1/sessions built the dashboard
// from orchMonitor.State("gastown") alone, so a second orchestrator the
// monitor held never reached the wire. Both states here use only fields that
// existed before #2148 (Adapter, Running, Root, Codebases, named Workers).
func TestHandleGetSessions_ShowsEveryOrchestrator(t *testing.T) {
	gastown := orchestrator.State{
		Adapter: "gastown", Running: true, Root: "/gt",
		Codebases: []orchestrator.Codebase{{
			Name: "irrlicht",
			Worktrees: []orchestrator.Worktree{{
				Path:    "/gt/irrlicht/polecats/p1",
				Workers: []orchestrator.Worker{{Role: "polecat", Name: "p1", State: "idle"}},
			}},
		}},
	}
	pool := orchestrator.State{
		Adapter: "workerlist", Running: true,
		Codebases: []orchestrator.Codebase{{
			Name: "team-a", Status: "operational",
			Worktrees: []orchestrator.Worktree{{
				Workers: []orchestrator.Worker{{Role: "worker", Name: "team-a-m1-1", ID: "proj-a1b2", State: "busy"}},
			}},
		}},
	}

	orchMonitor := services.NewOrchestratorMonitor(nil, services.NewPushService(), e2eLog{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = orchMonitor.Run(ctx) }()
	// Registered workerlist first: the payload order must not depend on it.
	orchMonitor.AddWatcher(ctx, staticOrchWatcher{st: pool})
	orchMonitor.AddWatcher(ctx, staticOrchWatcher{st: gastown})
	start := time.Now()
	if !pollUntil(t, 2*time.Second, 2*time.Millisecond, func() bool {
		return orchMonitor.State("gastown") != nil && orchMonitor.State("workerlist") != nil
	}) {
		t.Fatalf("monitor did not hold both states after %v", time.Since(start))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(filesystem.NewWithDir(t.TempDir()), orchMonitor, nil, nil, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Groups []struct {
			Type string `json:"type"`
		} `json:"groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var types []string
	for _, g := range out.Groups {
		types = append(types, g.Type)
	}
	if want := []string{"gastown", "workerlist"}; !slices.Equal(types, want) {
		t.Errorf("top-level group types = %q, want %q (one group per orchestrator, sorted by adapter)", types, want)
	}
}
