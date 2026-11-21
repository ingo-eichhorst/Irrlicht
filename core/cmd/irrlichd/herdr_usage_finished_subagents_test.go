package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// The daemon-side half of #2061's finished-subagent fix, driven the way a
// Claude Code session reaches it: a parent turn ends, the detector reclassifies
// the parent `ready`, its finishedParent cleanup deletes the child session —
// and `irrlicht-ls --herdr-usage`, run against the real /api/v1/sessions
// handler, must still list that child under the parent's subagents with its
// final usage.

type fsuWatcher struct{ ch chan agent.Event }

func (w *fsuWatcher) Identity() agent.Identity        { return agent.Identity{Name: "claude-code"} }
func (w *fsuWatcher) Watch(ctx context.Context) error { <-ctx.Done(); return nil }
func (w *fsuWatcher) Subscribe() <-chan agent.Event   { return w.ch }
func (w *fsuWatcher) Unsubscribe(<-chan agent.Event)  {}

type fsuProcessWatcher struct{}

func (fsuProcessWatcher) Watch(int, string) error       { return nil }
func (fsuProcessWatcher) Unwatch(int)                   {}
func (fsuProcessWatcher) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func (fsuProcessWatcher) Close() error                  { return nil }

type fsuGit struct{}

func (fsuGit) GetBranch(context.Context, string) (string, bool)      { return "main", true }
func (fsuGit) GetProjectName(context.Context, string) (string, bool) { return "demo", true }
func (fsuGit) GetGitRoot(context.Context, string) (string, bool)     { return "", true }
func (fsuGit) GetHeadCommit(context.Context, string) (string, bool)  { return "", true }
func (fsuGit) GetBranchFromTranscript(string) string                 { return "" }
func (fsuGit) GetCWDFromTranscript(string) string                    { return "" }

// fsuMetrics computes nothing, so every session keeps the metrics it was
// seeded with — the child's "final usage" is exactly what the test wrote.
type fsuMetrics struct{}

func (fsuMetrics) ComputeMetrics(string, string) (*session.SessionMetrics, error) { return nil, nil }
func (fsuMetrics) ComputeMetricsTimeline(string, string) ([]session.MetricsTimelinePoint, error) {
	return nil, nil
}
func (fsuMetrics) PruneEntry(string)                                  {}
func (fsuMetrics) IngestRateLimit(string, *session.RateLimitSnapshot) {}
func (fsuMetrics) IngestTaskEstimate(string, *session.TaskEstimate)   {}
func (fsuMetrics) IngestTaskSummary(string, string, int64)            {}
func (fsuMetrics) IngestTurnBoundary(string)                          {}
func (fsuMetrics) PurgeDeadBackgroundProcs(string, []string)          {}
func (fsuMetrics) PurgeDeadBackgroundPIDs(string, []string)           {}

func fsuUsage(input, output int64, cost float64) *session.SessionMetrics {
	return &session.SessionMetrics{
		ModelName:      "claude-haiku-5-5",
		ElapsedSeconds: 30,
		CumInputTokens: input, CumOutputTokens: output, CumCacheReadTokens: 100, CumCacheCreationTokens: 10,
		EstimatedCostUSD: cost,
		PricingSource:    "litellm@2026-11-20",
		UsageCoverage: &session.UsageCoverage{
			TokenTypes:        []session.TokenType{session.TokenInput, session.TokenOutput, session.TokenCacheRead, session.TokenCacheWrite},
			ExcludesSubagents: true,
		},
	}
}

func TestHerdrUsage_ListsSubagentsTheDaemonHasRemoved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFSUFixture(t)
	f.runUntilChildRemoved(t, f.seedParentWithFinishedChild(t))

	// Dashboards: the parent's subagent summary stays cleared (#593).
	if parent, _ := f.repo.Load("parent-pane-a"); parent.Subagents != nil {
		t.Errorf("parent subagent summary = %+v after cleanup, want nil", parent.Subagents)
	}

	root := f.herdrUsageRootForPaneA(t)
	if root.SessionID != "parent-pane-a" || !int64Is(root.Tokens.Input, 1200) {
		t.Errorf("root = %s with input %v, want parent-pane-a's own 1200 (parent totals exclude subagents)",
			root.SessionID, root.Tokens.Input)
	}
	if len(root.Subagents) != 1 {
		t.Fatalf("subagents = %+v, want child-of-a: a subagent the daemon removed at parent-ready must still be listed", root.Subagents)
	}
	if sub := root.Subagents[0]; !sub.isChildOfAsFinalUsage() {
		t.Errorf("subagent = %+v, want child-of-a with its final 10/20 tokens and $0.01 with a pricing source", sub)
	}
}

// fsuFixture is a session detector over a real session repository and
// finished-subagent store, fed by a watcher the test sends events on.
type fsuFixture struct {
	dir     string
	repo    *filesystem.SessionRepository
	store   *filesystem.FinishedSubagentStore
	watcher *fsuWatcher
	det     *services.SessionDetector
}

func newFSUFixture(t *testing.T) *fsuFixture {
	dir := t.TempDir()
	f := &fsuFixture{
		dir:     dir,
		repo:    filesystem.NewWithDir(filepath.Join(dir, "instances")),
		store:   filesystem.NewFinishedSubagentStoreWithDir(filepath.Join(dir, "finished-subagents")),
		watcher: &fsuWatcher{ch: make(chan agent.Event, 16)},
	}
	f.det = services.NewSessionDetector([]inbound.Watcher{f.watcher}, services.SessionDetectorDeps{
		PW: fsuProcessWatcher{}, Repo: f.repo, Log: e2eLog{}, Git: fsuGit{}, Metrics: fsuMetrics{}, Version: "test",
	})
	f.det.SetFinishedSubagentStore(f.store)
	return f
}

// seedParentWithFinishedChild saves a working parent in herdr pane w1:p1 and
// its child, whose transcript has been quiet for a while: it finished. It
// returns the parent's transcript path.
func (f *fsuFixture) seedParentWithFinishedChild(t *testing.T) string {
	dir := f.dir
	now := time.Now().Unix()
	parentPath := filepath.Join(dir, "parent.jsonl")
	childPath := filepath.Join(dir, "child.jsonl")
	for _, p := range []string{parentPath, childPath} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The child's transcript has been quiet for a while: it finished.
	stale := time.Now().Add(-100 * time.Second)
	if err := os.Chtimes(childPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	parentMetrics := fsuUsage(1200, 340, 0.42)
	parentMetrics.LastEventType = "assistant"
	parentMetrics.LastAssistantText = "All done."
	f.mustSave(t, &session.SessionState{
		SessionID: "parent-pane-a", State: session.StateWorking, Adapter: "claude-code",
		TranscriptPath: parentPath, CWD: dir, FirstSeen: now - 60, UpdatedAt: now, EventCount: 10,
		Launcher: &session.Launcher{HerdrPaneID: "w1:p1", HerdrSocketPath: "/tmp/herdr-test/herdr.sock"},
		Metrics:  parentMetrics,
	})
	childMetrics := fsuUsage(10, 20, 0.01)
	childMetrics.LastEventType = "assistant"
	f.mustSave(t, &session.SessionState{
		SessionID: "child-of-a", State: session.StateWorking, Adapter: "claude-code",
		ParentSessionID: "parent-pane-a", TranscriptPath: childPath, CWD: dir,
		FirstSeen: now - 30, UpdatedAt: now, EventCount: 5, Metrics: childMetrics,
	})
	return parentPath
}

func (f *fsuFixture) mustSave(t *testing.T, s *session.SessionState) {
	t.Helper()
	if err := f.repo.Save(s); err != nil {
		t.Fatal(err)
	}
}

// runUntilChildRemoved runs the detector, sends the parent's activity, and
// polls the subject to a deadline: the child's session is gone and the parent
// is ready. The detector is stopped before it returns.
func (f *fsuFixture) runUntilChildRemoved(t *testing.T, parentPath string) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.det.Run(ctx) }()
	f.watcher.ch <- agent.Event{
		Type: agent.EventActivity, SessionID: "parent-pane-a", ProjectDir: "-demo", TranscriptPath: parentPath,
	}
	var parent, child *session.SessionState
	removed := pollUntil(t, 10*time.Second, 20*time.Millisecond, func() bool {
		parent, _ = f.repo.Load("parent-pane-a")
		child, _ = f.repo.Load("child-of-a")
		return parent != nil && parent.State == session.StateReady && child == nil
	})
	cancel()
	<-done
	if !removed {
		t.Fatalf("after 10s: parent %+v, child still present %t — the cleanup this test needs never ran", parent, child != nil)
	}
}

// fsuSubagent and fsuRoot are the parts of the usage document this test reads.
type fsuSubagent struct {
	SessionID string `json:"session_id"`
	Tokens    struct {
		Input  *int64 `json:"input"`
		Output *int64 `json:"output"`
	} `json:"tokens"`
	Cost struct {
		USD           *float64 `json:"usd"`
		PricingSource *string  `json:"pricing_source"`
	} `json:"cost"`
}

type fsuRoot struct {
	SessionID string `json:"session_id"`
	Tokens    struct {
		Input *int64 `json:"input"`
	} `json:"tokens"`
	Subagents []fsuSubagent `json:"subagents"`
}

// isChildOfAsFinalUsage reports child-of-a with its final 10/20 tokens and
// $0.01 with a pricing source.
func (s fsuSubagent) isChildOfAsFinalUsage() bool {
	return s.SessionID == "child-of-a" && int64Is(s.Tokens.Input, 10) && int64Is(s.Tokens.Output, 20) &&
		s.Cost.USD != nil && *s.Cost.USD == 0.01 && s.Cost.PricingSource != nil
}

func int64Is(p *int64, want int64) bool {
	return p != nil && *p == want
}

// herdrUsageRootForPaneA serves the fixture's repository and store on the
// real /api/v1/sessions handler, runs a freshly built `irrlicht-ls
// --herdr-usage` for pane w1:p1 against it, and returns the document's one
// session.
func (f *fsuFixture) herdrUsageRootForPaneA(t *testing.T) fsuRoot {
	push := services.NewPushService()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(f.repo, services.NewOrchestratorMonitor(nil, push, nil), nil, nil, nil, f.store))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	bin := filepath.Join(t.TempDir(), "irrlicht-ls")
	if out, err := exec.Command("go", "build", "-o", bin, "../irrlicht-ls").CombinedOutput(); err != nil {
		t.Fatalf("go build irrlicht-ls: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "--herdr-usage")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "IRRLICHT_BIND_ADDR=" + u.Host,
		"HERDR_PANE_ID=w1:p1", "HERDR_SOCKET_PATH=/tmp/herdr-test/herdr.sock"}
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("irrlicht-ls --herdr-usage: %v", err)
	}
	var doc struct {
		Sessions []fsuRoot `json:"sessions"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil || len(doc.Sessions) != 1 {
		t.Fatalf("document = %s (%v), want one session", stdout, err)
	}
	return doc.Sessions[0]
}

// TestHandleGetSessions_FinishedSubagentsOnlyOnRequest: the records are served
// under ?include=finished_subagents and nowhere else, so the payload every
// dashboard reads is unchanged.
func TestHandleGetSessions_FinishedSubagentsOnlyOnRequest(t *testing.T) {
	dir := t.TempDir()
	store := filesystem.NewFinishedSubagentStoreWithDir(dir)
	store.Retain(session.FinishedSubagent{SessionID: "gone", ParentSessionID: "root", FinishedAt: time.Now().Unix()})
	push := services.NewPushService()
	h := handleGetSessions(filesystem.NewWithDir(t.TempDir()), services.NewOrchestratorMonitor(nil, push, nil), nil, nil, nil, store)

	get := func(target string) map[string]json.RawMessage {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, target, nil))
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		return body
	}
	if _, ok := get("/api/v1/sessions")["finished_subagents"]; ok {
		t.Error("finished_subagents served without being asked for; the dashboard payload must not change")
	}
	raw, ok := get("/api/v1/sessions?include=finished_subagents")["finished_subagents"]
	if !ok || !isOnlyTheGoneRecord(raw) {
		t.Errorf("?include=finished_subagents served %s, want the one retained record", raw)
	}
}

// isOnlyTheGoneRecord reports whether raw holds exactly one record, gone's.
func isOnlyTheGoneRecord(raw json.RawMessage) bool {
	var recs []session.FinishedSubagent
	return json.Unmarshal(raw, &recs) == nil && len(recs) == 1 && recs[0].SessionID == "gone"
}
