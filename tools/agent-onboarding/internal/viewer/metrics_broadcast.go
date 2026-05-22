package viewer

import (
	"os"
	"path/filepath"
	"sync"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// buildMetricsCollector constructs the production metrics.Adapter the
// daemon uses, so replay broadcasts can carry the same SessionMetrics
// shape the live UI expects (model, total_tokens, context_window,
// estimated_cost_usd, …). Mirrors the wiring in
// core/cmd/irrlichd/main.go: parserFactories are derived from
// agents.All() plus the FilesUnderCWD (aider) / ProcessOwnedStore
// (opencode) overrides that agents.Parsers omits.
func buildMetricsCollector() outbound.MetricsCollector {
	all := agents.All()
	parserFactories := agents.Parsers(all)
	parserFactories[aider.AdapterName] = func() tailer.TranscriptParser { return &aider.Parser{} }
	parserFactories[opencode.AdapterName] = func() tailer.TranscriptParser { return &opencode.Parser{} }
	return metrics.New(metrics.Registry{
		Parsers:          parserFactories,
		SubagentCounters: agents.SubagentCounters(all),
		MetricsProviders: agents.MetricsProviders(all),
		FallbackName:     claudecode.AdapterName,
	})
}

// metricsEnricher decorates a PushBroadcaster so each session broadcast
// carries a populated SessionState.Metrics. Wraps the manager's shared
// broadcaster per-playback: state machine → enricher → shared logged
// broadcaster → WebSocket hub → dashboard iframe. The dashboard JS at
// platforms/web/index.html:1895 already renders model_name /
// total_tokens / context_utilization_percentage / estimated_cost_usd
// from agent.metrics.*; the enricher just fills those fields in.
//
// The recorded transcript.jsonl is static — every ComputeMetrics call
// returns the same SessionMetrics. We cache per sessionID to avoid
// hammering the tailer once per broadcast. Limitation: multi-session
// recordings (e.g. session-reset with v1 → /clear → v2) share a single
// transcript file, so all sessions see the same final metrics blob.
type metricsEnricher struct {
	inner          outbound.PushBroadcaster
	collector      outbound.MetricsCollector
	transcriptPath string

	mu    sync.Mutex
	cache map[string]*session.SessionMetrics
}

func newMetricsEnricher(inner outbound.PushBroadcaster, collector outbound.MetricsCollector, eventsDir string) *metricsEnricher {
	transcriptPath := filepath.Join(eventsDir, "transcript.jsonl")
	// Invalidate any persisted ledger for this transcript. The metrics
	// collector caches per-transcript LastOffset to disk so a daemon
	// restart resumes mid-stream — exactly the wrong behaviour for
	// replay: a recorded transcript never grows, so a stale lastOffset
	// at EOF would have the next ComputeMetrics call read zero bytes
	// and return total_tokens=0 / no model. Deleting the ledger forces
	// a fresh full scan on the next call.
	if ledgerDir, err := metrics.LedgerDir(); err == nil {
		_ = os.Remove(filepath.Join(ledgerDir, metrics.LedgerFilename(transcriptPath)))
	}
	return &metricsEnricher{
		inner:          inner,
		collector:      collector,
		transcriptPath: transcriptPath,
		cache:          map[string]*session.SessionMetrics{},
	}
}

func (e *metricsEnricher) Broadcast(msg outbound.PushMessage) {
	if msg.Session != nil && msg.Session.Metrics == nil {
		if m := e.lookup(msg.Session.SessionID, msg.Session.Adapter); m != nil {
			cp := *msg.Session
			cp.Metrics = m
			msg.Session = &cp
		}
	}
	e.inner.Broadcast(msg)
}

func (e *metricsEnricher) lookup(sessionID, adapter string) *session.SessionMetrics {
	e.mu.Lock()
	defer e.mu.Unlock()
	if m, ok := e.cache[sessionID]; ok {
		return m
	}
	m, _ := e.collector.ComputeMetrics(e.transcriptPath, adapter)
	e.cache[sessionID] = m // cache the nil result too — no transcript means no metrics, forever for this session
	return m
}

func (e *metricsEnricher) Subscribe() chan outbound.PushMessage {
	return e.inner.Subscribe()
}

func (e *metricsEnricher) Unsubscribe(ch chan outbound.PushMessage) {
	e.inner.Unsubscribe(ch)
}
