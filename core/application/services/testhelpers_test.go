package services_test

import (
	"context"
	"errors"
	"sync"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

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
func (g *mockGit) GetGitRoot(dir string) string                { return "" }
func (g *mockGit) GetBranchFromTranscript(path string) string  { return "" }
func (g *mockGit) GetCWDFromTranscript(path string) string     { return "" }

type mockMetrics struct{}

func (m *mockMetrics) ComputeMetrics(path string) (*session.SessionMetrics, error) {
	return nil, nil
}

// --- AgentWatcher mock -------------------------------------------------------

// mockAgentWatcher implements inbound.AgentWatcher for tests.
type mockAgentWatcher struct {
	ch     chan agent.Event
	unsubs int
}

func newMockAgentWatcher() *mockAgentWatcher {
	return &mockAgentWatcher{
		ch: make(chan agent.Event, 16),
	}
}

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

// mockProcessWatcher implements outbound.ProcessWatcher for tests.
type mockProcessWatcher struct {
	watched map[int]string
}

func newMockProcessWatcher() *mockProcessWatcher {
	return &mockProcessWatcher{watched: make(map[int]string)}
}

func (w *mockProcessWatcher) Watch(pid int, sessionID string) error {
	w.watched[pid] = sessionID
	return nil
}

func (w *mockProcessWatcher) Unwatch(pid int) {
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
		[]inbound.AgentWatcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0,
	)
}

// newDetectorWithCWDDiscovery builds a SessionDetector with a mock CWD-based
// PID discovery function wired up via WithCWDDiscovery.
func newDetectorWithCWDDiscovery(
	tw *mockAgentWatcher,
	pw *mockProcessWatcher,
	repo *mockRepo,
	cwdFn func(string, func([]int) int) (int, error),
) *services.SessionDetector {
	det := services.NewSessionDetector(
		[]inbound.AgentWatcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0,
	)
	det.WithCWDDiscovery(cwdFn)
	return det
}
