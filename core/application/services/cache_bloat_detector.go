package services

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/domain/stats"
	"irrlicht/core/ports/outbound"
)

const secondsPerDay = 86400

// maxDeltaWindow bounds the per-session per-turn samples kept for the live
// rolling median, so a marathon session reflects recent behaviour (and memory
// stays bounded) rather than averaging over its entire lifetime.
const maxDeltaWindow = 50

// snapshotTTLSecs caps how often the detector re-scans the session repository
// for the baseline. The baseline is computed over completed sessions in a
// multi-day window, so it changes slowly — re-scanning on every turn boundary
// of every active session would be needless disk I/O.
const snapshotTTLSecs = 60

// sessionLister is the read-only slice of the SessionRepository that the
// cache-bloat detector needs: the project's completed sessions, for the
// rolling baseline and per-version grouping.
type sessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// cacheBloatRecorder emits the structured cache_bloat_detected lifecycle
// event. Satisfied by outbound.EventRecorder; narrowed so the detector can be
// unit-tested without the full recorder.
type cacheBloatRecorder interface {
	Record(ev lifecycle.Event)
}

// CacheBloatConfig holds the cache-regression detector tunables (issue #374).
type CacheBloatConfig struct {
	BaselineDays       int     // rolling baseline lookback window
	Threshold          float64 // trip multiplier; <= 0 disables the rule
	VersionDeltaTokens int64   // min per-version median delta to attribute
	MinTurns           int     // variance guard: turns before the rule fires
}

// CacheBloatDetector flags a working session whose median cache-creation per
// turn exceeds its project's p25 baseline × threshold, attributes the
// regression to an upstream agent version when the project's history spans two
// versions, and emits a structured event the first time it sees each
// (project, regressing_version) pair within a daemon process lifetime.
//
// It is driven once per processActivity pass via OnActivity and runs entirely
// on the detector's single event-loop goroutine, so its maps need no locking.
type CacheBloatDetector struct {
	lister   sessionLister
	recorder cacheBloatRecorder
	cfg      CacheBloatConfig
	now      func() int64 // injectable clock for tests

	lastDone map[string]bool      // sessionID -> IsAgentDone last pass (rising-edge)
	prevCum  map[string]int64     // sessionID -> CumCacheCreationTokens at last boundary
	deltas   map[string][]float64 // sessionID -> per-turn cache-creation deltas
	fired    map[string]struct{}  // "project|version" -> already emitted this process

	cached   []*session.SessionState // short-TTL snapshot of ListAll
	cachedAt int64                   // unix secs the snapshot was taken
}

// NewCacheBloatDetector builds a detector. recorder may be nil (the glyph still
// fires; only the structured event is suppressed).
func NewCacheBloatDetector(lister sessionLister, recorder cacheBloatRecorder, cfg CacheBloatConfig) *CacheBloatDetector {
	return &CacheBloatDetector{
		lister:   lister,
		recorder: recorder,
		cfg:      cfg,
		now:      func() int64 { return time.Now().Unix() },
		lastDone: map[string]bool{},
		prevCum:  map[string]int64{},
		deltas:   map[string][]float64{},
		fired:    map[string]struct{}{},
	}
}

// OnActivity is called once per processActivity pass. On a turn boundary (a
// rising edge of IsAgentDone) it counts the turn, samples the turn's
// cache-creation delta, and re-evaluates the regression rule against the
// project baseline. A no-op on non-boundary passes and when disabled.
func (c *CacheBloatDetector) OnActivity(state *session.SessionState) {
	if c == nil || c.cfg.Threshold <= 0 || state == nil || state.Metrics == nil {
		return // kill switch, or nothing to measure
	}
	sid := state.SessionID
	done := state.Metrics.IsAgentDone()
	wasDone := c.lastDone[sid]
	c.lastDone[sid] = done
	if !done || wasDone {
		return // still working, or this finished turn was already counted
	}

	// A turn just completed. Count it and sample this turn's cache creation as
	// the delta of the cumulative total since the previous boundary.
	state.Metrics.CompletedTurns++
	cum := state.Metrics.CumCacheCreationTokens
	delta := cum - c.prevCum[sid]
	if delta < 0 {
		delta = 0 // cumulative reset (e.g. /clear) — don't emit a negative turn
	}
	c.prevCum[sid] = cum
	c.deltas[sid] = append(c.deltas[sid], float64(delta))
	if len(c.deltas[sid]) > maxDeltaWindow {
		c.deltas[sid] = c.deltas[sid][len(c.deltas[sid])-maxDeltaWindow:]
	}

	// Variance guard: need a few turns before the per-session median is stable.
	if len(c.deltas[sid]) < c.cfg.MinTurns {
		return
	}
	currentMedian, ok := stats.Median(c.deltas[sid])
	if !ok {
		return
	}
	baseline, ok := c.computeBaseline(state.ProjectName, state.Adapter, sid)
	if !ok || baseline <= 0 {
		return // cold start: no comparable history yet, can't judge
	}

	if currentMedian <= baseline*c.cfg.Threshold {
		// Below threshold — clear any prior verdict (re-evaluated each turn).
		state.Metrics.CacheBloat = false
		state.Metrics.CacheBloatPercent = 0
		state.Metrics.CacheBloatTooltip = ""
		state.Metrics.CacheBloatExplanation = ""
		return
	}

	// Regression tripped. Set the glyph and (when possible) name the version.
	state.Metrics.CacheBloat = true
	state.Metrics.CacheBloatPercent = int(math.Round((currentMedian/baseline - 1) * 100))
	tooltip, regressing, prior, deltaTokens := c.attributeVersion(state, currentMedian)
	state.Metrics.CacheBloatTooltip = tooltip
	state.Metrics.CacheBloatExplanation = cacheBloatExplanation(tooltip)

	// Emit the structured event once per (project, regressing_version) pair
	// within this process. regressing is "" when no attribution is possible,
	// which dedupes per project.
	key := state.ProjectName + "|" + regressing
	if _, seen := c.fired[key]; seen {
		return
	}
	c.fired[key] = struct{}{}
	if c.recorder != nil {
		c.recorder.Record(lifecycle.Event{
			Kind:              lifecycle.KindCacheBloatDetected,
			SessionID:         sid,
			Adapter:           state.Adapter,
			Project:           state.ProjectName,
			RegressingVersion: regressing,
			PriorVersion:      prior,
			DeltaTokens:       deltaTokens,
			BaselineMedian:    baseline,
			CurrentMedian:     currentMedian,
		})
	}
}

// snapshot returns the session repository's sessions, cached for snapshotTTLSecs
// so a busy session's per-turn evaluations don't re-scan the repository on
// every turn boundary. Returns nil on a lister error.
func (c *CacheBloatDetector) snapshot() []*session.SessionState {
	if c.lister == nil {
		return nil
	}
	now := c.now()
	if c.cached != nil && now-c.cachedAt < snapshotTTLSecs {
		return c.cached
	}
	all, err := c.lister.ListAll()
	if err != nil {
		return nil
	}
	c.cached, c.cachedAt = all, now
	return all
}

// computeBaseline returns the p25 of "cache creation per completed turn" over
// the project's completed sessions of the same adapter within the lookback
// window, excluding the session under evaluation (excludeID) so a bloated
// session can't raise the baseline it's judged against. ok is false when there
// is no usable history.
func (c *CacheBloatDetector) computeBaseline(project, adapter, excludeID string) (float64, bool) {
	if project == "" {
		return 0, false
	}
	all := c.snapshot()
	cutoff := c.now() - int64(c.cfg.BaselineDays)*secondsPerDay
	var samples []float64
	for _, s := range all {
		if !c.eligible(s, project, adapter, cutoff) || s.SessionID == excludeID {
			continue
		}
		samples = append(samples, perTurnCacheCreation(s))
	}
	return stats.Percentile(samples, 0.25)
}

// attributeVersion groups the project's completed sessions by AgentVersion and,
// when the window spans ≥2 versions whose per-version median cache-creation
// differs by more than VersionDeltaTokens, returns a tooltip naming the
// regressing (current) version. Otherwise it returns empty strings — no false
// attribution.
func (c *CacheBloatDetector) attributeVersion(state *session.SessionState, currentMedian float64) (tooltip, regressing, prior string, deltaTokens int64) {
	newest := state.Metrics.AgentVersion
	if newest == "" {
		return "", "", "", 0
	}
	cutoff := c.now() - int64(c.cfg.BaselineDays)*secondsPerDay
	groups := c.groupByVersion(state, cutoff)
	if len(groups) < 2 {
		return "", "", "", 0 // single version → no attribution
	}

	prior = findPriorVersion(groups, newest)
	if prior == "" {
		return "", "", "", 0
	}

	// Newest-version median: prefer the project's history; fall back to the
	// live session's running median when the newest version has no completed
	// sessions persisted yet.
	newestMedian := medianForVersion(groups, newest, currentMedian)
	priorMedian, ok := stats.Median(groups[prior].samples)
	if !ok {
		return "", "", "", 0
	}

	delta := newestMedian - priorMedian
	if delta <= float64(c.cfg.VersionDeltaTokens) {
		return "", "", "", 0 // versions don't differ enough
	}
	deltaTokens = int64(delta)
	tooltip = fmt.Sprintf("%s %s +%dK cache tokens vs %s", state.Adapter, newest, deltaTokens/1000, prior)
	return tooltip, newest, prior, deltaTokens
}

// versionGroup accumulates one AgentVersion's per-turn cache-creation samples
// and the most recent UpdatedAt seen among its sessions, for attributeVersion.
type versionGroup struct {
	samples  []float64
	lastSeen int64
}

// groupByVersion buckets the project's eligible completed sessions (same
// project/adapter, recent enough, excluding state itself) by AgentVersion.
func (c *CacheBloatDetector) groupByVersion(state *session.SessionState, cutoff int64) map[string]*versionGroup {
	groups := map[string]*versionGroup{}
	for _, s := range c.snapshot() {
		if !c.eligible(s, state.ProjectName, state.Adapter, cutoff) ||
			s.SessionID == state.SessionID || s.Metrics.AgentVersion == "" {
			continue
		}
		g := groups[s.Metrics.AgentVersion]
		if g == nil {
			g = &versionGroup{}
			groups[s.Metrics.AgentVersion] = g
		}
		g.samples = append(g.samples, perTurnCacheCreation(s))
		if s.UpdatedAt > g.lastSeen {
			g.lastSeen = s.UpdatedAt
		}
	}
	return groups
}

// findPriorVersion returns the most-recently-seen version in groups other
// than newest, or "" when no other version is present.
func findPriorVersion(groups map[string]*versionGroup, newest string) string {
	var priorLastSeen int64 = -1
	var prior string
	for v, g := range groups {
		if v == newest {
			continue
		}
		if g.lastSeen > priorLastSeen {
			priorLastSeen, prior = g.lastSeen, v
		}
	}
	return prior
}

// medianForVersion returns the median of version's samples in groups,
// falling back to fallback when the version has no group or no computable
// median.
func medianForVersion(groups map[string]*versionGroup, version string, fallback float64) float64 {
	g := groups[version]
	if g == nil {
		return fallback
	}
	if m, ok := stats.Median(g.samples); ok {
		return m
	}
	return fallback
}

// cacheBloatExplanation composes the CacheBloat badge's longer plain-language
// hover text from its short tooltip (issue #827). Both UIs used to derive
// this string independently from cache_bloat_tooltip; composing it once here
// and shipping it verbatim as CacheBloatExplanation keeps future wording
// tweaks from silently diverging between platforms.
func cacheBloatExplanation(tooltip string) string {
	base := "This session is creating prompt-cache tokens well above normal for this project — it's getting less benefit from caching and costing more per turn."
	attribution := ""
	if tooltip != "" {
		attribution = fmt.Sprintf(" Likely tied to %s.", tooltip)
	}
	causes := " Common causes: an agent update that changed context construction, large or varying pasted content each turn, or frequent context resets (e.g. /clear)."
	return base + attribution + causes
}

// eligible reports whether a stored session counts toward the project's
// baseline / version groups: same project + adapter, recent enough, and with
// enough completed turns to be statistically meaningful.
func (c *CacheBloatDetector) eligible(s *session.SessionState, project, adapter string, cutoff int64) bool {
	return s != nil && s.Metrics != nil &&
		s.ProjectName == project && s.Adapter == adapter &&
		s.UpdatedAt >= cutoff &&
		s.Metrics.CompletedTurns >= c.cfg.MinTurns
}

// perTurnCacheCreation is a completed session's mean cache-creation per turn —
// the per-session statistic the baseline and version groups are built from.
func perTurnCacheCreation(s *session.SessionState) float64 {
	if s.Metrics.CompletedTurns <= 0 {
		return 0
	}
	return float64(s.Metrics.CumCacheCreationTokens) / float64(s.Metrics.CompletedTurns)
}

// LoggerCacheBloatSink writes cache_bloat_detected findings to the structured
// events.log via the Logger port — the always-on sink the ir:agent-releases
// workflow consumes. The event's fields are encoded as a JSON message under
// the cache_bloat_detected event type. Satisfies cacheBloatRecorder.
type LoggerCacheBloatSink struct{ log outbound.Logger }

// NewLoggerCacheBloatSink wraps a Logger as the detector's emission sink.
func NewLoggerCacheBloatSink(log outbound.Logger) *LoggerCacheBloatSink {
	return &LoggerCacheBloatSink{log: log}
}

func (s *LoggerCacheBloatSink) Record(ev lifecycle.Event) {
	if s == nil || s.log == nil {
		return
	}
	payload, err := json.Marshal(struct {
		Project           string  `json:"project,omitempty"`
		Adapter           string  `json:"adapter,omitempty"`
		RegressingVersion string  `json:"regressing_version,omitempty"`
		PriorVersion      string  `json:"prior_version,omitempty"`
		DeltaTokens       int64   `json:"delta_tokens,omitempty"`
		BaselineMedian    float64 `json:"baseline_median,omitempty"`
		CurrentMedian     float64 `json:"current_median,omitempty"`
		SessionID         string  `json:"session_id,omitempty"`
	}{
		Project:           ev.Project,
		Adapter:           ev.Adapter,
		RegressingVersion: ev.RegressingVersion,
		PriorVersion:      ev.PriorVersion,
		DeltaTokens:       ev.DeltaTokens,
		BaselineMedian:    ev.BaselineMedian,
		CurrentMedian:     ev.CurrentMedian,
		SessionID:         ev.SessionID,
	})
	if err != nil {
		return
	}
	s.log.LogInfo(string(lifecycle.KindCacheBloatDetected), ev.SessionID, string(payload))
}
