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
	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// errInternalErrorMsg is the generic 500 response body used across handlers
// in this file where the underlying error is already logged and its details
// aren't safe or useful to expose to the client.
const errInternalErrorMsg = "internal error"

// headerContentType and contentTypeJSON name the response header/value pair
// set by every JSON-encoding handler in this file.
const (
	headerContentType = "Content-Type"
	contentTypeJSON   = "application/json"
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
			http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
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
		w.Header().Set(headerContentType, contentTypeJSON)
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
		var costs map[string]float64
		if g.Type == "gastown" {
			costs = gastownGroupCosts(g, byTf)
		} else {
			costs = regularGroupCosts(g, byTf)
		}
		if len(costs) > 0 {
			g.Costs = costs
		}
	}
}

// gastownGroupCosts sums the distinct project costs across every session
// beneath an orchestrator group (rigs span projects), counting a shared
// project once — the per-timeframe half of attachGroupCosts' gastown branch.
func gastownGroupCosts(g *session.AgentGroup, byTf map[string]map[string]float64) map[string]float64 {
	costs := make(map[string]float64, len(costTimeframeSeconds))
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
	return costs
}

// regularGroupCosts returns a plain project group's own trailing-window
// costs, keyed by the group's name.
func regularGroupCosts(g *session.AgentGroup, byTf map[string]map[string]float64) map[string]float64 {
	costs := make(map[string]float64, len(costTimeframeSeconds))
	for tf := range costTimeframeSeconds {
		if v, ok := byTf[tf][g.Name]; ok {
			costs[tf] = v
		}
	}
	return costs
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

// historyStateResponse is the chart=state payload (#981, the Activity
// Matrix): a per-project, per-state agent-count grid, unlike every other
// chart's single-value-per-point series. Projects is row order (busiest
// first, by total activity across every state and bucket); ByState mirrors
// outbound.StateSeriesResult.ByState directly (state -> project -> per-bucket
// counts, each slice aligned to BucketStarts) — the matrix is dense, not the
// sparse [{ts,project,value}] shape the canvas time-series charts use, since
// every cell needs a value to render a grid. Concurrency reuses the agents
// side panel's peak/average/current shape (working+waiting combined).
type historyStateResponse struct {
	Range         string                          `json:"range"`
	Chart         string                          `json:"chart"`
	Group         string                          `json:"group"`
	Start         int64                           `json:"start"`
	End           int64                           `json:"end"`
	BucketSeconds int64                           `json:"bucket_seconds"`
	BucketStarts  []int64                         `json:"bucket_starts"`
	Projects      []string                        `json:"projects"`
	ByState       map[string]map[string][]float64 `json:"by_state"`
	Concurrency   *historyConcurrency             `json:"concurrency,omitempty"`
	Scope         string                          `json:"scope,omitempty"`
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

// historyDoraResponse is the chart=dora payload (#951): DORA metrics for one
// project's git repo over the selected window. Unlike the cost chart it's a
// period summary, not a time series — computed fresh per request, no
// persistence (see services.ComputeDoraMetrics).
type historyDoraResponse struct {
	Range               string     `json:"range"`
	Chart               string     `json:"chart"`
	Project             string     `json:"project"`
	Start               int64      `json:"start"`
	End                 int64      `json:"end"`
	Available           bool       `json:"available"`
	Message             string     `json:"message,omitempty"`
	DeploymentFrequency doraMetric `json:"deployment_frequency"`
	LeadTime            doraMetric `json:"lead_time"`
	ChangeFailureRate   doraMetric `json:"change_failure_rate"`
	MTTR                doraMetric `json:"mttr"`
}

// doraMetric mirrors dora.Metric for the wire as its own type, so a JSON tag
// change here never has to touch the domain package.
type doraMetric struct {
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	SampleSize int     `json:"sample_size"`
	Available  bool    `json:"available"`
	Message    string  `json:"message,omitempty"`
}

func toDoraMetric(m dora.Metric) doraMetric {
	return doraMetric{Value: m.Value, Unit: m.Unit, SampleSize: m.SampleSize, Available: m.Available, Message: m.Message}
}

// historyGitReader is the narrow git surface chart=dora needs, matching the
// historySessionLister convention of small per-consumer interfaces.
type historyGitReader interface {
	GetGitRoot(dir string) string
	ListReleaseTags(dir string) []dora.TagInfo
	CommitsInRange(dir, fromRef, toRef string) []dora.CommitInfo
	TagContaining(dir, hash string) string
}

// serveHistoryDoraChart serves chart=dora (#951): requires exactly one
// ?project=, since DORA metrics are inherently repo-scoped — unlike
// cost/yield's implicit "all projects." Any other resolution failure
// (project not found, not a git repo, no release tags) is a well-formed 200
// with Available:false, not an error, mirroring
// serveHistoryAgentsChart's nil-reader / fetchHistoryCostSeries's
// nil-tracker empty-but-valid payload convention.
func serveHistoryDoraChart(w http.ResponseWriter, git historyGitReader, sessions historySessionLister, project, rangeKey string, start, end int64) {
	if project == "" {
		http.Error(w, "chart=dora requires ?project=<name>", http.StatusBadRequest)
		return
	}
	if strings.Contains(project, ",") {
		http.Error(w, "chart=dora requires exactly one project, not a comma-separated list", http.StatusBadRequest)
		return
	}

	result, err := services.ComputeDoraMetrics(git, sessions, project, start, end)
	if err != nil {
		http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
		return
	}

	writeHistoryJSON(w, historyDoraResponse{
		Range:               rangeKey,
		Chart:               "dora",
		Project:             project,
		Start:               start,
		End:                 end,
		Available:           result.Available,
		Message:             result.Message,
		DeploymentFrequency: toDoraMetric(result.DeploymentFrequency),
		LeadTime:            toDoraMetric(result.LeadTime),
		ChangeFailureRate:   toDoraMetric(result.ChangeFailureRate),
		MTTR:                toDoraMetric(result.MTTR),
	})
}

// historyChartKnown validates the requested ?chart= value. Charts implemented
// in Phase 1-3 pass through; anything else is a client error (writes a 400).
// Returns false once it has already written the response, in which case the
// caller must return immediately.
func historyChartKnown(w http.ResponseWriter, chart string) bool {
	switch chart {
	case "cost", "tokens", "co2", "models", "providers":
		// implemented (Phase 2); co2 added for issue #829
	case "yield":
		// implemented (#373) — handled after range resolution below
	case "dora":
		// implemented (#951) — handled after range resolution below
	case "agents":
		// implemented (Phase 3) — handled after range resolution below
	case "state":
		// implemented (#981, the "Activity Matrix" — time-in-state, the
		// optional second half of #751) — handled after range resolution below
	default:
		http.Error(w, "unknown chart: "+chart, http.StatusBadRequest)
		return false
	}
	return true
}

// historyGroupKnown validates the requested ?group= axis, writing a 400 and
// returning false for anything outside the supported set.
func historyGroupKnown(w http.ResponseWriter, group string) bool {
	switch group {
	case "project", "branch", "provider", "model", "session":
		// implemented (Phase 2)
	case "token_type":
		// implemented (faceted) — tokens metric only (validated below)
	default:
		http.Error(w, "unknown group: "+group, http.StatusBadRequest)
		return false
	}
	return true
}

// historyMetricAndGroup derives the measured metric and the effective
// stacking axis from chart and the requested group. chart picks the measured
// metric for tokens/co2; models/providers/agents are presets that pin the
// stacking axis to that dimension (one-click "cost by X"; agents is
// reconstructed per project only), overriding whatever ?group= requested.
func historyMetricAndGroup(chart, group string) (metric, effectiveGroup string) {
	switch chart {
	case "tokens":
		return "tokens", group
	case "co2":
		return "co2", group
	case "models":
		return "cost", "model"
	case "providers":
		return "cost", "provider"
	case "agents":
		return "cost", "project"
	case "state":
		// Same project-only limitation as agents: recordings carry no
		// branch/provider/model axis. Metric is unused for chart=state (it
		// never touches CostTracker), "cost" is just a harmless placeholder.
		return "cost", "project"
	default:
		return "cost", group
	}
}

// historyGroupMetricMismatch reports the one invalid chart/group combination:
// token_type is inherently a token concept — it can't slice cost (we store no
// per-token-type cost on disk).
func historyGroupMetricMismatch(group, metric string) bool {
	return group == "token_type" && metric != "tokens"
}

// historyScopeEcho renders a parsed ?scope= drilldown back into its
// "field:value" wire form, or "" when there is no active scope.
func historyScopeEcho(field, value string) string {
	if field == "" {
		return ""
	}
	return field + ":" + value
}

// writeHistoryJSON encodes v as a history endpoint's JSON response body.
func writeHistoryJSON(w http.ResponseWriter, v any) {
	w.Header().Set(headerContentType, contentTypeJSON)
	json.NewEncoder(w).Encode(v)
}

// serveHistoryAgentsChart serves chart=agents: a concurrent-agents series
// reconstructed from lifecycle recordings via ConcurrencyReader.AgentsSeries
// (Phase 3, #751). A nil reader or an unresolved recordings dir yields an
// empty-but-valid payload rather than an error.
func serveHistoryAgentsChart(w http.ResponseWriter, concurrency outbound.ConcurrencyReader, rangeKey, scopeEcho string, query outbound.SeriesQuery) {
	var cr *outbound.ConcurrencyResult
	if concurrency != nil {
		c, err := concurrency.AgentsSeries(query)
		if err != nil {
			http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
			return
		}
		cr = c
	}
	if cr == nil {
		// No reader (recordings dir unresolved): empty-but-valid payload.
		cr = &outbound.ConcurrencyResult{Start: query.Start, End: query.End, BucketSeconds: query.BucketSeconds, BucketStarts: []int64{}, ByKey: map[string][]float64{}, PeakByKey: map[string]float64{}}
	}
	writeHistoryJSON(w, buildAgentsResponse(rangeKey, scopeEcho, cr))
}

// serveHistoryStateChart serves chart=state (#981): a per-project,
// per-state (working/waiting/ready) series reconstructed from lifecycle
// recordings via ConcurrencyReader.StateSeries — AgentsSeries' per-state
// counterpart. A nil reader or an unresolved recordings dir yields an
// empty-but-valid payload rather than an error, mirroring
// serveHistoryAgentsChart.
func serveHistoryStateChart(w http.ResponseWriter, concurrency outbound.ConcurrencyReader, rangeKey, scopeEcho string, query outbound.SeriesQuery) {
	var sr *outbound.StateSeriesResult
	if concurrency != nil {
		s, err := concurrency.StateSeries(query)
		if err != nil {
			http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
			return
		}
		sr = s
	}
	if sr == nil {
		sr = &outbound.StateSeriesResult{
			Start: query.Start, End: query.End, BucketSeconds: query.BucketSeconds,
			BucketStarts: []int64{},
			ByState: map[string]map[string][]float64{
				session.StateWorking: {}, session.StateWaiting: {}, session.StateReady: {},
			},
		}
	}
	writeHistoryJSON(w, buildStateResponse(rangeKey, scopeEcho, sr))
}

// historyCrossFilters resolves the orthogonal ?project=/?provider=/?token_type=
// cross-filters (comma-separated, multi-value). The active group dimension is
// never filtered — drop whichever matches it — so a dimension is never both a
// stacking axis and a filter. ok is false for an invalid ?token_type=, which
// the caller turns into a 400.
func historyCrossFilters(q url.Values, group string) (projects, providers, tokenTypes []string, ok bool) {
	projects = parseCSVParam(q.Get("project"))
	providers = parseCSVParam(q.Get("provider"))
	tokenTypes, ok = parseTokenTypeFilter(q.Get("token_type"))
	if !ok {
		return nil, nil, nil, false
	}
	switch group {
	case "project":
		projects = nil
	case "provider":
		providers = nil
	case "token_type":
		tokenTypes = nil
	}
	return projects, providers, tokenTypes, true
}

// fetchHistoryCostSeries resolves the cost series for the cost/tokens/co2/
// models/providers charts. A nil tracker (init failed) yields an
// empty-but-valid payload so the dashboard renders cleanly instead of
// erroring; ok is false only when a present tracker's query itself failed.
func fetchHistoryCostSeries(tracker outbound.CostTracker, query outbound.SeriesQuery) (series *outbound.CostSeriesResult, ok bool) {
	if tracker == nil {
		return &outbound.CostSeriesResult{Start: query.Start, End: query.End, BucketSeconds: query.BucketSeconds, BucketStarts: []int64{}, ByKey: map[string][]float64{}, Totals: map[string]float64{}}, true
	}
	s, err := tracker.CostSeries(query)
	if err != nil {
		return nil, false
	}
	return s, true
}

// handleGetHistory serves GET /api/v1/history?range=&chart=&group=&start=&end=
// &bucket=&forecast=&forecast_buckets=. Range is a trailing window
// (day|week|month|year), a calendar shorthand (this-month), or an explicit
// start&end (unix seconds). Bucket granularity is downsampled at read time.
// historyQuery is the validated, defaulted request shape handleGetHistory's
// callers (the yield/agents/cost-series branches) all build their response
// from. Assembled by resolveHistoryQuery, which owns every early-exit
// validation so handleGetHistory's own body stays a flat dispatch.
type historyQuery struct {
	chart       string
	group       string
	metric      string
	scopeEcho   string
	rangeKey    string
	start, end  int64
	seriesQuery outbound.SeriesQuery
}

// resolveHistoryQuery parses and validates handleGetHistory's query params,
// writing the appropriate 400 response and returning ok=false on the first
// invalid one.
func resolveHistoryQuery(w http.ResponseWriter, q url.Values) (historyQuery, bool) {
	chart := q.Get("chart")
	if chart == "" {
		chart = "cost"
	}
	if !historyChartKnown(w, chart) {
		return historyQuery{}, false
	}

	group := q.Get("group")
	if group == "" {
		group = "project"
	}
	if !historyGroupKnown(w, group) {
		return historyQuery{}, false
	}

	metric, group := historyMetricAndGroup(chart, group)
	if historyGroupMetricMismatch(group, metric) {
		http.Error(w, "group=token_type requires chart=tokens", http.StatusBadRequest)
		return historyQuery{}, false
	}

	scopeField, scopeValue := parseHistoryScope(q.Get("scope"))
	scopeEcho := historyScopeEcho(scopeField, scopeValue)

	// chart=state (#981) resolves its window from a named ?granularity=
	// zoom-level instead of the usual ?range=/?bucket= pair — the granularity
	// picks both the bucket width and the trailing window at once.
	var rangeKey string
	var start, end, bucketSeconds int64
	if chart == "state" {
		granularity := q.Get("granularity")
		if granularity == "" {
			granularity = "24h"
		}
		bs, s, e, gok := resolveHistoryGranularity(granularity)
		if !gok {
			http.Error(w, "invalid granularity: use 1m|10m|60m|8h|24h|7d|1mo|6mo|1y", http.StatusBadRequest)
			return historyQuery{}, false
		}
		rangeKey, start, end, bucketSeconds = granularity, s, e, bs
	} else {
		rk, s, e, ok := resolveHistoryRange(q)
		if !ok {
			http.Error(w, "invalid range: use range=day|week|month|year|this-month or start&end (unix seconds)", http.StatusBadRequest)
			return historyQuery{}, false
		}
		rangeKey, start, end = rk, s, e
		bucketSeconds = historyBucketSeconds(q, end-start)
	}

	seriesQuery := outbound.SeriesQuery{
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		Group:         group,
		ScopeField:    scopeField,
		ScopeValue:    scopeValue,
	}

	return historyQuery{
		chart:       chart,
		group:       group,
		metric:      metric,
		scopeEcho:   scopeEcho,
		rangeKey:    rangeKey,
		start:       start,
		end:         end,
		seriesQuery: seriesQuery,
	}, true
}

func handleGetHistory(tracker outbound.CostTracker, sessions historySessionLister, concurrency outbound.ConcurrencyReader, git historyGitReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		hq, ok := resolveHistoryQuery(w, q)
		if !ok {
			return
		}

		// Yield is a per-project aggregate over completed sessions, not a cost
		// time series — handle it before the cost-tracker path (#373).
		if hq.chart == "yield" {
			writeHistoryJSON(w, buildYieldResponse(hq.rangeKey, hq.group, hq.start, hq.end, sessions))
			return
		}

		// DORA metrics are a per-project period summary, not a cost time
		// series — handle it before the cost-tracker path (#951).
		if hq.chart == "dora" {
			serveHistoryDoraChart(w, git, sessions, q.Get("project"), hq.rangeKey, hq.start, hq.end)
			return
		}

		if hq.chart == "agents" {
			serveHistoryAgentsChart(w, concurrency, hq.rangeKey, hq.scopeEcho, hq.seriesQuery)
			return
		}

		if hq.chart == "state" {
			serveHistoryStateChart(w, concurrency, hq.rangeKey, hq.scopeEcho, hq.seriesQuery)
			return
		}

		projects, providers, tokenTypes, ok := historyCrossFilters(q, hq.group)
		if !ok {
			http.Error(w, "invalid token_type: use input|output|cache_read|cache_creation", http.StatusBadRequest)
			return
		}
		hq.seriesQuery.Metric = hq.metric
		hq.seriesQuery.Projects = projects
		hq.seriesQuery.Providers = providers
		hq.seriesQuery.TokenTypes = tokenTypes

		series, ok := fetchHistoryCostSeries(tracker, hq.seriesQuery)
		if !ok {
			http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
			return
		}

		writeHistoryJSON(w, buildHistoryResponse(hq.rangeKey, hq.chart, hq.group, hq.scopeEcho, series, q))
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

// historyGranularitySpec pairs a named granularity's bucket width with the
// number of buckets its default window shows — the "zoom level" for
// chart=state's activity matrix (#981): picking a granularity changes both
// the bucket width and the visible span at once (e.g. "1 min" buckets show
// the last 45 minutes, "1 year" buckets show the last 8 years).
type historyGranularitySpec struct {
	bucketSeconds int64
	buckets       int64
}

// historyGranularitySpecs is chart=state's only bucket-width source — unlike
// every other chart (downsampled by span via historyBucketSeconds), the
// activity matrix always resolves both bucket width and window from a single
// named step. The month/6-month/year entries use an averaged bucket width
// (30/182/365 days) rather than true calendar boundaries: the shared series
// pipeline assumes a uniform bucket stride (BucketStarts is a fixed-step
// loop — see StateSeries/AgentsSeries/CostSeries), and switching that to
// variable-width calendar buckets would be a cross-cutting change touching
// every chart, not just this one. Tracked as a known approximation — see the
// feature issue's open questions.
var historyGranularitySpecs = map[string]historyGranularitySpec{
	"1m":  {60, 45},
	"10m": {600, 48},
	"60m": {3600, 24},
	"8h":  {8 * 3600, 21},
	"24h": {86400, 30},
	"7d":  {7 * 86400, 20},
	"1mo": {30 * 86400, 18},
	"6mo": {182 * 86400, 10},
	"1y":  {365 * 86400, 8},
}

// resolveHistoryGranularity resolves a chart=state ?granularity= value into a
// bucket width and a trailing [start, now] window — the default window for
// that granularity's zoom level. ok is false for an unrecognized granularity.
func resolveHistoryGranularity(granularity string) (bucketSeconds, start, end int64, ok bool) {
	spec, known := historyGranularitySpecs[granularity]
	if !known {
		return 0, 0, 0, false
	}
	end = time.Now().Unix()
	start = end - spec.bucketSeconds*spec.buckets
	return spec.bucketSeconds, start, end, true
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

// sortKeysByValueDesc orders a group-key → value map's keys deterministically:
// value desc, then name — the ordering every history/agents chart uses for its
// series and top-contributors list.
func sortKeysByValueDesc(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if values[keys[i]] != values[keys[j]] {
			return values[keys[i]] > values[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// appendSparsePoints appends one [{ts,project,value}] point per non-zero
// bucket, in key order, to dest — the sparse series shape both the cost and
// agents charts render (zero buckets are omitted rather than drawn as gaps).
func appendSparsePoints(dest []historyPoint, keys []string, byKey map[string][]float64, bucketStarts []int64) []historyPoint {
	for _, k := range keys {
		for i, v := range byKey[k] {
			if i >= len(bucketStarts) {
				break
			}
			if v != 0 {
				dest = append(dest, historyPoint{TS: bucketStarts[i], Project: k, Value: v})
			}
		}
	}
	return dest
}

// topHistoryContributors takes the first limit keys (already ordered by
// sortKeysByValueDesc) and renders them as the side panel's ranked list.
func topHistoryContributors(keys []string, values map[string]float64, limit int) []historyContributor {
	out := []historyContributor{}
	for i, k := range keys {
		if i >= limit {
			break
		}
		out = append(out, historyContributor{Label: k, Value: values[k]})
	}
	return out
}

// historyShouldForecast reports whether buildHistoryResponse should attach a
// linear forecast. Forecast projects USD spend; it isn't meaningful for token
// counts, and compounding a linear projection on top of an already-modeled
// CO2e estimate would overstate precision it doesn't have.
func historyShouldForecast(chart string, q url.Values, total float64, bucketCount int) bool {
	return chart != "tokens" && chart != "co2" && historyForecastEnabled(q) && total > 0 && bucketCount > 0
}

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

	keys := sortKeysByValueDesc(s.Totals)
	for _, k := range keys {
		resp.Total += s.Totals[k]
	}
	resp.Series = appendSparsePoints(resp.Series, keys, s.ByKey, s.BucketStarts)
	resp.TopContributors = topHistoryContributors(keys, s.Totals, 5)

	if s.TokenSplit != nil {
		resp.TokenSplit = &historyTokenSplit{Input: s.TokenSplit.Input, Output: s.TokenSplit.Output, Cache: s.TokenSplit.Cache}
	}

	if historyShouldForecast(chart, q, resp.Total, len(s.BucketStarts)) {
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
	resolveUnknownConcurrencyProject(c)

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

	keys := sortKeysByValueDesc(c.PeakByKey)
	resp.Series = appendSparsePoints(resp.Series, keys, c.ByKey, c.BucketStarts)
	resp.TopContributors = topHistoryContributors(keys, c.PeakByKey, 5)
	return resp
}

// historyStateProjectLimit caps chart=state's project rows: unlike the cost
// chart's separate top-contributors side list, a state-chart project IS a
// rendered grid row, so only the busiest N are worth showing — years of
// one-off worktree sessions would otherwise render as an unbounded, mostly
// stale row list (issue #1046).
const historyStateProjectLimit = 8

// buildStateResponse flattens a per-state concurrency reconstruction into the
// activity-matrix envelope (#981). Project row order is busiest-first, by
// each project's total count summed across every state and bucket — the same
// "rank by what the reader cares about" convention sortKeysByValueDesc gives
// every other chart's top-contributors list. Rows are capped to the busiest
// historyStateProjectLimit and ByState is pruned to match (#1046).
func buildStateResponse(rangeKey, scope string, s *outbound.StateSeriesResult) historyStateResponse {
	totals := map[string]float64{}
	for _, byProject := range s.ByState {
		for project, vals := range byProject {
			for _, v := range vals {
				totals[project] += v
			}
		}
	}
	resolveUnknownStateProject(s, totals)

	projects := sortKeysByValueDesc(totals)
	if len(projects) > historyStateProjectLimit {
		projects = projects[:historyStateProjectLimit]
	}

	return historyStateResponse{
		Range:         rangeKey,
		Chart:         "state",
		Group:         "project",
		Start:         s.Start,
		End:           s.End,
		BucketSeconds: s.BucketSeconds,
		BucketStarts:  s.BucketStarts,
		Projects:      projects,
		ByState:       pruneStateProjects(s.ByState, projects),
		Concurrency:   &historyConcurrency{Peak: s.Peak, Average: s.Average, Current: s.Current},
		Scope:         scope,
	}
}

// pruneStateProjects drops every by-state project entry outside the capped
// row set kept (all three canonical state keys are always preserved, even if
// their pruned sub-map ends up empty) — the response shouldn't carry
// per-bucket series for rows the client will never draw (#1046).
func pruneStateProjects(byState map[string]map[string][]float64, keep []string) map[string]map[string][]float64 {
	keepSet := make(map[string]bool, len(keep))
	for _, p := range keep {
		keepSet[p] = true
	}
	out := make(map[string]map[string][]float64, len(byState))
	for state, byProject := range byState {
		pruned := make(map[string][]float64, len(keep))
		for project, vals := range byProject {
			if keepSet[project] {
				pruned[project] = vals
			}
		}
		out[state] = pruned
	}
	return out
}

// historyUnknownShareKeep is the one policy decision every chart's "unknown"/
// CWD-less/keyless bucket shares: surfaced only when its value is at least
// historyUnknownMinShare of the window's grand total, dropped otherwise as a
// misleading sliver. Factored out so resolveUnknownBucket/
// resolveUnknownConcurrencyProject/resolveUnknownStateProject each implement
// only their own map shape's mutation, not three copies of this arithmetic.
func historyUnknownShareKeep(value, grand float64) bool {
	return grand > 0 && value/grand >= historyUnknownMinShare
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
	if historyUnknownShareKeep(uTotal, grand) {
		s.Totals[historyUnknownLabel] = uTotal
		if uSeries != nil {
			s.ByKey[historyUnknownLabel] = uSeries
		}
	}
}

// resolveUnknownConcurrencyProject is resolveUnknownBucket's chart=agents
// counterpart, over ConcurrencyResult's ByKey/PeakByKey shape — both keyed by
// project, unlike CostSeriesResult's single Totals/ByKey pair (#1046).
func resolveUnknownConcurrencyProject(c *outbound.ConcurrencyResult) {
	uPeak, ok := c.PeakByKey[""]
	if !ok {
		return
	}
	grand := 0.0
	for _, v := range c.PeakByKey {
		grand += v
	}
	uSeries := c.ByKey[""]
	delete(c.PeakByKey, "")
	delete(c.ByKey, "")
	if historyUnknownShareKeep(uPeak, grand) {
		c.PeakByKey[historyUnknownLabel] = uPeak
		if uSeries != nil {
			c.ByKey[historyUnknownLabel] = uSeries
		}
	}
}

// resolveUnknownStateProject is resolveUnknownBucket's chart=state
// counterpart: totals is buildStateResponse's already-computed per-project
// ranking sum (mutated in place); s.ByState's three per-state sub-maps are
// mutated to match — deleting the "" project entirely, or relabeling it to
// historyUnknownLabel in every state that has one, per the same ≥10%-share
// rule (#1046).
func resolveUnknownStateProject(s *outbound.StateSeriesResult, totals map[string]float64) {
	uTotal, ok := totals[""]
	if !ok {
		return
	}
	grand := 0.0
	for _, v := range totals {
		grand += v
	}
	keep := historyUnknownShareKeep(uTotal, grand)
	delete(totals, "")
	if keep {
		totals[historyUnknownLabel] = uTotal
	}
	for _, byProject := range s.ByState {
		vals, ok := byProject[""]
		if !ok {
			continue
		}
		delete(byProject, "")
		if keep {
			byProject[historyUnknownLabel] = vals
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

	byProject := aggregateYieldBySession(sessions, start, end)

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
		resp.Projects = append(resp.Projects, yieldProjectRow(p, a))
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

// yieldAgg accumulates one project's productive/reverted/unknown spend while
// aggregateYieldBySession walks completed sessions.
type yieldAgg struct {
	productive, reverted, unknown float64
	revertedCount                 int
}

// aggregateYieldBySession folds completed (ready) sessions within [start,end)
// into per-project productive/reverted/unknown spend, the core windowing and
// bucketing step of buildYieldResponse. Sessions are windowed by UpdatedAt —
// their completion time — so a revert detected later never moves a session
// into a newer window. Only sessions that have gone ready (non-empty
// YieldState) are counted; spend from sessions still in flight is excluded.
func aggregateYieldBySession(sessions []*session.SessionState, start, end int64) map[string]*yieldAgg {
	byProject := make(map[string]*yieldAgg)
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
			a = &yieldAgg{}
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
	return byProject
}

// yieldProjectRow builds one project's row of the chart=yield response —
// unknown (non-git) spend is reported separately and kept out of the ratio's
// denominator.
func yieldProjectRow(project string, a *yieldAgg) historyYieldProject {
	total := a.productive + a.reverted
	y := 0.0
	if total > 0 {
		y = a.productive / total
	}
	return historyYieldProject{
		Project:        project,
		ProductiveCost: a.productive,
		RevertedCost:   a.reverted,
		UnknownCost:    a.unknown,
		TotalCost:      total,
		Yield:          y,
		RevertedCount:  a.revertedCount,
	}
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
		w.Header().Set(headerContentType, contentTypeJSON)
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
		w.Header().Set(headerContentType, contentTypeJSON)
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
	w.Header().Set(headerContentType, contentTypeJSON)
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
			http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
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

		w.Header().Set(headerContentType, contentTypeJSON)
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
		w.Header().Set(headerContentType, "application/gzip")
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
