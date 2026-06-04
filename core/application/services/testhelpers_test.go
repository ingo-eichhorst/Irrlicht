package services_test

import (
	"context"
	"errors"
	"sync"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// mockRecorder captures lifecycle events for assertions. Safe for
// concurrent use — the detector records from multiple goroutines.
type mockRecorder struct {
	mu     sync.Mutex
	events []lifecycle.Event
}

func (r *mockRecorder) Record(ev lifecycle.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *mockRecorder) Close() error { return nil }

func (r *mockRecorder) snapshot() []lifecycle.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]lifecycle.Event, len(r.events))
	copy(out, r.events)
	return out
}

// --- shared mock implementations for tests -----------------------------------

type mockRepo struct {
	mu     sync.Mutex
	states map[string]*session.SessionState
	saves  int
}

func newMockRepo() *mockRepo {
	return &mockRepo{states: make(map[string]*session.SessionState)}
}

func (r *mockRepo) Load(sessionID string) (*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[sessionID]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

func (r *mockRepo) Save(s *session.SessionState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[s.SessionID] = s
	r.saves++
	return nil
}

func (r *mockRepo) Delete(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, sessionID)
	return nil
}

func (r *mockRepo) ListAll() ([]*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*session.SessionState, 0, len(r.states))
	for _, s := range r.states {
		out = append(out, s)
	}
	return out, nil
}

type mockLogger struct {
	mu     sync.Mutex
	infos  []string
	errors []string
}

func (l *mockLogger) LogInfo(_, _, msg string) {
	l.mu.Lock()
	l.infos = append(l.infos, msg)
	l.mu.Unlock()
}
func (l *mockLogger) LogError(_, _, msg string) {
	l.mu.Lock()
	l.errors = append(l.errors, msg)
	l.mu.Unlock()
}
func (l *mockLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *mockLogger) Close() error                                            { return nil }

type mockGit struct{}

func (g *mockGit) GetBranch(dir string) string { return "main" }
func (g *mockGit) GetProjectName(dir string) string {
	if dir == "" {
		return ""
	}
	return "project"
}
func (g *mockGit) GetGitRoot(dir string) string               { return "" }
func (g *mockGit) GetBranchFromTranscript(path string) string { return "" }
func (g *mockGit) GetCWDFromTranscript(path string) string    { return "" }

// cwdGit is a mockGit whose GetCWDFromTranscript returns a fixed cwd. It
// mimics the production fswatcher path, where EventNewSession carries no
// CWD and the cwd comes from transcript content (issue #576 rescue).
type cwdGit struct {
	mockGit
	cwd string
}

func (g *cwdGit) GetCWDFromTranscript(path string) string { return g.cwd }

type mockMetrics struct {
	pruned []string
}

func (m *mockMetrics) ComputeMetrics(path, adapter string) (*session.SessionMetrics, error) {
	return nil, nil
}

func (m *mockMetrics) ComputeMetricsTimeline(path, adapter string) ([]session.MetricsTimelinePoint, error) {
	return nil, nil
}

func (m *mockMetrics) PruneEntry(path string) { m.pruned = append(m.pruned, path) }

func (m *mockMetrics) IngestRateLimit(path string, snap *session.RateLimitSnapshot) {}

// funcMetrics is a metrics collector whose ComputeMetrics behaviour can be
// configured per test. Used to simulate a tailer that returns refreshed
// metrics for already-persisted sessions during seedFromDisk.
type funcMetrics struct {
	fn func(path, adapter string) (*session.SessionMetrics, error)
}

func (m *funcMetrics) ComputeMetrics(path, adapter string) (*session.SessionMetrics, error) {
	if m.fn == nil {
		return nil, nil
	}
	return m.fn(path, adapter)
}

func (m *funcMetrics) ComputeMetricsTimeline(path, adapter string) ([]session.MetricsTimelinePoint, error) {
	return nil, nil
}

func (m *funcMetrics) PruneEntry(path string) {}

func (m *funcMetrics) IngestRateLimit(path string, snap *session.RateLimitSnapshot) {}

// --- AgentWatcher mock -------------------------------------------------------

// mockAgentWatcher implements inbound.Watcher for tests.
type mockAgentWatcher struct {
	ch       chan agent.Event
	unsubs   int
	identity agent.Identity
}

// newMockAgentWatcher returns a mock watcher with the default test
// identity "claude-code". Tests targeting a different adapter override
// it via .withIdentity(). The default was chosen so existing tests that
// previously set Event.Adapter="claude-code" continue to receive the
// same identity tag from the SessionDetector's merge pipeline.
func newMockAgentWatcher() *mockAgentWatcher {
	return &mockAgentWatcher{
		ch:       make(chan agent.Event, 16),
		identity: agent.Identity{Name: "claude-code"},
	}
}

// withIdentity tags this watcher with an Identity so events it emits get
// the supplied adapter name when the SessionDetector wraps them in the
// merge pipeline. Tests that previously set Event.Adapter inline now call
// this on the watcher instead.
func (w *mockAgentWatcher) withIdentity(id agent.Identity) *mockAgentWatcher {
	w.identity = id
	return w
}

func (w *mockAgentWatcher) Identity() agent.Identity { return w.identity }

func (w *mockAgentWatcher) Watch(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (w *mockAgentWatcher) Subscribe() <-chan agent.Event {
	return w.ch
}

func (w *mockAgentWatcher) Unsubscribe(ch <-chan agent.Event) {
	w.unsubs++
}

// --- ProcessWatcher mock -----------------------------------------------------

// mockProcessWatcher implements outbound.ProcessWatcher for tests. The real
// pidMonitor.Watch/Unwatch are mutex-guarded and called concurrently (one
// goroutine per discovered session), so the mock must be thread-safe too — a
// bare map write here races under -race when two sessions are assigned at once.
type mockProcessWatcher struct {
	mu      sync.Mutex
	watched map[int]string
}

func newMockProcessWatcher() *mockProcessWatcher {
	return &mockProcessWatcher{watched: make(map[int]string)}
}

func (w *mockProcessWatcher) Watch(pid int, sessionID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watched[pid] = sessionID
	return nil
}

func (w *mockProcessWatcher) Unwatch(pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watched, pid)
}

func (w *mockProcessWatcher) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (w *mockProcessWatcher) Close() error { return nil }

// --- helper to build SessionDetector for tests --------------------------------

func newDetector(
	tw *mockAgentWatcher,
	pw *mockProcessWatcher,
	repo *mockRepo,
) *services.SessionDetector {
	return services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0, nil, nil, nil,
	)
}

// newDetectorWithMetrics builds a SessionDetector using a caller-provided
// MetricsCollector (e.g. a funcMetrics that returns refreshed token counts).
func newDetectorWithMetrics(
	tw *mockAgentWatcher,
	pw *mockProcessWatcher,
	repo *mockRepo,
	metrics *funcMetrics,
) *services.SessionDetector {
	return services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, metrics, nil,
		"test", 0, nil, nil, nil,
	)
}

// newDetectorWithLiveCWDs builds a SessionDetector with a process-name map
// and a live-CWD lookup injected, for stale-transcript rescue tests
// (issue #576). git may be nil, defaulting to mockGit (whose
// GetCWDFromTranscript returns "").
func newDetectorWithLiveCWDs(
	tw *mockAgentWatcher,
	pw *mockProcessWatcher,
	repo *mockRepo,
	git outbound.GitResolver,
	processNames map[string]string,
	liveCWDs services.LiveCWDsFunc,
) *services.SessionDetector {
	if git == nil {
		git = &mockGit{}
	}
	return services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, git, &mockMetrics{}, nil,
		"test", 0, nil, processNames, liveCWDs,
	)
}

// newDetectorWithCWDDiscovery builds a SessionDetector with a mock CWD-based
// PID discovery function registered for the "claude-code" adapter.
func newDetectorWithCWDDiscovery(
	tw *mockAgentWatcher,
	pw *mockProcessWatcher,
	repo *mockRepo,
	cwdFn func(string, func([]int) int) (int, error),
) *services.SessionDetector {
	discovers := map[string]agent.PIDDiscoverFunc{
		"claude-code": func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
			return cwdFn(cwd, disambiguate)
		},
	}
	return services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0, discovers, nil, nil,
	)
}
