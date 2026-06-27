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

// History tab (issue #369). Phase 1 serves chart=cost grouped by project only,
// computed from the cost snapshot files via CostTracker.CostSeries. The other
// chart types and groups are scaffolded in the UI but return 501 here with a
// phase hint so the frontend can disable them honestly.

type historyPoint struct {
	TS      int64   `json:"ts"`
	Project string  `json:"project"`
	Value   float64 `json:"value"`
}

type historyContributor struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
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
	Group           string               `json:"group"`
	Start           int64                `json:"start"`
	End             int64                `json:"end"`
	BucketSeconds   int64                `json:"bucket_seconds"`
	BucketStarts    []int64              `json:"bucket_starts"`
	Total           float64              `json:"total"`
	Series          []historyPoint       `json:"series"`
	TopContributors []historyContributor `json:"top_contributors"`
	Forecast        *historyForecast     `json:"forecast,omitempty"`
}

// handleGetHistory serves GET /api/v1/history?range=&chart=&group=&start=&end=
// &bucket=&forecast=&forecast_buckets=. Range is a trailing window
// (day|week|month|year), a calendar shorthand (this-month), or an explicit
// start&end (unix seconds). Bucket granularity is downsampled at read time.
func handleGetHistory(tracker outbound.CostTracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		chart := q.Get("chart")
		if chart == "" {
			chart = "cost"
		}
		switch chart {
		case "cost":
			// implemented
		case "tokens", "models", "providers":
			writeHistoryNotImplemented(w, "chart="+chart, 2)
			return
		case "agents":
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
		case "project":
			// implemented
		case "branch", "provider", "model", "session":
			writeHistoryNotImplemented(w, "group="+group, 2)
			return
		default:
			http.Error(w, "unknown group: "+group, http.StatusBadRequest)
			return
		}

		rangeKey, start, end, ok := resolveHistoryRange(q)
		if !ok {
			http.Error(w, "invalid range: use range=day|week|month|year|this-month or start&end (unix seconds)", http.StatusBadRequest)
			return
		}
		bucketSeconds := historyBucketSeconds(q, end-start)

		var series *outbound.CostSeriesResult
		if tracker != nil {
			s, err := tracker.CostSeries(start, end, bucketSeconds)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			series = s
		}
		if series == nil {
			// No tracker (init failed): respond with an empty-but-valid payload
			// so the dashboard renders cleanly instead of erroring.
			series = &outbound.CostSeriesResult{Start: start, End: end, BucketSeconds: bucketSeconds, BucketStarts: []int64{}, ByProject: map[string][]float64{}, Totals: map[string]float64{}}
		}

		resp := buildHistoryResponse(rangeKey, chart, group, series, q)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
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
	switch rk {
	case "day", "week", "month", "year":
		return rk, now.Unix() - costTimeframeSeconds[rk], now.Unix(), true
	case "this-month":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return rk, first.Unix(), now.Unix(), true
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

// buildHistoryResponse flattens the per-project series into the response
// envelope: a sparse [{ts,project,value}] series (zero buckets omitted),
// per-project top contributors, and an optional linear forecast over the
// grand (all-project) per-bucket total.
func buildHistoryResponse(rangeKey, chart, group string, s *outbound.CostSeriesResult, q url.Values) historyResponse {
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
	}
	if resp.BucketStarts == nil {
		resp.BucketStarts = []int64{}
	}

	// Deterministic project order: total desc, then name.
	projects := make([]string, 0, len(s.Totals))
	for p := range s.Totals {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		if s.Totals[projects[i]] != s.Totals[projects[j]] {
			return s.Totals[projects[i]] > s.Totals[projects[j]]
		}
		return projects[i] < projects[j]
	})

	grand := make([]float64, len(s.BucketStarts))
	for _, p := range projects {
		resp.Total += s.Totals[p]
		for i, v := range s.ByProject[p] {
			if i >= len(s.BucketStarts) {
				break
			}
			grand[i] += v
			if v != 0 {
				resp.Series = append(resp.Series, historyPoint{TS: s.BucketStarts[i], Project: p, Value: v})
			}
		}
	}
	for i, p := range projects {
		if i >= 5 {
			break
		}
		resp.TopContributors = append(resp.TopContributors, historyContributor{Label: p, Value: s.Totals[p]})
	}

	if historyForecastEnabled(q) && resp.Total > 0 && len(grand) > 0 {
		resp.Forecast = computeLinearForecast(grand, s.BucketStarts, s.BucketSeconds, resp.Total, historyForecastBuckets(q, len(grand)))
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

// computeLinearForecast fits y = a + b·x (least squares) over the per-bucket
// grand-total series and projects `horizon` future buckets. Projected is the
// current total plus the (non-negative) projected future spend; basis is
// "linear" so the UI can label it an estimate.
func computeLinearForecast(grand []float64, bucketStarts []int64, bucketSeconds int64, currentTotal float64, horizon int) *historyForecast {
	n := len(grand)
	var sx, sy, sxx, sxy float64
	for i, y := range grand {
		x := float64(i)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	fn := float64(n)
	var a, b float64
	if denom := fn*sxx - sx*sx; denom != 0 {
		b = (fn*sxy - sx*sy) / denom
		a = (sy - b*sx) / fn
	} else if fn > 0 {
		a = sy / fn
	}

	fc := &historyForecast{Basis: "linear", HorizonBuckets: horizon, Series: []historyForecastPoint{}}
	var lastTS int64
	if n > 0 {
		lastTS = bucketStarts[n-1]
	}
	var future float64
	for k := 1; k <= horizon; k++ {
		y := a + b*float64(n-1+k)
		if y < 0 {
			y = 0
		}
		fc.Series = append(fc.Series, historyForecastPoint{TS: lastTS + int64(k)*bucketSeconds, Value: y})
		future += y
	}
	fc.Projected = currentTotal + future
	if fc.Projected < currentTotal {
		fc.Projected = currentTotal
	}
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
