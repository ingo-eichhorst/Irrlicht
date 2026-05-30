package viewer

import (
	"os"
	"path/filepath"
	"sync"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/agentwiring"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
	"irrlicht/tools/onboarding-factory/internal/replay"
)

// buildMetricsCollector constructs the production metrics.Adapter the
// daemon uses, so replay broadcasts can carry the same SessionMetrics
// shape the live UI expects (model, total_tokens, context_window,
// estimated_cost_usd, …). It delegates to
// agentwiring.BuildMetricsCollector — the single source of truth shared
// with core/cmd/irrlichd, so the viewer can never drift from the daemon's
// parser map or fall behind when a new adapter is added.
func buildMetricsCollector() outbound.MetricsCollector {
	return agentwiring.BuildMetricsCollector(agents.All())
}

// metricsEnricher decorates a PushBroadcaster so each session broadcast
// carries a populated SessionState.Metrics. Wraps the manager's shared
// broadcaster per-playback: state machine → enricher → shared logged
// broadcaster → WebSocket hub → dashboard iframe. The dashboard JS at
// platforms/web/index.html:1895 already renders model_name /
// total_tokens / context_utilization_percentage / estimated_cost_usd
// from agent.metrics.*; the enricher just fills those fields in.
//
// When a StateMachine is attached (attachMachine), the enricher builds a
// per-turn metrics TIMELINE from the transcript (ComputeMetricsTimeline) and,
// on every broadcast, returns the cumulative snapshot at the machine's current
// playhead — so cost/tokens climb turn-by-turn and follow seeks instead of
// jumping straight to the final total. Adapters with no transcript-line stream
// (e.g. OpenCode) yield no timeline and fall back to a single cached
// ComputeMetrics. Limitation: multi-session recordings (session-reset v1 →
// /new → v2) share one transcript file, so both sessions share one timeline.
type metricsEnricher struct {
	inner          outbound.PushBroadcaster
	collector      outbound.MetricsCollector
	transcriptPath string

	mu       sync.Mutex
	machine  *replay.StateMachine // nil → no playhead; fall back to single-shot
	tlBuilt  bool                 // timeline build attempted (built lazily on first broadcast)
	timeline []timelinePoint      // ascending by offsetMs; empty → fall back
	cache    map[string]*session.SessionMetrics
}

// timelinePoint is a cumulative metrics snapshot at a recording-relative
// offset (ms from the playback anchor).
type timelinePoint struct {
	offsetMs int64
	metrics  *session.SessionMetrics
}

func newMetricsEnricher(inner outbound.PushBroadcaster, collector outbound.MetricsCollector, eventsDir string) *metricsEnricher {
	transcriptPath := filepath.Join(eventsDir, "transcript.jsonl")
	// Invalidate any persisted ledger for this transcript. The metrics
	// collector caches per-transcript LastOffset to disk so a daemon
	// restart resumes mid-stream — exactly the wrong behaviour for
	// replay: a recorded transcript never grows, so a stale lastOffset
	// at EOF would have the next ComputeMetrics call read zero bytes
	// and return total_tokens=0 / no model. Deleting the ledger forces
	// a fresh full scan on the next call. (Only the single-shot fallback
	// uses this ledger; ComputeMetricsTimeline drives a private tailer.)
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

// attachMachine wires the playback state machine so the enricher can read the
// live playhead (LivePlayheadMs) and the recording anchor (Anchor). Called
// after replay.New and before the machine runs.
func (e *metricsEnricher) attachMachine(m *replay.StateMachine) {
	e.mu.Lock()
	e.machine = m
	e.mu.Unlock()
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

// lookup returns the metrics to stamp onto a session broadcast (or Snapshot).
// With a playhead it returns the cumulative timeline snapshot at the current
// position (animated); otherwise it falls back to a single cached
// ComputeMetrics over the whole transcript.
func (e *metricsEnricher) lookup(sessionID, adapter string) *session.SessionMetrics {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.machine != nil {
		e.ensureTimelineLocked(adapter)
		if len(e.timeline) > 0 {
			// Lock-free playhead read: this runs inside Broadcast, which the
			// state machine calls while holding its mutex; LivePlayheadMs would
			// deadlock by re-taking it.
			return selectAtPlayhead(e.timeline, e.machine.PlayheadMsLockFree())
		}
		// No timeline for this adapter (e.g. OpenCode) → single-shot below.
	}

	if m, ok := e.cache[sessionID]; ok {
		return m
	}
	m, _ := e.collector.ComputeMetrics(e.transcriptPath, adapter)
	e.cache[sessionID] = m // cache the nil result too — no transcript means no metrics, forever for this session
	return m
}

// ensureTimelineLocked builds the per-turn timeline once, using the same
// adapter string the broadcast carries (so parser resolution matches the
// proven ComputeMetrics path) and the machine's anchor to convert each
// snapshot's virtual time into a playback offset. e.mu must be held.
func (e *metricsEnricher) ensureTimelineLocked(adapter string) {
	if e.tlBuilt {
		return
	}
	e.tlBuilt = true
	tl, err := e.collector.ComputeMetricsTimeline(e.transcriptPath, adapter)
	if err != nil || len(tl) == 0 {
		return
	}
	anchor := e.machine.Anchor()
	pts := make([]timelinePoint, 0, len(tl))
	for _, p := range tl {
		off := p.VirtualTime.Sub(anchor).Milliseconds()
		if off < 0 {
			off = 0
		}
		pts = append(pts, timelinePoint{offsetMs: off, metrics: p.Metrics})
	}
	e.timeline = pts
}

// selectAtPlayhead returns a copy of the last timeline snapshot whose offset is
// at or before posMs. Returns nil while the playhead precedes the first
// snapshot (show "no metrics yet" rather than a final-ish total at t=0).
func selectAtPlayhead(pts []timelinePoint, posMs int64) *session.SessionMetrics {
	var chosen *session.SessionMetrics
	for _, p := range pts {
		if p.offsetMs <= posMs {
			chosen = p.metrics
		} else {
			break
		}
	}
	if chosen == nil {
		return nil
	}
	cp := *chosen // fresh pointer per broadcast; fields are read-only downstream
	return &cp
}

func (e *metricsEnricher) Subscribe() chan outbound.PushMessage {
	return e.inner.Subscribe()
}

func (e *metricsEnricher) Unsubscribe(ch chan outbound.PushMessage) {
	e.inner.Unsubscribe(ch)
}
