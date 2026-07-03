package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"irrlicht/core/adapters/outbound/httputil"
	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// costTimeframeSeconds maps the four supported time-frame keys to their
// trailing-window duration in seconds. These are rolling windows (not
// calendar-aligned) and are embedded under each project group's "costs"
// field in the /api/v1/sessions response.
var costTimeframeSeconds = map[string]int64{
	"day":   24 * 3600,
	"week":  7 * 24 * 3600,
	"month": 30 * 24 * 3600,
	"year":  365 * 24 * 3600,
}

// costAttachTTL bounds how stale the cached per-project cost maps may be
// before the handler recomputes them. Well below either client's 30 s
// poll cadence, short enough to keep the dashboard feeling live.
const costAttachTTL = 5 * time.Second

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

func handleGetSessions(repo outbound.SessionRepository, orchMonitor *services.OrchestratorMonitor, tracker outbound.CostTracker, controllable func(sessionID string) bool) http.HandlerFunc {
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
		annotateControllable(groups, controllable)
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

// annotateControllable walks the group tree and marks each agent (and nested
// child) Controllable per the InputService gate. No-op when fn is nil (tests,
// or before the backchannel feature is wired).
func annotateControllable(groups []*session.AgentGroup, fn func(sessionID string) bool) {
	if fn == nil {
		return
	}
	var walkAgents func(agents []*session.Agent)
	walkAgents = func(agents []*session.Agent) {
		for _, a := range agents {
			if a == nil || a.SessionState == nil {
				continue
			}
			a.Controllable = fn(a.SessionID)
			walkAgents(a.Children)
		}
	}
	var walkGroups func(gs []*session.AgentGroup)
	walkGroups = func(gs []*session.AgentGroup) {
		for _, g := range gs {
			if g == nil {
				continue
			}
			walkAgents(g.Agents)
			walkGroups(g.Groups)
		}
	}
	walkGroups(groups)
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
// trailing-window cost for day/week/month/year. A regular project group gets
// its single project's cost; an orchestrator (gastown) group gets the sum of
// the distinct project costs across every session beneath it (rigs span
// projects), counting a shared project once.
func attachGroupCosts(groups []*session.AgentGroup, byTf map[string]map[string]float64) {
	if byTf == nil {
		return
	}
	for _, g := range groups {
		if g == nil {
			continue
		}
		costs := make(map[string]float64, len(costTimeframeSeconds))
		if g.Type == "gastown" {
			projects := collectProjectNames(g)
			for tf := range costTimeframeSeconds {
				var sum float64
				for p := range projects {
					if v, ok := byTf[tf][p]; ok {
						sum += v
					}
				}
				if sum > 0 {
					costs[tf] = sum
				}
			}
		} else {
			for tf := range costTimeframeSeconds {
				if v, ok := byTf[tf][g.Name]; ok {
					costs[tf] = v
				}
			}
		}
		if len(costs) > 0 {
			g.Costs = costs
		}
	}
}

// collectProjectNames returns the distinct, non-empty ProjectNames of every
// agent under g — direct agents, agents in nested sub-groups (rigs), and their
// children. De-duped so a project shared by multiple orchestrator sessions is
// counted once.
//
// Caveat: trailing-window cost is keyed per-project, not per-session, so if a
// project has sessions both under the orchestrator and in a regular group, its
// whole windowed cost is attributed to the gastown total AND to that regular
// group — there's no per-session split to apportion. Acceptable for an
// at-a-glance orchestrator rollup.
func collectProjectNames(g *session.AgentGroup) map[string]struct{} {
	out := map[string]struct{}{}
	var walk func(agents []*session.Agent)
	walk = func(agents []*session.Agent) {
		for _, a := range agents {
			if a == nil || a.SessionState == nil {
				continue
			}
			if a.ProjectName != "" {
				out[a.ProjectName] = struct{}{}
			}
			walk(a.Children)
		}
	}
	walk(g.Agents)
	for _, sub := range g.Groups {
		for p := range collectProjectNames(sub) {
			out[p] = struct{}{}
		}
	}
	return out
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

// History tab (issues #369 / #750). Phase 1 served chart=cost grouped by
// project; Phase 2 adds the tokens/models/providers chart types and the
// branch/provider/model/session group axes plus drilldown scoping, all computed
// from the cost snapshot files via CostTracker.CostSeries. Phase 3 (#751) adds
// chart=agents, a concurrent-agents series reconstructed from the lifecycle
// recordings via ConcurrencyReader.AgentsSeries; chart=state stays a 501 stub.

type historyPoint struct {
	TS int64 `json:"ts"`
	// Project carries the group key for the point — a project, branch,
	// provider, model, or session id depending on ?group. The json tag stays
	// "project" for Phase 1 wire compatibility (web + macOS decoders).
	Project string  `json:"project"`
	Value   float64 `json:"value"`
}

type historyContributor struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// historyTokenSplit mirrors outbound.TokenSplit on the wire: the window's
// aggregate token throughput by kind, present only for chart=tokens. Drives the
// tokens side panel (in/out/cache).
type historyTokenSplit struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cache  float64 `json:"cache"`
}

// historyConcurrency is the concurrent-agents summary, present only for
// chart=agents (#751). Peak is the window's max simultaneous agents, Average the
// time-weighted mean, Current the count still active at the window's end edge.
// Drives the agents side panel.
type historyConcurrency struct {
	Peak    float64 `json:"peak"`
	Average float64 `json:"average"`
	Current float64 `json:"current"`
}

type historyForecastPoint struct {
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

type historyForecast struct {
	Projected      float64                `json:"projected"`
	Basis          string                 `json:"basis"`
	HorizonBuckets int                    `json:"horizon_buckets"`
	Series         []historyForecastPoint `json:"series"`
}

type historyResponse struct {
	Range           string               `json:"range"`
	Chart           string               `json:"chart"`
	Group           string               `json:"group"` // effective stacking axis (pinned to model/provider for chart=models|providers)
	Start           int64                `json:"start"`
	End             int64                `json:"end"`
	BucketSeconds   int64                `json:"bucket_seconds"`
	BucketStarts    []int64              `json:"bucket_starts"`
	Total           float64              `json:"total"`
	Series          []historyPoint       `json:"series"`
	TopContributors []historyContributor `json:"top_contributors"`
	Forecast        *historyForecast     `json:"forecast,omitempty"`
	TokenSplit      *historyTokenSplit   `json:"token_split,omitempty"` // chart=tokens only
	Concurrency     *historyConcurrency  `json:"concurrency,omitempty"` // chart=agents only
	Scope           string               `json:"scope,omitempty"`       // active drilldown filter "field:value"
}

// historyYieldProject is one project's productive/reverted/unknown cost split
// and the resulting yield ratio (#373).
type historyYieldProject struct {
	Project        string  `json:"project"`
	ProductiveCost float64 `json:"productive_cost"`
	RevertedCost   float64 `json:"reverted_cost"`
	UnknownCost    float64 `json:"unknown_cost"`
	TotalCost      float64 `json:"total_cost"` // productive + reverted (yield denominator)
	Yield          float64 `json:"yield"`      // productive / total; 0 when total == 0
	RevertedCount  int     `json:"reverted_count"`
}

// historyYieldResponse is the chart=yield payload (#373): per-project productive
// vs reverted spend and the headline ratio over the selected window. Unlike the
// cost chart it is a per-project aggregate, not a time series — it reads
// completed (ready) sessions directly, since yield is a per-session property.
type historyYieldResponse struct {
	Range          string                `json:"range"`
	Chart          string                `json:"chart"`
	Group          string                `json:"group"`
	Start          int64                 `json:"start"`
	End            int64                 `json:"end"`
	ProductiveCost float64               `json:"productive_cost"`
	RevertedCost   float64               `json:"reverted_cost"`
	UnknownCost    float64               `json:"unknown_cost"`
	TotalCost      float64               `json:"total_cost"`
	Yield          float64               `json:"yield"`
	Projects       []historyYieldProject `json:"projects"`
}

// historySessionLister is the narrow read the yield aggregation needs over the
// session repository.
type historySessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// handleGetHistory serves GET /api/v1/history?range=&chart=&group=&start=&end=
// &bucket=&forecast=&forecast_buckets=. Range is a trailing window
// (day|week|month|year), a calendar shorthand (this-month), or an explicit
// start&end (unix seconds). Bucket granularity is downsampled at read time.
func handleGetHistory(tracker outbound.CostTracker, sessions historySessionLister, concurrency outbound.ConcurrencyReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		chart := q.Get("chart")
		if chart == "" {
			chart = "cost"
		}
		switch chart {
		case "cost", "tokens", "models", "providers":
			// implemented (Phase 2)
		case "yield":
			// implemented (#373) — handled after range resolution below
		case "agents":
			// implemented (Phase 3) — handled after range resolution below
		case "state":
			// time-in-state is the issue's optional second half (#751); the
			// reconstruction exists but no chart is wired yet.
			writeHistoryNotImplemented(w, "chart="+chart, 3)
			return
		default:
			http.Error(w, "unknown chart: "+chart, http.StatusBadRequest)
			return
		}

		group := q.Get("group")
		if group == "" {
			group = "project"
		}
		switch group {
		case "project", "branch", "provider", "model", "session":
			// implemented (Phase 2)
		case "token_type":
			// implemented (faceted) — tokens metric only (validated below)
		default:
			http.Error(w, "unknown group: "+group, http.StatusBadRequest)
			return
		}

		// chart picks the measured metric; models/providers are presets that
		// pin the stacking axis to that dimension (one-click "cost by X").
		// agents is reconstructed per project only.
		metric := "cost"
		switch chart {
		case "tokens":
			metric = "tokens"
		case "models":
			group = "model"
		case "providers":
			group = "provider"
		case "agents":
			group = "project"
		}

		// token_type is inherently a token concept — it can't slice cost (we
		// store no per-token-type cost on disk).
		if group == "token_type" && metric != "tokens" {
			http.Error(w, "group=token_type requires chart=tokens", http.StatusBadRequest)
			return
		}

		scopeField, scopeValue := parseHistoryScope(q.Get("scope"))
		scopeEcho := ""
		if scopeField != "" {
			scopeEcho = scopeField + ":" + scopeValue
		}

		rangeKey, start, end, ok := resolveHistoryRange(q)
		if !ok {
			http.Error(w, "invalid range: use range=day|week|month|year|this-month or start&end (unix seconds)", http.StatusBadRequest)
			return
		}

		// Yield is a per-project aggregate over completed sessions, not a cost
		// time series — handle it before the cost-tracker path (#373).
		if chart == "yield" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(buildYieldResponse(rangeKey, group, start, end, sessions))
			return
		}

		bucketSeconds := historyBucketSeconds(q, end-start)

		if chart == "agents" {
			var cr *outbound.ConcurrencyResult
			if concurrency != nil {
				c, err := concurrency.AgentsSeries(outbound.SeriesQuery{
					Start:         start,
					End:           end,
					BucketSeconds: bucketSeconds,
					Group:         group,
					ScopeField:    scopeField,
					ScopeValue:    scopeValue,
				})
				if err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				cr = c
			}
			if cr == nil {
				// No reader (recordings dir unresolved): empty-but-valid payload.
				cr = &outbound.ConcurrencyResult{Start: start, End: end, BucketSeconds: bucketSeconds, BucketStarts: []int64{}, ByKey: map[string][]float64{}, PeakByKey: map[string]float64{}}
			}
			resp := buildAgentsResponse(rangeKey, scopeEcho, cr)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Orthogonal cross-filters (comma-separated, multi-value). The active
		// group dimension is never filtered — drop whichever matches it — so a
		// dimension is never both a stacking axis and a filter.
		projects := parseCSVParam(q.Get("project"))
		providers := parseCSVParam(q.Get("provider"))
		tokenTypes, okTT := parseTokenTypeFilter(q.Get("token_type"))
		if !okTT {
			http.Error(w, "invalid token_type: use input|output|cache_read|cache_creation", http.StatusBadRequest)
			return
		}
		switch group {
		case "project":
			projects = nil
		case "provider":
			providers = nil
		case "token_type":
			tokenTypes = nil
		}

		var series *outbound.CostSeriesResult
		if tracker != nil {
			s, err := tracker.CostSeries(outbound.SeriesQuery{
				Start:         start,
				End:           end,
				BucketSeconds: bucketSeconds,
				Group:         group,
				Metric:        metric,
				ScopeField:    scopeField,
				ScopeValue:    scopeValue,
				Projects:      projects,
				Providers:     providers,
				TokenTypes:    tokenTypes,
			})
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			series = s
		}
		if series == nil {
			// No tracker (init failed): respond with an empty-but-valid payload
			// so the dashboard renders cleanly instead of erroring.
			series = &outbound.CostSeriesResult{Start: start, End: end, BucketSeconds: bucketSeconds, BucketStarts: []int64{}, ByKey: map[string][]float64{}, Totals: map[string]float64{}}
		}

		resp := buildHistoryResponse(rangeKey, chart, group, scopeEcho, series, q)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// parseHistoryScope parses a ?scope=field:value drilldown filter, used to
// re-scope the series to one contributor. Returns empty field/value (no filter)
// for an empty, malformed, or unknown-field scope. The value may itself contain
// colons (split on the first only).
func parseHistoryScope(s string) (field, value string) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", ""
	}
	switch f := s[:i]; f {
	case "project", "branch", "provider", "model", "session":
		return f, s[i+1:]
	}
	return "", ""
}

// parseCSVParam splits a comma-separated multi-value filter param into its
// trimmed, non-empty values. Returns nil (no constraint) for an empty param.
func parseCSVParam(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseTokenTypeFilter parses the ?token_type= cross-filter into validated token
// kinds. An empty param is no filter (nil, true); an unrecognized kind is
// rejected (nil, false) so the handler can 400 rather than silently ignore it.
func parseTokenTypeFilter(s string) ([]string, bool) {
	raw := parseCSVParam(s)
	if raw == nil {
		return nil, true
	}
	valid := map[string]bool{}
	for _, k := range outbound.TokenTypeKeys {
		valid[k] = true
	}
	for _, p := range raw {
		if !valid[p] {
			return nil, false
		}
	}
	return raw, true
}

// writeHistoryNotImplemented emits a 501 with a phase hint for chart types and
// groups scaffolded in the UI but not yet wired (Phase 2/3).
func writeHistoryNotImplemented(w http.ResponseWriter, what string, phase int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	json.NewEncoder(w).Encode(map[string]any{
		"error": what + " is not implemented in Phase 1",
		"phase": phase,
	})
}

// resolveHistoryRange resolves the requested window to [start, end) unix
// seconds. Precedence: explicit start&end → calendar shorthand / trailing
// window via range. Missing range defaults to "day". Returns ok=false on a
// malformed explicit range or an unknown range key.
func resolveHistoryRange(q url.Values) (rangeKey string, start, end int64, ok bool) {
	if s := q.Get("start"); s != "" {
		startN, err1 := strconv.ParseInt(s, 10, 64)
		endN, err2 := strconv.ParseInt(q.Get("end"), 10, 64)
		if err1 != nil || err2 != nil || endN <= startN {
			return "", 0, 0, false
		}
		return "custom", startN, endN, true
	}
	rk := q.Get("range")
	if rk == "" {
		rk = "day"
	}
	now := time.Now()
	nowUnix := now.Unix()
	switch rk {
	case "day", "week", "month", "year":
		return rk, nowUnix - costTimeframeSeconds[rk], nowUnix, true
	case "this-month":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return rk, first.Unix(), nowUnix, true
	default:
		return "", 0, 0, false
	}
}

// historyBucketSeconds picks a bucket width: an explicit positive ?bucket=
// override, else downsampled by span — 1 m for ≤2 d, 1 h for ≤14 d, else 1 d.
func historyBucketSeconds(q url.Values, span int64) int64 {
	if b := q.Get("bucket"); b != "" {
		if v, err := strconv.ParseInt(b, 10, 64); err == nil && v > 0 {
			return v
		}
	}
	switch {
	case span <= 2*24*3600:
		return 60
	case span <= 14*24*3600:
		return 3600
	default:
		return 86400
	}
}

// historyUnknownLabel / historyUnknownMinShare govern the "unknown" group
// bucket: rows missing a value on the group axis (branch/provider/model) are
// surfaced as one "unknown" contributor only when they are at least this share
// of the window total; below it the bucket is dropped rather than drawn as a
// misleading sliver. Project/session never produce an empty key.
const (
	historyUnknownLabel    = "unknown"
	historyUnknownMinShare = 0.10
)

// buildHistoryResponse flattens the per-key series into the response envelope:
// a sparse [{ts,project,value}] series (zero buckets omitted), per-key top
// contributors, and an optional linear forecast over the grand per-bucket
// total. The "project" json field carries the group key generically (Phase 1
// wire compat). For chart=tokens it also attaches the in/out/cache token split.
func buildHistoryResponse(rangeKey, chart, group, scope string, s *outbound.CostSeriesResult, q url.Values) historyResponse {
	resolveUnknownBucket(s)

	resp := historyResponse{
		Range:           rangeKey,
		Chart:           chart,
		Group:           group,
		Start:           s.Start,
		End:             s.End,
		BucketSeconds:   s.BucketSeconds,
		BucketStarts:    s.BucketStarts,
		Series:          []historyPoint{},
		TopContributors: []historyContributor{},
		Scope:           scope,
	}

	// Deterministic key order: total desc, then name.
	keys := make([]string, 0, len(s.Totals))
	for k := range s.Totals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if s.Totals[keys[i]] != s.Totals[keys[j]] {
			return s.Totals[keys[i]] > s.Totals[keys[j]]
		}
		return keys[i] < keys[j]
	})

	for _, k := range keys {
		resp.Total += s.Totals[k]
		for i, v := range s.ByKey[k] {
			if i >= len(s.BucketStarts) {
				break
			}
			if v != 0 {
				resp.Series = append(resp.Series, historyPoint{TS: s.BucketStarts[i], Project: k, Value: v})
			}
		}
	}
	for i, k := range keys {
		if i >= 5 {
			break
		}
		resp.TopContributors = append(resp.TopContributors, historyContributor{Label: k, Value: s.Totals[k]})
	}

	if s.TokenSplit != nil {
		resp.TokenSplit = &historyTokenSplit{Input: s.TokenSplit.Input, Output: s.TokenSplit.Output, Cache: s.TokenSplit.Cache}
	}

	// Forecast projects USD spend; it isn't meaningful for token counts.
	if chart != "tokens" && historyForecastEnabled(q) && resp.Total > 0 && len(s.BucketStarts) > 0 {
		resp.Forecast = computeLinearForecast(s.BucketStarts, s.BucketSeconds, resp.Total, historyForecastBuckets(q, len(s.BucketStarts)))
	}
	return resp
}

// buildAgentsResponse flattens a concurrency reconstruction into the history
// envelope (#751). It reuses the same sparse [{ts,project,value}] series +
// bucket_starts the cost chart's canvas renderer consumes (so the frontend
// renderer is shared untouched), ranks top contributors by each project's peak
// concurrency, and attaches the peak/average/current summary the agents side
// panel shows. Total carries the exact whole-window peak (the side panel's
// headline). No forecast or token split — neither is meaningful for a count.
func buildAgentsResponse(rangeKey, scope string, c *outbound.ConcurrencyResult) historyResponse {
	resp := historyResponse{
		Range:           rangeKey,
		Chart:           "agents",
		Group:           "project",
		Start:           c.Start,
		End:             c.End,
		BucketSeconds:   c.BucketSeconds,
		BucketStarts:    c.BucketStarts,
		Total:           c.Peak,
		Series:          []historyPoint{},
		TopContributors: []historyContributor{},
		Concurrency:     &historyConcurrency{Peak: c.Peak, Average: c.Average, Current: c.Current},
		Scope:           scope,
	}

	// Deterministic key order: peak desc, then name.
	keys := make([]string, 0, len(c.PeakByKey))
	for k := range c.PeakByKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if c.PeakByKey[keys[i]] != c.PeakByKey[keys[j]] {
			return c.PeakByKey[keys[i]] > c.PeakByKey[keys[j]]
		}
		return keys[i] < keys[j]
	})

	for _, k := range keys {
		for i, v := range c.ByKey[k] {
			if i >= len(c.BucketStarts) {
				break
			}
			if v != 0 {
				resp.Series = append(resp.Series, historyPoint{TS: c.BucketStarts[i], Project: k, Value: v})
			}
		}
	}
	for i, k := range keys {
		if i >= 5 {
			break
		}
		resp.TopContributors = append(resp.TopContributors, historyContributor{Label: k, Value: c.PeakByKey[k]})
	}
	return resp
}

// resolveUnknownBucket relabels or drops the "" key that CostSeries emits for
// rows missing a value on the group axis (branch/provider/model), per the ≥10%
// share rule. A no-op for project/session, which always carry a key.
func resolveUnknownBucket(s *outbound.CostSeriesResult) {
	uTotal, ok := s.Totals[""]
	if !ok {
		return
	}
	grand := 0.0
	for _, v := range s.Totals {
		grand += v
	}
	uSeries := s.ByKey[""]
	delete(s.Totals, "")
	delete(s.ByKey, "")
	if grand > 0 && uTotal/grand >= historyUnknownMinShare {
		s.Totals[historyUnknownLabel] = uTotal
		if uSeries != nil {
			s.ByKey[historyUnknownLabel] = uSeries
		}
	}
}

// buildYieldResponse aggregates completed (ready) sessions into per-project
// productive/reverted/unknown spend and the resulting yield ratio over
// [start,end) (#373). Sessions are windowed by UpdatedAt — their completion
// time — so a revert detected later never moves a session into a newer window.
// Only sessions that have gone ready (non-empty YieldState) are counted; spend
// from sessions still in flight is excluded. Unknown (non-git) spend is reported
// separately and kept out of the ratio's denominator.
func buildYieldResponse(rangeKey, group string, start, end int64, lister historySessionLister) historyYieldResponse {
	resp := historyYieldResponse{
		Range:    rangeKey,
		Chart:    "yield",
		Group:    group,
		Start:    start,
		End:      end,
		Projects: []historyYieldProject{},
	}
	if lister == nil {
		return resp
	}
	sessions, err := lister.ListAll()
	if err != nil {
		return resp
	}

	type agg struct {
		productive, reverted, unknown float64
		revertedCount                 int
	}
	byProject := make(map[string]*agg)
	for _, st := range sessions {
		if st == nil || st.YieldState == "" {
			continue // not a completed, ready-captured session
		}
		if st.UpdatedAt < start || st.UpdatedAt >= end {
			continue
		}
		cost := 0.0
		if st.Metrics != nil {
			cost = st.Metrics.EstimatedCostUSD
		}
		project := st.ProjectName
		if project == "" {
			project = "unknown"
		}
		a := byProject[project]
		if a == nil {
			a = &agg{}
			byProject[project] = a
		}
		switch st.YieldState {
		case session.YieldReverted:
			a.reverted += cost
			a.revertedCount++
		case session.YieldProductive:
			a.productive += cost
		default: // YieldUnknown
			a.unknown += cost
		}
	}

	// Project order: total (productive+reverted) desc, then name.
	names := make([]string, 0, len(byProject))
	for p := range byProject {
		names = append(names, p)
	}
	sort.Slice(names, func(i, j int) bool {
		ti := byProject[names[i]].productive + byProject[names[i]].reverted
		tj := byProject[names[j]].productive + byProject[names[j]].reverted
		if ti != tj {
			return ti > tj
		}
		return names[i] < names[j]
	})

	for _, p := range names {
		a := byProject[p]
		total := a.productive + a.reverted
		y := 0.0
		if total > 0 {
			y = a.productive / total
		}
		resp.Projects = append(resp.Projects, historyYieldProject{
			Project:        p,
			ProductiveCost: a.productive,
			RevertedCost:   a.reverted,
			UnknownCost:    a.unknown,
			TotalCost:      total,
			Yield:          y,
			RevertedCount:  a.revertedCount,
		})
		resp.ProductiveCost += a.productive
		resp.RevertedCost += a.reverted
		resp.UnknownCost += a.unknown
	}
	resp.TotalCost = resp.ProductiveCost + resp.RevertedCost
	if resp.TotalCost > 0 {
		resp.Yield = resp.ProductiveCost / resp.TotalCost
	}
	return resp
}

// historyForecastEnabled defaults to on; ?forecast=false|0 disables it.
func historyForecastEnabled(q url.Values) bool {
	switch q.Get("forecast") {
	case "false", "0":
		return false
	default:
		return true
	}
}

// historyForecastBuckets returns the projection horizon: an explicit positive
// ?forecast_buckets= override, else ~20% of the visible buckets (min 1, cap 60).
func historyForecastBuckets(q url.Values, n int) int {
	if b := q.Get("forecast_buckets"); b != "" {
		if v, err := strconv.Atoi(b); err == nil && v > 0 {
			return v
		}
	}
	h := n / 5
	if h < 1 {
		h = 1
	}
	if h > 60 {
		h = 60
	}
	return h
}

// computeLinearForecast projects future spend by extrapolating the window's
// mean per-bucket rate (total ÷ bucket count) forward over `horizon` buckets —
// a linear extrapolation of cumulative spend. It depends only on the total and
// the elapsed bucket count, not on where in the window the spend landed: the
// same total yields the same projection whether spend was bursty-early or
// bursty-late. (A least-squares fit over the sparse, mostly-zero per-bucket
// deltas does depend on burst position, which made the headline projection
// swing widely for identical totals.) basis is "linear" so the UI labels it an
// estimate.
func computeLinearForecast(bucketStarts []int64, bucketSeconds int64, currentTotal float64, horizon int) *historyForecast {
	n := len(bucketStarts)
	fc := &historyForecast{Basis: "linear", HorizonBuckets: horizon, Series: []historyForecastPoint{}}
	if n == 0 {
		fc.Projected = currentTotal
		return fc
	}
	rate := currentTotal / float64(n) // mean USD per bucket over the window
	lastTS := bucketStarts[n-1]
	for k := 1; k <= horizon; k++ {
		fc.Series = append(fc.Series, historyForecastPoint{TS: lastTS + int64(k)*bucketSeconds, Value: rate})
	}
	fc.Projected = currentTotal + rate*float64(horizon)
	return fc
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
		Name         string   `json:"name"`
		DisplayName  string   `json:"display_name"`
		IconSVGLight string   `json:"icon_svg_light"`
		IconSVGDark  string   `json:"icon_svg_dark"`
		Presets      []string `json:"presets"`
	}
	entries := make([]agentEntry, 0, len(allAgents))
	for _, a := range allAgents {
		// Supported backchannel presets (issue #754), sorted for a stable
		// contract; always a slice (never null) so the macOS editor can iterate
		// without a nil check.
		presets := make([]string, 0, len(a.Control.Presets))
		for p := range a.Control.Presets {
			presets = append(presets, p)
		}
		sort.Strings(presets)
		entries = append(entries, agentEntry{
			Name:         a.Identity.Name,
			DisplayName:  a.Identity.DisplayName,
			IconSVGLight: a.Identity.IconSVGLight,
			IconSVGDark:  a.Identity.IconSVGDark,
			Presets:      presets,
		})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}
}

// publishStatusResp is the shared shape of the GET and PUT
// /api/v1/relay/publish responses: {"enabled":false} when publishing is off,
// otherwise the forwarder's live link state.
type publishStatusResp struct {
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url,omitempty"`
	State       string `json:"state,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	DaemonID    string `json:"daemonId,omitempty"`
	DaemonLabel string `json:"daemonLabel,omitempty"`
}

// writePublishStatus encodes the controller's current publish state as JSON.
func writePublishStatus(w http.ResponseWriter, controller *relay.PublishController) {
	w.Header().Set("Content-Type", "application/json")
	enabled, s := controller.Status()
	if !enabled {
		json.NewEncoder(w).Encode(publishStatusResp{Enabled: false})
		return
	}
	json.NewEncoder(w).Encode(publishStatusResp{
		Enabled:     true,
		URL:         s.URL,
		State:       s.State,
		LastError:   s.LastError,
		DaemonID:    s.DaemonID,
		DaemonLabel: s.DaemonLabel,
	})
}

// handleGetPublishStatus serves the daemon → relay forwarder's live link state
// so the macOS app can show a publish-connection indicator (issue #718). The
// forwarder runs only while publishing is enabled; when it is off the controller
// reports {"enabled":false}.
func handleGetPublishStatus(controller *relay.PublishController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writePublishStatus(w, controller)
	}
}

// handlePutPublishStatus reconfigures publishing on the running daemon (issue
// #722): the macOS app PUTs {enabled,url,token} when its publish settings change
// instead of relaunching the daemon. Apply is idempotent, so the app can POST on
// every nudge. Responds with the resulting status (same shape as GET) so the
// caller sees the effect immediately. Registered loopback-only in main.go — it
// mutates forwarder config and carries the relay token in its body.
func handlePutPublishStatus(controller *relay.PublishController) http.HandlerFunc {
	type publishConfigReq struct {
		Enabled bool   `json:"enabled"`
		URL     string `json:"url"`
		Token   string `json:"token"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req publishConfigReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		controller.Apply(req.Enabled, req.URL, req.Token)
		writePublishStatus(w, controller)
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

// handleDiagnosticsBundle serves the diagnostics bundle (#736) as a gzip+tar
// download. Must be wrapped in localhostOnly by the caller — the bundle carries
// session paths and (pre-redaction) process argv. The bundle is bounded, so it
// builds in memory: a build failure returns a clean 500 rather than a truncated
// download.
func handleDiagnosticsBundle(svc *services.DiagnosticsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := svc.WriteBundle(&buf); err != nil {
			http.Error(w, "failed to build diagnostics bundle", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="irrlicht-diag-%s.tar.gz"`, fileSafe(Version)))
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		_, _ = w.Write(buf.Bytes())
	}
}

// fileSafe makes a version string safe for use in a download filename.
func fileSafe(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
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
