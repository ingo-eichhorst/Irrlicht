package main

// `irrlicht-ls --herdr-usage` answers herdr's `usage` plugin action (#2061):
// one schema-1 usage document — Factory's contract, mirrored field for field
// in herdrusage_contract_test.go — for the herdr pane named by HERDR_PANE_ID.
//
// herdr hands a plugin action the pane it was invoked for as HERDR_PANE_ID,
// which over the socket API is `context.focused_pane_id` — not necessarily
// the pane a person has focused. This mode reads only that variable; it never
// asks herdr which pane is focused.
//
// Every number it prints is one Irrlicht already holds. Where Irrlicht cannot
// vouch for a number it prints null, and when it knows why, the reason goes
// under `unavailable` keyed by the field's dotted path. A missing count is
// never written as zero.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/daemonaddr"
)

// usageSchema is the contract version this mode writes.
const usageSchema = 1

// herdrUsageTimeout bounds the one daemon request. Factory gives the whole
// usage read — finding the action, invoking it, polling its log — 5s
// (runtime_herdr.rs USAGE_TIMEOUT at factory c3caaafe4), so the request must
// fail well inside that rather than be cut off by it.
const herdrUsageTimeout = 3 * time.Second

// usageDoc and the types below it are what this mode writes: schema 1.
type usageDoc struct {
	Schema    int            `json:"schema"`
	PaneID    string         `json:"pane_id"`
	SampledAt string         `json:"sampled_at"`
	Sessions  []harnessUsage `json:"sessions"`
}

type harnessUsage struct {
	SessionID      string            `json:"session_id"`
	Adapter        *string           `json:"adapter"`
	Model          *string           `json:"model"`
	Tokens         tokenCounts       `json:"tokens"`
	Cost           usageCost         `json:"cost"`
	ElapsedSeconds *float64          `json:"elapsed_seconds"`
	ActiveSeconds  *float64          `json:"active_seconds"`
	Subagents      []subagentUsage   `json:"subagents"`
	RateLimit      *rateLimit        `json:"rate_limit"`
	Unavailable    map[string]string `json:"unavailable,omitempty"`
}

type tokenCounts struct {
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
}

type usageCost struct {
	USD           *float64 `json:"usd"`
	PricingSource *string  `json:"pricing_source"`
}

type subagentUsage struct {
	SessionID string      `json:"session_id"`
	Tokens    tokenCounts `json:"tokens"`
	Cost      usageCost   `json:"cost"`
}

type rateLimit struct {
	Provider           *string           `json:"provider"`
	PlanType           *string           `json:"plan_type"`
	AttributionQuality *string           `json:"attribution_quality"`
	Windows            []rateLimitWindow `json:"windows"`
}

type rateLimitWindow struct {
	WindowMinutes *int     `json:"window_minutes"`
	UsedPercent   *float64 `json:"used_percent"`
	ResetsAt      *string  `json:"resets_at"`
}

// herdrUsageEnv is what the mode reads from its environment and the clock —
// injected so tests need neither.
type herdrUsageEnv struct {
	paneID     string // HERDR_PANE_ID
	socketPath string // HERDR_SOCKET_PATH, which herdr 0.8.0 also passes
	url        string // the daemon's /api/v1/sessions
	warning    string // daemonaddr.ClientConfigWarning, folded into an error line
	client     *http.Client
	now        func() time.Time
}

// runHerdrUsage writes the usage document to stdout and returns the exit
// code. On any failure it writes exactly one line to stderr and nothing to
// stdout: 1 when the daemon could not answer, 2 when the request cannot be
// answered as asked. No panic escapes it.
func runHerdrUsage(env herdrUsageEnv, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "irrlicht-ls --herdr-usage: internal error: %v\n", r)
			code = 1
		}
	}()
	if env.paneID == "" {
		// Every session outside herdr has an empty pane id, so an empty
		// request would match all of them.
		fmt.Fprintln(stderr, "irrlicht-ls --herdr-usage: HERDR_PANE_ID is not set; herdr sets it for a plugin action")
		return 2
	}
	all, err := fetchSessions(env)
	if err != nil {
		msg := err.Error()
		if env.warning != "" {
			msg += " (" + env.warning + ")"
		}
		fmt.Fprintf(stderr, "irrlicht-ls --herdr-usage: %s\n", oneLine(msg))
		return 1
	}
	doc, err := buildUsageDoc(all, env.paneID, env.socketPath, env.now())
	if err != nil {
		fmt.Fprintf(stderr, "irrlicht-ls --herdr-usage: %s\n", oneLine(err.Error()))
		return 2
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "irrlicht-ls --herdr-usage: %s\n", oneLine(err.Error()))
		return 1
	}
	if _, err := stdout.Write(append(out, '\n')); err != nil {
		return 1
	}
	return 0
}

// defaultHerdrUsageEnv reads the real environment.
func defaultHerdrUsageEnv(getenv func(string) string) herdrUsageEnv {
	return herdrUsageEnv{
		paneID:     getenv("HERDR_PANE_ID"),
		socketPath: getenv("HERDR_SOCKET_PATH"),
		url:        daemonaddr.ClientURL("/api/v1/sessions?include=finished_subagents"),
		warning:    daemonaddr.ClientConfigWarning(),
		client:     &http.Client{Timeout: herdrUsageTimeout},
		now:        time.Now,
	}
}

// oneLine keeps a message to the single stderr line the contract allows.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// fetchSessions reads the daemon's live session list and flattens its group
// tree. The live endpoint, not the on-disk state files, because the handler
// applies the rate-limit inheritance pass the files never see.
func fetchSessions(env herdrUsageEnv) ([]*session.SessionState, error) {
	// #nosec G107 -- the URL is the local daemon's, from daemonaddr.ClientURL.
	resp, err := env.client.Get(env.url) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("daemon unreachable at %s: %v", env.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon at %s answered %s", env.url, resp.Status)
	}
	var payload struct {
		Groups            []*session.AgentGroup      `json:"groups"`
		FinishedSubagents []session.FinishedSubagent `json:"finished_subagents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("unreadable answer from daemon at %s: %v", env.url, err)
	}
	return withFinishedSubagents(flattenGroups(nil, payload.Groups), payload.FinishedSubagents), nil
}

// withFinishedSubagents adds the daemon's records of removed child sessions
// (#2061) to the live list, as sessions with their final usage, so a parent's
// subagents are still found after the daemon has deleted them. A record for a
// session that is still live is skipped: the live session is the newer
// answer, and a subagent is listed once. A record never carries a launcher,
// so it can be a subagent but never a pane's root.
func withFinishedSubagents(live []*session.SessionState, finished []session.FinishedSubagent) []*session.SessionState {
	seen := make(map[string]bool, len(live)+len(finished))
	for _, s := range live {
		seen[s.SessionID] = true
	}
	out := live
	for _, f := range finished {
		if f.SessionID == "" || seen[f.SessionID] {
			continue
		}
		seen[f.SessionID] = true
		out = append(out, &session.SessionState{
			SessionID:       f.SessionID,
			ParentSessionID: f.ParentSessionID,
			Adapter:         f.Adapter,
			FirstSeen:       f.FirstSeen,
			Metrics:         f.Usage,
		})
	}
	return out
}

// flattenGroups appends every session in a dashboard group tree — nested
// groups and agents' children included — to out.
func flattenGroups(out []*session.SessionState, groups []*session.AgentGroup) []*session.SessionState {
	for _, g := range groups {
		if g != nil {
			out = flattenGroups(flattenAgents(out, g.Agents), g.Groups)
		}
	}
	return out
}

func flattenAgents(out []*session.SessionState, agents []*session.Agent) []*session.SessionState {
	for _, a := range agents {
		if a == nil {
			continue
		}
		if a.SessionState != nil {
			out = append(out, a.SessionState)
		}
		out = flattenAgents(out, a.Children)
	}
	return out
}

// buildUsageDoc answers for one pane: every root session running in it, each
// with its child sessions listed as subagents.
func buildUsageDoc(all []*session.SessionState, paneID, socketPath string, now time.Time) (usageDoc, error) {
	matched, err := sessionsInPane(all, paneID, socketPath)
	if err != nil {
		return usageDoc{}, err
	}
	inPane := make(map[string]bool, len(matched))
	for _, s := range matched {
		inPane[s.SessionID] = true
	}
	childrenOf := make(map[string][]*session.SessionState)
	for _, s := range all {
		if s.ParentSessionID != "" {
			childrenOf[s.ParentSessionID] = append(childrenOf[s.ParentSessionID], s)
		}
	}

	var roots []*session.SessionState
	for _, s := range matched {
		// A child whose parent is in this pane is that parent's subagent.
		if s.ParentSessionID == "" || !inPane[s.ParentSessionID] {
			roots = append(roots, s)
		}
	}
	// Deterministic, most recently active first: Factory picks "the first
	// session whose adapter is X" (SessionUsage::session_for_adapter).
	sort.SliceStable(roots, func(i, j int) bool {
		if roots[i].UpdatedAt != roots[j].UpdatedAt {
			return roots[i].UpdatedAt > roots[j].UpdatedAt
		}
		return roots[i].SessionID < roots[j].SessionID
	})

	doc := usageDoc{
		Schema:    usageSchema,
		PaneID:    paneID,
		SampledAt: now.UTC().Format(time.RFC3339),
		Sessions:  []harnessUsage{},
	}
	for _, r := range roots {
		doc.Sessions = append(doc.Sessions, harnessUsageFor(r, descendants(r, childrenOf)))
	}
	return doc, nil
}

// sessionsInPane returns the sessions whose launcher names the pane. Pane ids
// repeat across herdr servers (each server has its own w1:p1), so with a
// socket path the match also requires the session's socket; without one, a
// pane id seen on more than one socket is refused rather than guessed.
func sessionsInPane(all []*session.SessionState, paneID, socketPath string) ([]*session.SessionState, error) {
	want := cleanSocket(socketPath)
	var matched []*session.SessionState
	sockets := map[string]bool{}
	for _, s := range all {
		l := s.Launcher
		if l == nil || l.HerdrPaneID != paneID {
			continue
		}
		have := cleanSocket(l.HerdrSocketPath)
		if want != "" && have != want {
			continue
		}
		if have != "" {
			sockets[have] = true
		}
		matched = append(matched, s)
	}
	if want == "" && len(sockets) > 1 {
		names := make([]string, 0, len(sockets))
		for s := range sockets {
			names = append(names, s)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("ambiguous pane %s: sessions on %d herdr servers use that pane id (%s); "+
			"set HERDR_SOCKET_PATH to choose one", paneID, len(names), strings.Join(names, ", "))
	}
	return matched, nil
}

func cleanSocket(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// descendants returns every session below root, depth first, each once.
func descendants(root *session.SessionState, childrenOf map[string][]*session.SessionState) []*session.SessionState {
	var out []*session.SessionState
	seen := map[string]bool{root.SessionID: true}
	var walk func(id string)
	walk = func(id string) {
		kids := append([]*session.SessionState(nil), childrenOf[id]...)
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].FirstSeen != kids[j].FirstSeen {
				return kids[i].FirstSeen < kids[j].FirstSeen
			}
			return kids[i].SessionID < kids[j].SessionID
		})
		for _, k := range kids {
			if seen[k.SessionID] {
				continue
			}
			seen[k.SessionID] = true
			out = append(out, k)
			walk(k.SessionID)
		}
	}
	walk(root.SessionID)
	return out
}

// harnessUsageFor maps one root session and its descendants to schema 1.
func harnessUsageFor(s *session.SessionState, subs []*session.SessionState) harnessUsage {
	u := harnessUsage{
		SessionID:   s.SessionID,
		Adapter:     strPtr(s.Adapter),
		Model:       modelOf(s),
		Unavailable: map[string]string{},
	}
	if u.Model == nil {
		u.Unavailable["model"] = "no model name observed"
	}
	m := s.Metrics
	adapter := adapterOrUnnamed(s.Adapter)

	// Children listed beside their parent must not be counted twice. Only an
	// adapter that has established its parents exclude their children's usage
	// may show a parent's own counts next to them; for any other, the counts
	// of every listed session that has listed children — the root included —
	// are withheld.
	excludes := m != nil && m.UsageCoverage != nil && m.UsageCoverage.ExcludesSubagents
	u.Tokens, u.Cost = accountUsage(m, adapter, "", u.Unavailable)
	if len(subs) > 0 && !excludes {
		u.Tokens, u.Cost = withheld(adapter, "", u.Unavailable)
	}
	u.Subagents = subagentsOf(subs, excludes, u.Unavailable)

	u.ElapsedSeconds = elapsedOf(m, u.Unavailable)
	u.Unavailable["active_seconds"] = "Irrlicht does not record per-session active time"
	if m != nil && m.RateLimit != nil {
		u.RateLimit = rateLimitOf(m.RateLimit)
	} else {
		u.Unavailable["rate_limit"] = "no rate-limit snapshot observed for this session"
	}
	return u
}

// subagentsOf maps a root's descendants to subagent entries, recording why
// each null is null under "subagents.<i>." in unavailable.
func subagentsOf(subs []*session.SessionState, excludes bool, unavailable map[string]string) []subagentUsage {
	hasKids := map[string]bool{}
	for _, c := range subs {
		hasKids[c.ParentSessionID] = true
	}
	out := make([]subagentUsage, 0, len(subs))
	for i, c := range subs {
		prefix := fmt.Sprintf("subagents.%d.", i)
		adapter := adapterOrUnnamed(c.Adapter)
		tokens, cost := accountUsage(c.Metrics, adapter, prefix, unavailable)
		if hasKids[c.SessionID] && !excludes {
			tokens, cost = withheld(adapter, prefix, unavailable)
		}
		out = append(out, subagentUsage{SessionID: c.SessionID, Tokens: tokens, Cost: cost})
	}
	return out
}

// withheld nulls one session's counts and cost because its adapter has not
// established that they exclude its listed subagents.
func withheld(adapter, prefix string, unavailable map[string]string) (tokenCounts, usageCost) {
	reason := fmt.Sprintf("the %s adapter has not established that a parent's counts exclude its listed subagents", adapter)
	for _, f := range tokenFields {
		unavailable[prefix+"tokens."+f.name] = reason
	}
	unavailable[prefix+"cost.usd"] = reason
	return tokenCounts{}, usageCost{}
}

// elapsedOf returns the session's elapsed seconds, or nil with a reason.
// SessionMetrics.ElapsedSeconds reads 0 whenever a metrics path has measured
// no span yet — the tailer's computeMetrics with no event, one event, or
// events under a second apart; hermes' fold until the session row has an
// ended_at — so a 0 is never printed as a measured zero.
func elapsedOf(m *session.SessionMetrics, unavailable map[string]string) *float64 {
	switch {
	case m == nil:
		unavailable["elapsed_seconds"] = "no usage observed for this session yet"
	case m.ElapsedSeconds <= 0:
		unavailable["elapsed_seconds"] = "no elapsed time measured for this session yet"
	default:
		v := float64(m.ElapsedSeconds)
		return &v
	}
	return nil
}

func adapterOrUnnamed(a string) string {
	if a == "" {
		return "unnamed"
	}
	return a
}

func modelOf(s *session.SessionState) *string {
	if m := s.Metrics; m != nil && m.ModelName != "" && m.ModelName != "unknown" {
		return strPtr(m.ModelName)
	}
	if s.Model != "" && s.Model != "unknown" {
		return strPtr(s.Model)
	}
	return nil
}

// tokenFields ties each schema-1 token field to its token type and the
// SessionMetrics counter behind it.
var tokenFields = []struct {
	name  string
	typ   session.TokenType
	value func(*session.SessionMetrics) int64
	slot  func(*tokenCounts) **int64
}{
	{"input", session.TokenInput, func(m *session.SessionMetrics) int64 { return m.CumInputTokens }, func(t *tokenCounts) **int64 { return &t.Input }},
	{"output", session.TokenOutput, func(m *session.SessionMetrics) int64 { return m.CumOutputTokens }, func(t *tokenCounts) **int64 { return &t.Output }},
	{"cache_read", session.TokenCacheRead, func(m *session.SessionMetrics) int64 { return m.CumCacheReadTokens }, func(t *tokenCounts) **int64 { return &t.CacheRead }},
	{"cache_write", session.TokenCacheWrite, func(m *session.SessionMetrics) int64 { return m.CumCacheCreationTokens }, func(t *tokenCounts) **int64 { return &t.CacheWrite }},
}

// accountUsage maps one session's metrics to its token counts and cost,
// recording why each null is null under prefix in unavailable.
func accountUsage(m *session.SessionMetrics, adapter, prefix string, unavailable map[string]string) (tokenCounts, usageCost) {
	if m == nil {
		for _, f := range tokenFields {
			unavailable[prefix+"tokens."+f.name] = "no usage observed for this session yet"
		}
		unavailable[prefix+"cost.usd"] = "no usage observed for this session yet"
		return tokenCounts{}, usageCost{}
	}
	return tokensOf(m, adapter, prefix, unavailable), costOf(m, adapter, prefix+"cost.usd", unavailable)
}

// tokensOf prints a counted token type, and nulls one the adapter does not
// declare, does not read, or whose turns went uncounted.
func tokensOf(m *session.SessionMetrics, adapter, prefix string, unavailable map[string]string) tokenCounts {
	var tokens tokenCounts
	cov := m.UsageCoverage
	for _, f := range tokenFields {
		key := prefix + "tokens." + f.name
		switch {
		case cov == nil:
			unavailable[key] = fmt.Sprintf("the %s adapter does not declare which token types it counts", adapter)
		case m.TokensUnobserved:
			unavailable[key] = "some turns reported only a provider cost, and their tokens were not counted"
		case !cov.Observes(f.typ):
			unavailable[key] = fmt.Sprintf("the %s adapter does not read %s tokens from the agent's usage record", adapter, f.name)
		default:
			v := f.value(m)
			*f.slot(&tokens) = &v
		}
	}
	return tokens
}

// costOf prints the cost only when its provenance vouches for it: nothing
// unpriced, and a table price only over a complete set of counted token
// types. key is the cost's dotted path.
func costOf(m *session.SessionMetrics, adapter, key string, unavailable map[string]string) usageCost {
	cov := m.UsageCoverage
	fromTable := session.PricedFromTable(m.PricingSource)
	switch {
	case len(m.UnpricedModels) > 0:
		unavailable[key] = "no price known for " + describeModels(m.UnpricedModels)
	case fromTable && cov == nil:
		unavailable[key] = fmt.Sprintf("priced from token counts the %s adapter does not declare", adapter)
	case fromTable && !cov.ObservesAll():
		unavailable[key] = fmt.Sprintf("priced from only the token types the %s adapter reads", adapter)
	case m.PricingSource != "":
		usd := m.EstimatedCostUSD
		return usageCost{USD: &usd, PricingSource: strPtr(m.PricingSource)}
	case cov != nil && !m.TokensUnobserved && m.EstimatedCostUSD == 0 && countedTokens(m) == 0:
		// A declared adapter that has counted nothing yet: nothing to price.
		zero := 0.0
		return usageCost{USD: &zero}
	default:
		unavailable[key] = "Irrlicht did not record how this cost was formed"
	}
	return usageCost{}
}

func countedTokens(m *session.SessionMetrics) int64 {
	return m.CumInputTokens + m.CumOutputTokens + m.CumCacheReadTokens + m.CumCacheCreationTokens
}

func describeModels(models []string) string {
	named := make([]string, len(models))
	for i, m := range models {
		if m == "" {
			m = "tokens with no model name"
		}
		named[i] = m
	}
	return strings.Join(named, ", ")
}

// rateLimitOf maps a quota snapshot. Provider and attribution quality are the
// snapshot's own stamps — never derived from the adapter's name — and only
// subscription-allowance windows are windows in schema 1's sense.
func rateLimitOf(rl *session.RateLimitSnapshot) *rateLimit {
	out := &rateLimit{
		Provider:           strPtr(rl.Provider),
		PlanType:           strPtr(rl.PlanType),
		AttributionQuality: strPtr(rl.AttributionQuality),
		Windows:            []rateLimitWindow{},
	}
	for i := range rl.Windows {
		w := &rl.Windows[i]
		if !w.IsSubscriptionAllowance() {
			continue
		}
		win := rateLimitWindow{}
		if w.WindowMinutes > 0 {
			minutes := w.WindowMinutes
			win.WindowMinutes = &minutes
		}
		used := w.UsedPercent
		win.UsedPercent = &used
		if w.ResetsAt > 0 {
			win.ResetsAt = strPtr(time.Unix(w.ResetsAt, 0).UTC().Format(time.RFC3339))
		}
		out.Windows = append(out.Windows, win)
	}
	return out
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
