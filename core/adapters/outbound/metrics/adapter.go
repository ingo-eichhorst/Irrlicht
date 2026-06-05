package metrics

import (
	"os"
	"sync"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// lockedTailer wraps a TranscriptTailer with its own mutex so that
// concurrent ComputeMetrics calls for different sessions don't block
// each other — only calls for the same transcript path serialize.
type lockedTailer struct {
	mu sync.Mutex
	t  *tailer.TranscriptTailer
	lp string // path to the session ledger file; empty when ledger dir is unavailable
}

// Adapter implements ports/outbound.MetricsCollector using the transcript-tailer package.
// It caches TranscriptTailer instances per path so that lastOffset-based
// incremental reads work across calls, avoiding re-parsing the full 64KB tail.
//
// For adapters that register a MetricsProvider (e.g. OpenCode with its SQLite
// database), ComputeMetrics delegates to that provider instead of the tailer.
type Adapter struct {
	mu               sync.Mutex // protects the tailers map only
	tailers          map[string]*lockedTailer
	parsers          map[string]agents.ParserFactory
	subagents        map[string]agents.SubagentCounter
	metricsProviders map[string]agents.MetricsProvider
	fallbackName     string // adapter name to use when the requested name is unknown
}

// Registry holds the per-adapter behaviour the metrics adapter dispatches
// on. Callers populate it from an []agent.Agent slice via the helpers in
// core/adapters/inbound/agents (Parsers, SubagentCounters,
// MetricsProviders).
type Registry struct {
	Parsers          map[string]agents.ParserFactory
	SubagentCounters map[string]agents.SubagentCounter
	MetricsProviders map[string]agents.MetricsProvider
	// FallbackName is the adapter name whose parser handles unknown
	// adapters (preserves the "default to Claude Code" behaviour). Looked
	// up in Parsers at parse time — single source of truth for the
	// fallback factory, and zero ambiguity if Parsers["claude-code"] is
	// ever swapped.
	FallbackName string
}

// New returns a metrics Adapter configured from the given Registry.
func New(r Registry) *Adapter {
	return &Adapter{
		tailers:          make(map[string]*lockedTailer),
		parsers:          r.Parsers,
		subagents:        r.SubagentCounters,
		metricsProviders: r.MetricsProviders,
		fallbackName:     r.FallbackName,
	}
}

// parserFor returns a fresh TranscriptParser for the given adapter name,
// falling back to the parser registered under fallbackName for unknown
// names. Returns nil when neither lookup yields a factory.
func (a *Adapter) parserFor(name string) tailer.TranscriptParser {
	if f, ok := a.parsers[name]; ok {
		return f()
	}
	if f, ok := a.parsers[a.fallbackName]; ok {
		return f()
	}
	return nil
}

// countOpenSubagents returns the adapter-specific count of in-process child
// agents currently running, or zero when the adapter did not register a counter.
func (a *Adapter) countOpenSubagents(name string, m *tailer.SessionMetrics) int {
	if f, ok := a.subagents[name]; ok {
		return f(m)
	}
	return 0
}

// ComputeMetrics analyses the transcript at transcriptPath and returns session metrics.
// The adapter parameter selects the correct transcript parser.
// Returns (nil, nil) when the transcript doesn't exist yet or yields no data.
//
// For adapters with a registered MetricsProvider (e.g. OpenCode), the provider
// is called directly. The transcriptPath for such adapters doubles as a session
// discriminator: it is formatted as "<dbPath>?session=<sessionID>" so the
// provider can extract both the database path and the session ID.
func (a *Adapter) ComputeMetrics(transcriptPath, adapter string) (*session.SessionMetrics, error) {
	if transcriptPath == "" {
		return nil, nil
	}

	// Delegate to adapter-specific provider when registered.
	if provider, ok := a.metricsProviders[adapter]; ok {
		return provider(transcriptPath, "")
	}

	a.mu.Lock()
	lt, ok := a.tailers[transcriptPath]
	if !ok {
		t := tailer.NewTranscriptTailer(transcriptPath, a.parserFor(adapter), adapter)
		lp := ledgerPath(transcriptPath)
		if s := loadLedger(lp); s != nil {
			t.SetLedgerState(*s)
		}
		lt = &lockedTailer{t: t, lp: lp}
		a.tailers[transcriptPath] = lt
	}
	a.mu.Unlock()

	// Per-tailer lock: serializes calls for the same path but allows
	// different sessions to process concurrently.
	lt.mu.Lock()
	m, err := lt.t.TailAndProcess()
	if err == nil && m != nil {
		saveLedger(lt.lp, lt.t.GetLedgerState())
	}
	lt.mu.Unlock()
	if err != nil || m == nil {
		return nil, nil //nolint:nilerr — absent transcript is not an error
	}
	// Plain field copies live in replayengine.TailerToDomain — the single
	// tailer→domain conversion shared with the replay paths. Everything
	// below is a live-only enrichment.
	result := replayengine.TailerToDomain(m)
	result.OpenSubagents = a.countOpenSubagents(adapter, m)
	if m.RateLimit != nil {
		result.RateLimit = tailerRateLimitToDomain(m.RateLimit)
		history := tailerRateLimitHistoryToDomain(m.RateLimitHistory)
		if eta := session.ForecastCap(history, time.Now()); eta != nil {
			etaUnix := eta.Unix()
			result.RateLimitForecastEta = &etaUnix
		}
	}
	// A fresh in-band marker is the agent's own holistic estimate and wins;
	// when none survives (claude ≥2.1.162 drops mid-task text blocks, #604)
	// — or the surviving one has gone stale while the task list kept moving
	// (#622) — the estimate derives from the task list's completion stamps.
	// FresherTaskEstimate holds the grace rule; the forecast base must
	// follow whichever source was chosen.
	markerEst := tailerTaskEstimateToDomain(m.TaskEstimate)
	var markerBase *session.TaskEstimate
	if markerEst != nil {
		markerEst.Source = "marker"
		markerBase = tailerTaskEstimateToDomain(m.TaskEstimateBase)
	}
	tasksEst, tasksBase := session.TaskEstimateFromTasks(result.Tasks)
	result.TaskEstimate = session.FresherTaskEstimate(markerEst, tasksEst, time.Now())
	estBase := markerBase
	if result.TaskEstimate == tasksEst && tasksEst != nil {
		estBase = tasksBase
	}
	if result.TaskEstimate != nil {
		if eta := session.ForecastTaskCompletion(result.TaskEstimate, estBase, m.ElapsedSeconds, time.Now()); eta != nil {
			etaUnix := eta.Unix()
			result.TaskCompletionEta = &etaUnix
		}
	}
	if result.ModelName == "" {
		result.ModelName = "unknown"
	}
	if result.PressureLevel == "" {
		result.PressureLevel = "unknown"
	}
	return result, nil
}

// ComputeMetricsTimeline returns cumulative SessionMetrics snapshots — one per
// transcript turn/accumulation point, ascending by VirtualTime — so a replay
// viewer can animate cost/tokens across the playhead instead of showing only
// the final total. It drives a throwaway replayengine pass on a private tailer
// (its own scratch file), so it never touches the per-path tailer cache or the
// on-disk ledger that ComputeMetrics maintains.
//
// Returns nil for an absent/empty transcript and for adapters backed by a
// MetricsProvider (e.g. OpenCode's SQLite store) which have no transcript-line
// stream to accumulate over — callers fall back to a single ComputeMetrics.
func (a *Adapter) ComputeMetricsTimeline(transcriptPath, adapter string) ([]session.MetricsTimelinePoint, error) {
	if transcriptPath == "" {
		return nil, nil
	}
	if _, ok := a.metricsProviders[adapter]; ok {
		return nil, nil // no transcript-line stream; caller uses single-shot
	}
	parser := a.parserFor(adapter)
	if parser == nil {
		return nil, nil
	}
	res, err := replayengine.ReplayTranscript(transcriptPath, replayengine.Options{
		Adapter:                    adapter,
		Parser:                     parser,
		DisableModelConfigFallback: true,
		EmitMetricsTimeline:        true,
	})
	if err != nil || res == nil || len(res.MetricsTimeline) == 0 {
		return nil, err //nolint:nilerr — absent/empty transcript is not an error
	}
	out := make([]session.MetricsTimelinePoint, 0, len(res.MetricsTimeline))
	for _, s := range res.MetricsTimeline {
		out = append(out, session.MetricsTimelinePoint{VirtualTime: s.VirtualTime, Metrics: s.Metrics})
	}
	return out, nil
}

// IngestRateLimit pushes a rate-limit snapshot into the tailer for
// transcriptPath. Used by the Claude Code statusline hook receiver. No-op
// when no tailer exists for the path (snapshot arrived before the session
// was detected) — the snapshot is simply dropped; the next statusline tick
// will populate it once the tailer exists.
func (a *Adapter) IngestRateLimit(transcriptPath string, snap *session.RateLimitSnapshot) {
	if transcriptPath == "" || snap == nil {
		return
	}
	a.mu.Lock()
	lt, ok := a.tailers[transcriptPath]
	a.mu.Unlock()
	if !ok {
		return
	}
	tailerSnap := domainRateLimitToTailer(snap)
	lt.mu.Lock()
	lt.t.IngestRateLimit(tailerSnap)
	lt.mu.Unlock()
}

// IngestTaskEstimate implements ports/outbound.MetricsCollector. Mirrors
// IngestRateLimit: hook-delivered estimates (#604) reach the session's
// tailer when one exists; otherwise no-op — the next ComputeMetrics creates
// the tailer and the next hook delivery lands.
func (a *Adapter) IngestTaskEstimate(transcriptPath string, est *session.TaskEstimate) {
	if transcriptPath == "" || est == nil {
		return
	}
	a.mu.Lock()
	lt, ok := a.tailers[transcriptPath]
	a.mu.Unlock()
	if !ok {
		return
	}
	tailerEst := &tailer.TaskEstimate{
		TotalRounds:     est.TotalRounds,
		CompletedRounds: est.CompletedRounds,
		Risk:            est.Risk,
		Confidence:      est.Confidence,
		ObservedAt:      est.UpdatedAt,
	}
	lt.mu.Lock()
	lt.t.IngestTaskEstimate(tailerEst)
	lt.mu.Unlock()
}

// domainRateLimitToTailer is the inbound counterpart to tailerRateLimitToDomain
// — used by IngestRateLimit when the HTTP layer hands us a domain-typed
// snapshot that has to land inside the tailer's mirror type.
func domainRateLimitToTailer(src *session.RateLimitSnapshot) *tailer.RateLimitSnapshot {
	if src == nil {
		return nil
	}
	dst := &tailer.RateLimitSnapshot{
		PlanType:    src.PlanType,
		ReachedType: src.ReachedType,
		SampledAt:   src.SampledAt,
	}
	if len(src.Windows) > 0 {
		dst.Windows = make([]tailer.RateLimitWindow, len(src.Windows))
		for i, w := range src.Windows {
			dst.Windows[i] = tailer.RateLimitWindow{
				UsedPercent:   w.UsedPercent,
				WindowMinutes: w.WindowMinutes,
				ResetsAt:      w.ResetsAt,
			}
		}
	}
	if src.Credits != nil {
		dst.Credits = &tailer.CreditsSnapshot{
			HasCredits: src.Credits.HasCredits,
			Unlimited:  src.Credits.Unlimited,
			Balance:    src.Credits.Balance,
		}
	}
	return dst
}

// PruneEntry releases per-session state when a session ends: drops the
// in-memory tailer cache entry and removes the on-disk ledger file. Idempotent
// on a missing transcript path or already-removed ledger file. Silent on I/O
// errors — the ledger is best-effort cache, not authoritative state.
//
// We don't take the per-tailer lock — caller invariant is that PruneEntry runs
// in response to EventRemoved (transcript file gone), so any concurrent
// TailAndProcess returns nil metrics without saving. If a save did race in,
// the daemon-startup orphan sweep would clean it up on next restart.
func (a *Adapter) PruneEntry(transcriptPath string) {
	if transcriptPath == "" {
		return
	}
	a.mu.Lock()
	delete(a.tailers, transcriptPath)
	a.mu.Unlock()
	if lp := ledgerPath(transcriptPath); lp != "" {
		_ = os.Remove(lp)
	}
}

// tailerRateLimitToDomain converts a tailer-side snapshot to its domain mirror.
func tailerRateLimitToDomain(src *tailer.RateLimitSnapshot) *session.RateLimitSnapshot {
	if src == nil {
		return nil
	}
	dst := &session.RateLimitSnapshot{
		PlanType:    src.PlanType,
		ReachedType: src.ReachedType,
		SampledAt:   src.SampledAt,
	}
	if len(src.Windows) > 0 {
		dst.Windows = make([]session.RateLimitWindow, len(src.Windows))
		for i, w := range src.Windows {
			dst.Windows[i] = session.RateLimitWindow{
				UsedPercent:   w.UsedPercent,
				WindowMinutes: w.WindowMinutes,
				ResetsAt:      w.ResetsAt,
			}
		}
	}
	if src.Credits != nil {
		dst.Credits = &session.CreditsSnapshot{
			HasCredits: src.Credits.HasCredits,
			Unlimited:  src.Credits.Unlimited,
			Balance:    src.Credits.Balance,
		}
	}
	return dst
}

// tailerTaskEstimateToDomain converts a tailer-side task estimate to its
// domain mirror.
func tailerTaskEstimateToDomain(src *tailer.TaskEstimate) *session.TaskEstimate {
	if src == nil {
		return nil
	}
	return &session.TaskEstimate{
		TotalRounds:     src.TotalRounds,
		CompletedRounds: src.CompletedRounds,
		Risk:            src.Risk,
		Confidence:      src.Confidence,
		UpdatedAt:       src.ObservedAt,
	}
}

// tailerRateLimitHistoryToDomain copies the rolling history into the
// domain-typed slice the forecast helper consumes.
func tailerRateLimitHistoryToDomain(src []tailer.RateLimitSnapshot) []session.RateLimitSnapshot {
	if len(src) == 0 {
		return nil
	}
	dst := make([]session.RateLimitSnapshot, len(src))
	for i := range src {
		converted := tailerRateLimitToDomain(&src[i])
		dst[i] = *converted
	}
	return dst
}

