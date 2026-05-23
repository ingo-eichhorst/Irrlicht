package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"irrlicht/core/adapters/outbound/httputil"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// sessionsResponse is the /api/v1/sessions payload. Groups is the dashboard
// hierarchy (per-project group costs live on each group's `costs` field);
// ProviderCosts holds per-provider trailing-window spend
// (providerKey → timeframe → USD) so clients can render windowed usage chips
// without re-attributing project costs — a single project can mix providers.
type sessionsResponse struct {
	Groups        []*session.AgentGroup         `json:"groups"`
	ProviderCosts map[string]map[string]float64 `json:"provider_costs,omitempty"`
}

// costAttachCache caches the last project + provider cost scans so successive
// /api/v1/sessions hits within costAttachTTL reuse them. A single TTL governs
// both maps. Shared across requests; the zero value is an empty cache.
type costAttachCache struct {
	mu          sync.RWMutex
	generatedAt time.Time
	byProject   map[string]map[string]float64 // timeframe → project → USD
	byProvider  map[string]map[string]float64 // timeframe → provider → USD
}

func (c *costAttachCache) get(now time.Time) (byProject, byProvider map[string]map[string]float64, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.byProject == nil || now.Sub(c.generatedAt) > costAttachTTL {
		return nil, nil, false
	}
	return c.byProject, c.byProvider, true
}

func (c *costAttachCache) put(now time.Time, byProject, byProvider map[string]map[string]float64) {
	c.mu.Lock()
	c.generatedAt = now
	c.byProject = byProject
	c.byProvider = byProvider
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
		// Cross-account rate-limit inheritance (issue #309): wrapper
		// sessions (Pi, OpenCode) inherit the subscription quota
		// snapshot from a first-party CLI authenticated to the same
		// OAuth account. Mutates `sessions` in place — the dashboard
		// builder then sees the inherited snapshots and the chip
		// renders for the wrapper just like it does for the donor.
		services.InheritRateLimits(sessions, "")
		groups := session.BuildDashboard(sessions, orchMonitor.State("gastown"))
		resp := sessionsResponse{Groups: groups}
		if tracker != nil {
			byProject, byProvider := costMaps(tracker, cache)
			attachGroupCosts(groups, byProject)
			resp.ProviderCosts = providerCostsByProvider(byProvider)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// costMaps returns the per-project and per-provider trailing-window cost maps,
// recomputing both via a single tracker scan when the cache is cold or stale.
// Either map is nil if the scan failed; callers must tolerate nil. The single
// scan keeps I/O bounded under concurrent polling.
func costMaps(tracker outbound.CostTracker, cache *costAttachCache) (byProject, byProvider map[string]map[string]float64) {
	now := time.Now()
	if p, pv, ok := cache.get(now); ok {
		return p, pv
	}
	p, pv, err := tracker.CostsInWindows(costTimeframeSeconds)
	if err != nil {
		return nil, nil
	}
	cache.put(now, p, pv)
	return p, pv
}

// attachGroupCosts populates each top-level group's Costs map with the
// trailing-window cost for day/week/month/year. Orchestrator groups are
// skipped — their agents span projects, so group-level aggregation is
// ambiguous.
func attachGroupCosts(groups []*session.AgentGroup, byTf map[string]map[string]float64) {
	if byTf == nil {
		return
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

// providerCostsByProvider inverts the tracker's timeframe→provider→USD map
// into the response shape providerKey→timeframe→USD (e.g.
// {"anthropic": {"day": 0.5, ...}}). Empty-provider buckets are dropped.
// Returns nil when there's nothing to report so the field is omitted.
func providerCostsByProvider(byTf map[string]map[string]float64) map[string]map[string]float64 {
	if byTf == nil {
		return nil
	}
	out := make(map[string]map[string]float64)
	for tf, perProvider := range byTf {
		for provider, v := range perProvider {
			if provider == "" {
				continue
			}
			m := out[provider]
			if m == nil {
				m = make(map[string]float64, len(costTimeframeSeconds))
				out[provider] = m
			}
			m[tf] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handleGetVersion serves the daemon's build version. Frontends use it to
// render `Irrlicht v$VERSION` in their app header without baking the value
// into their own bundle.
func handleGetVersion(version string) http.HandlerFunc {
	type versionResp struct {
		Version string `json:"version"`
	}
	body, _ := json.Marshal(versionResp{Version: version})
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}
}

// handleGetAgents serves the registered adapter branding so frontends can look
// up an adapter's display name and icon by `name` instead of hardcoding their
// own switches. The slice mirrors the order configured in main.go's agents;
// frontends should treat ordering as informational only and key by `name`.
func handleGetAgents(allAgents []agent.Agent) http.HandlerFunc {
	type agentEntry struct {
		Name         string `json:"name"`
		DisplayName  string `json:"display_name"`
		IconSVGLight string `json:"icon_svg_light"`
		IconSVGDark  string `json:"icon_svg_dark"`
	}
	entries := make([]agentEntry, 0, len(allAgents))
	for _, a := range allAgents {
		entries = append(entries, agentEntry{
			Name:         a.Identity.Name,
			DisplayName:  a.Identity.DisplayName,
			IconSVGLight: a.Identity.IconSVGLight,
			IconSVGDark:  a.Identity.IconSVGDark,
		})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
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
