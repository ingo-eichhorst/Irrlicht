package services_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// The removal paths a child session takes, and what the FinishedSubagentStore
// (#2061) keeps from each. The parent-ready path (cleanupChildren) is driven
// end to end, through the usage document, by
// core/cmd/irrlichd/herdr_usage_finished_subagents_test.go.

func newFinishedSubagentDetector(t *testing.T, repo *mockRepo, store *filesystem.FinishedSubagentStore) (*services.SessionDetector, func()) {
	t.Helper()
	tw := newMockAgentWatcher()
	det := services.NewSessionDetector([]inbound.Watcher{tw}, services.SessionDetectorDeps{
		PW: newMockProcessWatcher(), Repo: repo, Log: &mockLogger{}, Git: &mockGit{}, Metrics: &mockMetrics{},
		Broadcaster: &mockBroadcaster{}, Version: "test", ReadyTTL: sweepTestReadyTTL,
	})
	det.SetFinishedSubagentStore(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	return det, func() { cancel(); <-done }
}

func staleTranscript(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSessionDetector_SweptChildIsRetained: the liveness sweep's removal of a
// stuck child (reapStaleChild) keeps the child's usage — and only its usage —
// while the parent's subagent summary is still cleared (#593).
func TestSessionDetector_SweptChildIsRetained(t *testing.T) {
	repo := newMockRepo()
	store := filesystem.NewFinishedSubagentStoreWithDir(t.TempDir())
	det, stop := newFinishedSubagentDetector(t, repo, store)
	dir := t.TempDir()
	now := time.Now().Unix()

	parent := &session.SessionState{
		SessionID: "parent-waiting", State: session.StateWaiting,
		TranscriptPath: staleTranscript(t, dir, "parent.jsonl", 0),
		FirstSeen:      now - parentIdleSeconds, UpdatedAt: now - parentIdleSeconds, EventCount: 10,
		Metrics: &session.SessionMetrics{LastEventType: "assistant", LastAssistantText: "Should I continue?"},
	}
	child := &session.SessionState{
		SessionID: "stuck-child", State: session.StateWorking, ParentSessionID: "parent-waiting", Adapter: "claude-code",
		TranscriptPath: staleTranscript(t, dir, "child.jsonl", 5*time.Minute),
		FirstSeen:      now - 30, UpdatedAt: now, EventCount: 3,
		Metrics: &session.SessionMetrics{
			LastEventType: "tool_use", LastAssistantText: "secret prompt text",
			CumInputTokens: 42, CumOutputTokens: 7, EstimatedCostUSD: 0.02, PricingSource: "litellm@2026-11-20",
		},
	}
	parent.Subagents = session.ComputeSubagentSummary(parent, []*session.SessionState{child})
	repo.Save(parent)
	repo.Save(child)

	det.RunPIDLivenessSweepForTest()
	waitForSessionDeleted(repo, "stuck-child", time.Second)
	waitForCondition(func() bool { return len(store.List()) == 1 }, time.Second)
	stop()

	recs := store.List()
	if len(recs) != 1 {
		t.Fatalf("records after the sweep removed the child: %+v, want one", recs)
	}
	r := recs[0]
	if r.SessionID != "stuck-child" || r.ParentSessionID != "parent-waiting" || r.Adapter != "claude-code" ||
		r.FirstSeen != now-30 || r.FinishedAt == 0 {
		t.Errorf("record = %+v, want stuck-child under parent-waiting with its FirstSeen and a FinishedAt", r)
	}
	if r.Usage == nil || r.Usage.CumInputTokens != 42 || r.Usage.CumOutputTokens != 7 ||
		r.Usage.EstimatedCostUSD != 0.02 || r.Usage.PricingSource != "litellm@2026-11-20" {
		t.Errorf("usage = %+v, want the child's final 42/7 tokens, $0.02 and its pricing source", r.Usage)
	}
	if r.Usage != nil && (r.Usage.LastAssistantText != "" || r.Usage.LastEventType != "") {
		t.Errorf("the record carries non-usage fields: %+v", r.Usage)
	}
	if got, _ := repo.Load("parent-waiting"); got == nil || got.Subagents != nil {
		t.Errorf("parent summary after the sweep: %+v, want cleared (#593)", got)
	}
}

// TestSessionDetector_RemovedRootKeepsItsRecords: removing a root (here the
// sweep ages out a long-ready one) does not drop its subagents' records — the
// same session ID can come back with `claude --continue`, still owning them.
func TestSessionDetector_RemovedRootKeepsItsRecords(t *testing.T) {
	repo := newMockRepo()
	store := filesystem.NewFinishedSubagentStoreWithDir(t.TempDir())
	now := time.Now()
	if err := store.Retain(session.FinishedSubagent{SessionID: "old-child", ParentSessionID: "old-root", FinishedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	det, stop := newFinishedSubagentDetector(t, repo, store)
	dir := t.TempDir()

	aged := now.Add(-2 * sweepTestReadyTTL).Unix()
	repo.Save(&session.SessionState{
		SessionID: "old-root", State: session.StateReady,
		TranscriptPath: staleTranscript(t, dir, "old-root.jsonl", 2*sweepTestReadyTTL),
		FirstSeen:      aged, UpdatedAt: aged, EventCount: 10,
		Metrics: &session.SessionMetrics{LastEventType: "assistant"},
	})

	det.RunPIDLivenessSweepForTest()
	waitForSessionDeleted(repo, "old-root", 2*time.Second)
	stop()

	if got, _ := repo.Load("old-root"); got != nil {
		t.Fatalf("the sweep did not remove the aged-out root (state %q); this test needs it removed", got.State)
	}
	if got := store.List(); len(got) != 1 || got[0].SessionID != "old-child" {
		t.Errorf("records after old-root was removed: %+v, want old-child kept for a resumed root", got)
	}
}

// probeStore answers the detector's prune with what rootExists said per root.
type probeStore struct {
	roots []string
	asked map[string]bool
}

func (p *probeStore) Retain(session.FinishedSubagent) error { return nil }
func (p *probeStore) List() []session.FinishedSubagent      { return nil }
func (p *probeStore) PruneOrphans(rootExists func(string) bool) error {
	p.asked = map[string]bool{}
	for _, r := range p.roots {
		p.asked[r] = rootExists(r)
	}
	return nil
}

// TestSessionDetector_PruneAsksTheSessionList: the detector's prune answers
// "does this root still exist" from the session repository.
func TestSessionDetector_PruneAsksTheSessionList(t *testing.T) {
	repo := newMockRepo()
	repo.Save(&session.SessionState{SessionID: "live-root", State: session.StateReady, UpdatedAt: time.Now().Unix()})
	probe := &probeStore{roots: []string{"live-root", "gone-root"}}
	det := services.NewSessionDetector([]inbound.Watcher{newMockAgentWatcher()}, services.SessionDetectorDeps{
		PW: newMockProcessWatcher(), Repo: repo, Log: &mockLogger{}, Git: &mockGit{}, Metrics: &mockMetrics{}, Version: "test",
	})
	det.SetFinishedSubagentStore(probe)
	det.RunFinishedSubagentPruneForTest()
	if !probe.asked["live-root"] || probe.asked["gone-root"] {
		t.Errorf("rootExists answered %v, want live-root true and gone-root false", probe.asked)
	}
}

// freshMetrics returns scripted metrics for one transcript path and records
// every ComputeMetrics call.
type freshMetrics struct {
	mockMetrics
	path  string
	fresh *session.SessionMetrics
	calls []string
	mu    sync.Mutex
}

func (f *freshMetrics) ComputeMetrics(path, _ string) (*session.SessionMetrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
	if path == f.path {
		return f.fresh, nil
	}
	return nil, nil
}

func (f *freshMetrics) callsFor(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == path {
			n++
		}
	}
	return n
}

// sweepRetainOne seeds a waiting parent and a stuck child, lets the sweep
// remove the child, and returns the one record kept.
func sweepRetainOne(t *testing.T, metrics *freshMetrics, consent func(string) bool) session.FinishedSubagent {
	t.Helper()
	repo := newMockRepo()
	store := filesystem.NewFinishedSubagentStoreWithDir(t.TempDir())
	det := services.NewSessionDetector([]inbound.Watcher{newMockAgentWatcher()}, services.SessionDetectorDeps{
		PW: newMockProcessWatcher(), Repo: repo, Log: &mockLogger{}, Git: &mockGit{}, Metrics: metrics,
		Broadcaster: &mockBroadcaster{}, Version: "test", ReadyTTL: sweepTestReadyTTL,
	})
	det.SetFinishedSubagentStore(store)
	if consent != nil {
		det.SetConsentGate(consent)
	}
	dir := t.TempDir()
	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID: "parent", State: session.StateWaiting, Adapter: "claude-code",
		TranscriptPath: staleTranscript(t, dir, "parent.jsonl", 0),
		FirstSeen:      now - parentIdleSeconds, UpdatedAt: now - parentIdleSeconds, EventCount: 10,
		Metrics: &session.SessionMetrics{LastEventType: "assistant"},
	})
	repo.Save(&session.SessionState{
		SessionID: "child", State: session.StateWorking, ParentSessionID: "parent", Adapter: "claude-code",
		TranscriptPath: metrics.path, FirstSeen: now, UpdatedAt: now, EventCount: 3,
		Metrics: &session.SessionMetrics{LastEventType: "tool_use", CumInputTokens: 5, CumOutputTokens: 1},
	})
	det.RunPIDLivenessSweepForTest()
	waitForSessionDeleted(repo, "child", time.Second)
	recs := store.List()
	if len(recs) != 1 || recs[0].Usage == nil {
		t.Fatalf("records: %+v, want the removed child's", recs)
	}
	return recs[0]
}

// TestSessionDetector_RetainedUsageReadsTheTranscriptTail: the record holds
// what the child's transcript says at removal — here more than its last
// activity pass had stored.
func TestSessionDetector_RetainedUsageReadsTheTranscriptTail(t *testing.T) {
	metrics := &freshMetrics{
		path:  staleTranscript(t, t.TempDir(), "child.jsonl", 5*time.Minute),
		fresh: &session.SessionMetrics{CumInputTokens: 50, CumOutputTokens: 9, PricingSource: "litellm@2026-11-20"},
	}
	rec := sweepRetainOne(t, metrics, nil)
	if rec.Usage.CumInputTokens != 50 || rec.Usage.CumOutputTokens != 9 || rec.Usage.PricingSource != "litellm@2026-11-20" {
		t.Errorf("usage = %+v, want the transcript's 50/9 tokens and source, not the stored 5/1", rec.Usage)
	}
}

// TestSessionDetector_RetainedUsageHonoursObserveConsent: with the adapter's
// observe consent withheld, removing a child does not read its transcript;
// the record keeps the metrics already stored.
func TestSessionDetector_RetainedUsageHonoursObserveConsent(t *testing.T) {
	metrics := &freshMetrics{
		path:  staleTranscript(t, t.TempDir(), "child.jsonl", 5*time.Minute),
		fresh: &session.SessionMetrics{CumInputTokens: 50},
	}
	rec := sweepRetainOne(t, metrics, func(string) bool { return false })
	if n := metrics.callsFor(metrics.path); n != 0 {
		t.Errorf("the child's transcript was read %d time(s) with observe consent withheld", n)
	}
	if rec.Usage.CumInputTokens != 5 {
		t.Errorf("usage = %+v, want the stored 5 input tokens", rec.Usage)
	}
}
