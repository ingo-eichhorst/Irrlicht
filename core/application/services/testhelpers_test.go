package services_test

import (
	"errors"
	"sync"

	"irrlicht/core/domain/session"
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
func (g *mockGit) GetBranchFromTranscript(path string) string { return "" }

type mockMetrics struct{}

func (m *mockMetrics) ComputeMetrics(path string) (*session.SessionMetrics, error) {
	return nil, nil
}
