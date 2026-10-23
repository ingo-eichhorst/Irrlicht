package services

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// freshSnapshotMetrics mimics the real metrics adapter's round trip: a
// statusline tick is held in the tailer and every ComputeMetrics hands back a
// FRESHLY built snapshot that carries only the tailer mirror's identity
// fields — never ConfirmedAccountRef (domainRateLimitToTailer copies four).
type freshSnapshotMetrics struct {
	outbound.MetricsCollector
	held *session.RateLimitSnapshot
}

func (f *freshSnapshotMetrics) IngestRateLimit(_ string, snap *session.RateLimitSnapshot) {
	f.held = snap
}

func (f *freshSnapshotMetrics) ComputeMetrics(_, _ string) (*session.SessionMetrics, error) {
	m := &session.SessionMetrics{}
	if f.held != nil {
		m.RateLimit = &session.RateLimitSnapshot{SampledAt: f.held.SampledAt}
	}
	return m, nil
}

// snapshotRef is the session's confirmed account ref, "" when it has no
// rate-limit snapshot at all.
func snapshotRef(st *session.SessionState) string {
	if st.Metrics == nil || st.Metrics.RateLimit == nil {
		return ""
	}
	return st.Metrics.RateLimit.ConfirmedAccountRef
}

// R3 (red-first: with the binder field present but not called from the
// enricher the ref is ""): a statusline tick ingested, then the activity
// refresh, must leave the ref on the session's snapshot — on EVERY pass.
func TestEnricher_StampsAccountRefAfterEveryMetricsPass(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	g.now = 200
	fm := &freshSnapshotMetrics{}
	e := newMetadataEnricher(&enricherGit{rootAnswered: true}, fm)
	e.binder = g.b

	st := &session.SessionState{SessionID: "s1", Adapter: "claude-code", FirstSeen: 100, TranscriptPath: "/t.jsonl"}
	fm.IngestRateLimit("/t.jsonl", &session.RateLimitSnapshot{SampledAt: 150})
	e.RefreshOnActivity(st, "/t.jsonl")
	if snapshotRef(st) != "acct-A" {
		t.Fatalf("after RefreshOnActivity: snapshot = %+v, want ConfirmedAccountRef acct-A", st.Metrics)
	}
	e.RefreshMetrics(st)
	if got := st.Metrics.RateLimit.ConfirmedAccountRef; got != "acct-A" {
		t.Errorf("after RefreshMetrics: ref = %q, want acct-A", got)
	}

	// A second session created through EnrichNewSession is stamped too.
	st2 := &session.SessionState{SessionID: "s2", Adapter: "claude-code", FirstSeen: 100}
	e.EnrichNewSession(st2, agent.Event{SessionID: "s2", TranscriptPath: "/t.jsonl"})
	if snapshotRef(st2) != "acct-A" {
		t.Errorf("after EnrichNewSession: snapshot = %+v, want ConfirmedAccountRef acct-A", st2.Metrics)
	}
}
