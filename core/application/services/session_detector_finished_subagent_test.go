package services_test

import (
	"context"
	"os"
	"path/filepath"
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

// TestSessionDetector_RemovedRootForgetsItsRecords: when the root itself is
// removed (here: the sweep ages out a long-ready root), its subagents'
// records go with it.
func TestSessionDetector_RemovedRootForgetsItsRecords(t *testing.T) {
	repo := newMockRepo()
	store := filesystem.NewFinishedSubagentStoreWithDir(t.TempDir())
	now := time.Now()
	store.Retain(session.FinishedSubagent{SessionID: "old-child", ParentSessionID: "old-root", FinishedAt: now.Unix()})
	store.Retain(session.FinishedSubagent{SessionID: "keep-child", ParentSessionID: "live-root", FinishedAt: now.Unix()})
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
	waitForCondition(func() bool { return len(store.List()) == 1 }, time.Second)
	stop()

	if got, _ := repo.Load("old-root"); got != nil {
		t.Fatalf("the sweep did not remove the aged-out root (state %q); this test needs it removed", got.State)
	}
	recs := store.List()
	if len(recs) != 1 || recs[0].SessionID != "keep-child" {
		t.Errorf("records after old-root was removed: %+v, want only keep-child", recs)
	}
}
