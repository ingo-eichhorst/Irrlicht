package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"irrlicht/core/adapters/outbound/httputil"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// costAttachCache caches the last ProjectCostsInWindows result so successive
// /api/v1/sessions hits within costAttachTTL reuse one scan. Shared across
// requests; the zero value is an empty cache.
type costAttachCache struct {
	mu          sync.RWMutex
	generatedAt time.Time
	byTimeframe map[string]map[string]float64
}

func (c *costAttachCache) get(now time.Time) (map[string]map[string]float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.byTimeframe == nil || now.Sub(c.generatedAt) > costAttachTTL {
		return nil, false
	}
	return c.byTimeframe, true
}

func (c *costAttachCache) put(now time.Time, v map[string]map[string]float64) {
	c.mu.Lock()
	c.generatedAt = now
	c.byTimeframe = v
	c.mu.Unlock()
}

func handleGetSessions(repo outbound.SessionRepository, orchMonitor *services.OrchestratorMonitor, tracker outbound.CostTracker) http.HandlerFunc {
	cache := &costAttachCache{}
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := repo.ListAll()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		resp := session.BuildDashboard(sessions, orchMonitor.State("gastown"))
		if tracker != nil {
			attachGroupCosts(resp, tracker, cache)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// attachGroupCosts populates each top-level group's Costs map with the
// trailing-window cost for day/week/month/year. Orchestrator groups are
// skipped — their agents span projects, so group-level aggregation is
// ambiguous. Uses a single per-file scan (ProjectCostsInWindows) + a small
// per-handler TTL cache to keep I/O bounded under concurrent polling.
func attachGroupCosts(groups []*session.AgentGroup, tracker outbound.CostTracker, cache *costAttachCache) {
	now := time.Now()
	byTf, ok := cache.get(now)
	if !ok {
		m, err := tracker.ProjectCostsInWindows(costTimeframeSeconds)
		if err != nil {
			return
		}
		cache.put(now, m)
		byTf = m
	}
	for _, g := range groups {
		if g == nil || g.Type == "gastown" {
			continue
		}
		costs := make(map[string]float64, len(costTimeframeSeconds))
		for tf := range costTimeframeSeconds {
			if v, ok := byTf[tf][g.Name]; ok {
				costs[tf] = v
			}
		}
		if len(costs) > 0 {
			g.Costs = costs
		}
	}
}

func handleGetState(repo outbound.SessionRepository) http.HandlerFunc {
	type sessionEntry struct {
		ID                 string  `json:"id"`
		ProjectName        string  `json:"projectName,omitempty"`
		State              string  `json:"state"`
		Model              string  `json:"model,omitempty"`
		ContextUtilization float64 `json:"contextUtilization"`
		TotalTokens        int64   `json:"totalTokens"`
	}

	type stateResponse struct {
		Sessions     []sessionEntry `json:"sessions"`
		SessionCount int            `json:"sessionCount"`
		WorkingCount int            `json:"workingCount"`
		WaitingCount int            `json:"waitingCount"`
		ReadyCount   int            `json:"readyCount"`
		LastUpdated  string         `json:"lastUpdated"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := repo.ListAll()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		entries := make([]sessionEntry, 0, len(sessions))
		var workingCount, waitingCount, readyCount int
		for _, s := range sessions {
			var ctxUtil float64
			var totalTokens int64
			if s.Metrics != nil {
				ctxUtil = s.Metrics.ContextUtilization
				totalTokens = s.Metrics.TotalTokens
			}
			model := s.Model
			if s.Metrics != nil && s.Metrics.ModelName != "" && s.Metrics.ModelName != "unknown" {
				model = s.Metrics.ModelName
			}
			entries = append(entries, sessionEntry{
				ID:                 s.SessionID,
				ProjectName:        s.ProjectName,
				State:              s.State,
				Model:              model,
				ContextUtilization: ctxUtil,
				TotalTokens:        totalTokens,
			})
			switch s.State {
			case session.StateWorking:
				workingCount++
			case session.StateWaiting:
				waitingCount++
			case session.StateReady:
				readyCount++
			}
		}

		resp := stateResponse{
			Sessions:     entries,
			SessionCount: len(sessions),
			WorkingCount: workingCount,
			WaitingCount: waitingCount,
			ReadyCount:   readyCount,
			LastUpdated:  time.Now().UTC().Format(time.RFC3339),
		}

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	}
}

// localhostOnly wraps an HTTP handler to reject requests not originating from
// localhost or Unix sockets. Used to protect sensitive endpoints like pprof.
func localhostOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httputil.IsLoopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}
