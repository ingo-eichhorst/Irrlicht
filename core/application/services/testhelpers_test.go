package services_test

import (
	"context"
	"errors"
	"sync"
	"time"

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
	mu             sync.Mutex
	states         map[string]*session.SessionState
	lastSavedState map[string]string // sessionID → State at last Save; race-free snapshot for polling
	saves          int
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		states:         make(map[string]*session.SessionState),
		lastSavedState: make(map[string]string),
	}
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
	r.lastSavedState[s.SessionID] = s.State
	r.saves++
	return nil
}

func (r *mockRepo) Delete(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, sessionID)
	delete(r.lastSavedState, sessionID)
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

// pidOf reads a session's PID under r.mu. Background PID-discovery goroutines
// write state.PID on the shared *SessionState pointer, so a bare Load().PID
// read would race them (issue #606); this locked read is the race-free probe
// for waitForPID.
func (r *mockRepo) pidOf(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.states[sessionID]; ok {
		return s.PID
	}
	return 0
}

// eventCountOf reads a session's EventCount under r.mu — the race-free probe
// tests use to wait for an activity pass to complete (issue #606).
func (r *mockRepo) eventCountOf(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.states[sessionID]; ok {
		return s.EventCount
	}
	return 0
}

// savesCount reads the total Save count under r.mu — the race-free signal a
// test uses to wait out a processActivity pass that produces no observable
// field change (e.g. a no-op refresh that is gated from bumping EventCount).
func (r *mockRepo) savesCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saves
}

// updatedAtOf reads a session's UpdatedAt under r.mu (race-free; background
// goroutines mutate the shared *SessionState — issue #606).
func (r *mockRepo) updatedAtOf(sessionID string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.states[sessionID]; ok {
		return s.UpdatedAt
	}
	return 0
}

// waitForCondition polls fn until it returns true or the timeout elapses. The
// generic poll-for-condition replacement for fixed sleeps in tests whose
// completion signal isn't a saved State value (issue #606).
func waitForCondition(fn func() bool, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForPID polls until the session's PID is non-zero (PID discovery
// completed), or the timeout elapses. Lets tests wait for the detached
// discovery goroutine spawned by onNewSession/processActivity to finish its
// write before inspecting state — a fixed sleep is too short under
// parallel-load scheduling (issue #606).
func waitForPID(repo *mockRepo, sessionID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if repo.pidOf(sessionID) != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForSessionDeleted polls until the session no longer exists in the repo,
// or the timeout elapses. Used to wait out the same-PID cleanup that runs in a
// background discovery goroutine after a PID is assigned (issue #606).
func waitForSessionDeleted(repo *mockRepo, sessionID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		_, ok := repo.states[sessionID]
		repo.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForSessionState polls the repo's Save snapshot until the session was
// last saved with state want, or the timeout elapses. Lets tests wait for the
// detector's Run loop to finish async event processing without a fixed sleep
// (#578). Polling Load instead would race: the detector mutates session
// structs in place before calling Save, so the only race-free observation
// point is the state captured inside Save.
func waitForSessionState(repo *mockRepo, sessionID, want string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		got := repo.lastSavedState[sessionID]
		repo.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
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

// mockMetrics records PruneEntry calls. Safe for concurrent use: PruneEntry
// fires on the detector's Run goroutine while tests poll prunedSnapshot from
// the test goroutine (issue #606 flaky-sibling fix replaced a fixed sleep with
// a poll).
type mockMetrics struct {
	mu     sync.Mutex
	pruned []string
	purged []string
}

func (m *mockMetrics) ComputeMetrics(path, adapter string) (*session.SessionMetrics, error) {
	return nil, nil
}

func (m *mockMetrics) ComputeMetricsTimeline(path, adapter string) ([]session.MetricsTimelinePoint, error) {
	return nil, nil
}

func (m *mockMetrics) PruneEntry(path string) {
	m.mu.Lock()
	m.pruned = append(m.pruned, path)
	m.mu.Unlock()
}

// prunedSnapshot returns a race-free copy of the PruneEntry call log so tests
// can poll for the prune without racing the Run goroutine.
func (m *mockMetrics) prunedSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.pruned))
	copy(out, m.pruned)
	return out
}

func (m *mockMetrics) IngestRateLimit(path string, snap *session.RateLimitSnapshot) {}

func (m *mockMetrics) IngestTaskEstimate(path string, est *session.TaskEstimate) {}

func (m *mockMetrics) PurgeDeadBackgroundProcs(path string, _ []string) {
	m.mu.Lock()
	m.purged = append(m.purged, path)
	m.mu.Unlock()
}

func (m *mockMetrics) PurgeDeadBackgroundPIDs(path string, _ []string) {
	m.mu.Lock()
	m.purged = append(m.purged, path)
	m.mu.Unlock()
}

// purgedSnapshot returns a race-free copy of the PurgeDeadBackgroundProcs call
// log so tests can poll for the purge without racing the probe goroutine.
func (m *mockMetrics) purgedSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.purged))
	copy(out, m.purged)
	return out
}

// funcMetrics is a metrics collector whose ComputeMetrics behaviour can be
// configured per test. Used to simulate a tailer that returns refreshed
// metrics for already-persisted sessions during seedFromDisk.
type funcMetrics struct {
	fn func(path, adapter string) (*session.SessionMetrics, error)

	purgeMu     sync.Mutex
	purged      []string            // any purge call (path), kind-agnostic
	purgedProcs map[string][]string // path → dead output paths handed to PurgeDeadBackgroundProcs
	purgedPIDs  map[string][]string // path → dead PIDs handed to PurgeDeadBackgroundPIDs
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

func (m *funcMetrics) IngestTaskEstimate(path string, est *session.TaskEstimate) {}

func (m *funcMetrics) PurgeDeadBackgroundProcs(path string, outputs []string) {
	m.purgeMu.Lock()
	m.purged = append(m.purged, path)
	if m.purgedProcs == nil {
		m.purgedProcs = make(map[string][]string)
	}
	m.purgedProcs[path] = append([]string(nil), outputs...)
	m.purgeMu.Unlock()
}

func (m *funcMetrics) PurgeDeadBackgroundPIDs(path string, pids []string) {
	m.purgeMu.Lock()
	m.purged = append(m.purged, path)
	if m.purgedPIDs == nil {
		m.purgedPIDs = make(map[string][]string)
	}
	m.purgedPIDs[path] = append([]string(nil), pids...)
	m.purgeMu.Unlock()
}

// purgedSnapshot returns a race-free copy of the purge call log so tests can
// poll for the dead-verdict cleanup without racing the probe goroutine.
func (m *funcMetrics) purgedSnapshot() []string {
	m.purgeMu.Lock()
	defer m.purgeMu.Unlock()
	out := make([]string, len(m.purged))
	copy(out, m.purged)
	return out
}

// purgedProcsFor / purgedPIDsFor return the args of the last proc/PID purge for
// path (nil if none), so a mixed-process test can assert that only the dead
// kind was purged and with exactly its paths/PIDs. Race-free.
func (m *funcMetrics) purgedProcsFor(path string) (outputs []string, called bool) {
	m.purgeMu.Lock()
	defer m.purgeMu.Unlock()
	v, ok := m.purgedProcs[path]
	return append([]string(nil), v...), ok
}

func (m *funcMetrics) purgedPIDsFor(path string) (pids []string, called bool) {
	m.purgeMu.Lock()
	defer m.purgeMu.Unlock()
	v, ok := m.purgedPIDs[path]
	return append([]string(nil), v...), ok
}

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

// mockBroadcaster captures every Broadcast for assertions. Safe for
// concurrent use — the detector broadcasts from multiple goroutines.
type mockBroadcaster struct {
	mu   sync.Mutex
	msgs []outbound.PushMessage
}

func (b *mockBroadcaster) Broadcast(msg outbound.PushMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Deep-enough copy: the detector keeps mutating the *SessionState it
	// broadcasts, so snapshot the fields assertions need.
	if msg.Session != nil {
		s := *msg.Session
		msg.Session = &s
	}
	b.msgs = append(b.msgs, msg)
}

func (b *mockBroadcaster) Subscribe() chan outbound.PushMessage     { return nil }
func (b *mockBroadcaster) Unsubscribe(ch chan outbound.PushMessage) {}

// messages returns a snapshot of everything broadcast so far.
func (b *mockBroadcaster) messages() []outbound.PushMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]outbound.PushMessage, len(b.msgs))
	copy(out, b.msgs)
	return out
}

// newDetectorWithBroadcaster builds a SessionDetector whose pushes land in
// the returned mockBroadcaster (#593 summary-clearing assertions).
func newDetectorWithBroadcaster(
	tw *mockAgentWatcher,
	pw *mockProcessWatcher,
	repo *mockRepo,
) (*services.SessionDetector, *mockBroadcaster) {
	b := &mockBroadcaster{}
	det := services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, b,
		"test", 0, nil, nil, nil,
	)
	return det, b
}
