package services_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// --- mocks --------------------------------------------------------------

type mockPermStore struct {
	mu    sync.Mutex
	set   permission.Set
	saves int
}

func (s *mockPermStore) Load() (permission.Set, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil {
		return permission.Set{}, nil
	}
	return s.set, nil
}

func (s *mockPermStore) Save(set permission.Set) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set = set
	s.saves++
	return nil
}

func (s *mockPermStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

type mockPush struct {
	mu   sync.Mutex
	msgs []outbound.PushMessage
}

func (p *mockPush) Broadcast(m outbound.PushMessage) {
	p.mu.Lock()
	p.msgs = append(p.msgs, m)
	p.mu.Unlock()
}
func (p *mockPush) Subscribe() chan outbound.PushMessage     { return nil }
func (p *mockPush) Unsubscribe(ch chan outbound.PushMessage) {}

func (p *mockPush) count(typ string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, m := range p.msgs {
		if m.Type == typ {
			n++
		}
	}
	return n
}

type mockRegistrar struct {
	mu    sync.Mutex
	added []inbound.Watcher
}

func (r *mockRegistrar) AddWatcher(ctx context.Context, w inbound.Watcher) {
	r.mu.Lock()
	r.added = append(r.added, w)
	r.mu.Unlock()
}

func (r *mockRegistrar) addedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.added)
}

// signalWatcher reports when its Watch loop starts and stops.
type signalWatcher struct {
	*mockAgentWatcher
	started chan struct{}
	stopped chan struct{}
}

func newSignalWatcher() *signalWatcher {
	return &signalWatcher{
		mockAgentWatcher: newMockAgentWatcher(),
		started:          make(chan struct{}),
		stopped:          make(chan struct{}),
	}
}

func (w *signalWatcher) Watch(ctx context.Context) error {
	close(w.started)
	<-ctx.Done()
	close(w.stopped)
	return ctx.Err()
}

// --- helpers ------------------------------------------------------------

type effectCounter struct {
	mu      sync.Mutex
	applied int
	removed int
	last    string // "apply" or "remove" — the last executed closure effect
}

func (c *effectCounter) apply() error {
	c.mu.Lock()
	c.applied++
	c.last = "apply"
	c.mu.Unlock()
	return nil
}

func (c *effectCounter) remove() error {
	c.mu.Lock()
	c.removed++
	c.last = "remove"
	c.mu.Unlock()
	return nil
}

func (c *effectCounter) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applied, c.removed
}

func (c *effectCounter) lastEffect() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func testAgentDecl(c *effectCounter) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
		Process:  agent.Process{Match: agent.ExactName{Name: "testagent"}},
		Permissions: []agent.Permission{
			{Key: "config", Kind: permission.KindModify, Title: "Modify config",
				Apply: c.apply, Remove: c.remove},
			{Key: "transcripts", Kind: permission.KindObserve, Title: "Read transcripts"},
		},
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- tests --------------------------------------------------------------

func TestPermissionServiceFreshStateIsAllPendingAndInert(t *testing.T) {
	c := &effectCounter{}
	store := &mockPermStore{}
	reg := &mockRegistrar{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     store,
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: reg,
		Factories: nil,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	if svc.Granted("testagent", "config") || svc.Granted("testagent", "transcripts") {
		t.Fatal("fresh state must not grant anything")
	}
	applied, removed := c.counts()
	if applied != 0 || removed != 0 {
		t.Fatalf("fresh start ran effects: applied=%d removed=%d", applied, removed)
	}
	if reg.addedCount() != 0 {
		t.Fatal("fresh start registered watchers")
	}
	snap := svc.Snapshot()
	if snap.Mode != config.PermissionModeAsk {
		t.Fatalf("mode = %q", snap.Mode)
	}
	for _, a := range snap.Agents {
		for _, p := range a.Permissions {
			if p.State != string(permission.StatePending) {
				t.Fatalf("%s/%s state = %q, want pending", a.Name, p.Key, p.State)
			}
		}
	}
}

func TestPermissionServiceGrantAppliesAndDenyRemoves(t *testing.T) {
	c := &effectCounter{}
	store := &mockPermStore{}
	push := &mockPush{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     store,
		Push:      push,
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "config", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if applied, _ := c.counts(); applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if !svc.Granted("testagent", "config") {
		t.Fatal("not granted after grant")
	}
	if store.saveCount() != 1 {
		t.Fatalf("saves = %d, want 1", store.saveCount())
	}
	if push.count(outbound.PushTypePermissionsUpdated) != 1 {
		t.Fatal("expected one permissions_updated broadcast")
	}

	// Revoke actively undoes.
	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "config", Grant: false},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if _, removed := c.counts(); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if svc.Granted("testagent", "config") {
		t.Fatal("still granted after deny")
	}
}

func TestPermissionServiceReAnswerIsIdempotent(t *testing.T) {
	c := &effectCounter{}
	store := &mockPermStore{}
	push := &mockPush{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     store,
		Push:      push,
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	ans := []services.PermissionAnswer{{Agent: "testagent", Permission: "config", Grant: true}}
	if err := svc.Answer(ans); err != nil {
		t.Fatal(err)
	}
	// The losing surface submits the same answer after the race — no-op.
	if err := svc.Answer(ans); err != nil {
		t.Fatal(err)
	}
	if applied, _ := c.counts(); applied != 1 {
		t.Fatalf("applied = %d, want 1 (re-answer must not re-run effects)", applied)
	}
	if store.saveCount() != 1 {
		t.Fatalf("saves = %d, want 1", store.saveCount())
	}
	if push.count(outbound.PushTypePermissionsUpdated) != 1 {
		t.Fatal("re-answer must not re-broadcast")
	}
}

func TestPermissionServiceUnknownPermissionRejectsWholeBatch(t *testing.T) {
	c := &effectCounter{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     &mockPermStore{},
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "config", Grant: true},
		{Agent: "testagent", Permission: "nope", Grant: true},
	})
	if !errors.Is(err, services.ErrUnknownPermission) {
		t.Fatalf("err = %v, want ErrUnknownPermission", err)
	}
	// Nothing from the batch applied.
	if applied, _ := c.counts(); applied != 0 {
		t.Fatalf("applied = %d, want 0", applied)
	}
	if svc.Granted("testagent", "config") {
		t.Fatal("valid entry of invalid batch was applied")
	}
}

func TestPermissionServiceObserveGrantStartsAndDenyStopsWatchers(t *testing.T) {
	c := &effectCounter{}
	w := newSignalWatcher()
	reg := &mockRegistrar{}
	factories := map[string]services.WatcherFactory{
		"testagent": func() []inbound.Watcher { return []inbound.Watcher{w} },
	}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     &mockPermStore{},
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: reg,
		Factories: factories,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "transcripts", Grant: true},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "watcher start", func() bool {
		select {
		case <-w.started:
			return true
		default:
			return false
		}
	})
	if reg.addedCount() != 1 {
		t.Fatalf("registered watchers = %d, want 1", reg.addedCount())
	}

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "transcripts", Grant: false},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop after deny")
	}
}

func TestPermissionServiceGrantedStateResumesAtStart(t *testing.T) {
	c := &effectCounter{}
	set := permission.Set{}
	set.Put("testagent", "config", permission.StateGranted)
	set.Put("testagent", "transcripts", permission.StateGranted)
	store := &mockPermStore{set: set}
	w := newSignalWatcher()
	factories := map[string]services.WatcherFactory{
		"testagent": func() []inbound.Watcher { return []inbound.Watcher{w} },
	}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     store,
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: factories,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	// Persisted grants are exercised on boot: modify re-applied (hook
	// upgrade path), observe watchers started.
	if applied, _ := c.counts(); applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	waitFor(t, "watcher start", func() bool {
		select {
		case <-w.started:
			return true
		default:
			return false
		}
	})
}

func TestPermissionServiceGrantAllMode(t *testing.T) {
	c := &effectCounter{}
	store := &mockPermStore{}
	w := newSignalWatcher()
	factories := map[string]services.WatcherFactory{
		"testagent": func() []inbound.Watcher { return []inbound.Watcher{w} },
	}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     store,
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeGrantAll,
		Registrar: &mockRegistrar{},
		Factories: factories,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	if !svc.Granted("testagent", "config") || !svc.Granted("testagent", "transcripts") {
		t.Fatal("grant-all must grant everything")
	}
	if applied, _ := c.counts(); applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	waitFor(t, "watcher start", func() bool {
		select {
		case <-w.started:
			return true
		default:
			return false
		}
	})
	// Auto-grants are in-memory only: a later ask-mode daemon must still
	// see the user's real (unanswered) state.
	if store.saveCount() != 0 {
		t.Fatalf("grant-all persisted auto-grants (saves=%d)", store.saveCount())
	}
}

func TestPermissionServiceDetectionFlagsAndBroadcasts(t *testing.T) {
	c := &effectCounter{}
	push := &mockPush{}
	var mu sync.Mutex
	live := false
	hasLive := func(agent.ProcessMatcher) bool {
		mu.Lock()
		defer mu.Unlock()
		return live
	}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     &mockPermStore{},
		Push:      push,
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   hasLive,
	})
	svc.SetDetectionPollIntervalForTest(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)

	// Not live: detected stays false, no broadcast.
	time.Sleep(20 * time.Millisecond)
	if push.count(outbound.PushTypePermissionsUpdated) != 0 {
		t.Fatal("broadcast while nothing detected")
	}

	mu.Lock()
	live = true
	mu.Unlock()
	waitFor(t, "detection broadcast", func() bool {
		return push.count(outbound.PushTypePermissionsUpdated) >= 1
	})
	snap := svc.Snapshot()
	if len(snap.Agents) != 1 || !snap.Agents[0].Detected {
		t.Fatalf("snapshot not detected: %+v", snap.Agents)
	}
}

func TestPermissionServiceGrantAllAnswerDoesNotPersist(t *testing.T) {
	c := &effectCounter{}
	store := &mockPermStore{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     store,
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeGrantAll,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	// A user answer on a grant-all daemon applies in memory but must not
	// write the auto-granted set to disk — a later ask-mode daemon on the
	// same home would otherwise inherit grants the user never gave it.
	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "config", Grant: false},
	}); err != nil {
		t.Fatal(err)
	}
	if svc.Granted("testagent", "config") {
		t.Fatal("deny not applied in memory")
	}
	if store.saveCount() != 0 {
		t.Fatalf("grant-all Answer persisted state (saves=%d)", store.saveCount())
	}
}

func TestPermissionServiceObserveGranted(t *testing.T) {
	c := &effectCounter{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     &mockPermStore{},
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	if svc.ObserveGranted("testagent") {
		t.Fatal("pending observe permission must read as not granted")
	}
	// Granting the MODIFY permission must not unlock observe reads.
	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "config", Grant: true},
	}); err != nil {
		t.Fatal(err)
	}
	if svc.ObserveGranted("testagent") {
		t.Fatal("modify grant must not satisfy the observe gate")
	}
	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "transcripts", Grant: true},
	}); err != nil {
		t.Fatal(err)
	}
	if !svc.ObserveGranted("testagent") {
		t.Fatal("observe grant not reflected")
	}
	// Unknown adapters default closed.
	if svc.ObserveGranted("ghost") {
		t.Fatal("unknown adapter must read as not granted")
	}
}

func TestPermissionServiceDetectionStopsWhenNothingPending(t *testing.T) {
	c := &effectCounter{}
	set := permission.Set{}
	set.Put("testagent", "config", permission.StateGranted)
	set.Put("testagent", "transcripts", permission.StateDenied)
	var mu sync.Mutex
	calls := 0
	hasLive := func(agent.ProcessMatcher) bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return true
	}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     &mockPermStore{set: set},
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   hasLive,
	})
	svc.SetDetectionPollIntervalForTest(5 * time.Millisecond)
	svc.Start(context.Background())

	// Everything is answered at boot — the poller must not run at all.
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("detection polled %d times with nothing pending", calls)
	}
}

func TestPermissionServiceDetectionProbeOverride(t *testing.T) {
	c := &effectCounter{}
	push := &mockPush{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     &mockPermStore{},
		Push:      push,
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   func(agent.ProcessMatcher) bool { return false },
	})
	svc.SetDetectionProbe("testagent", func() bool { return true })
	svc.SetDetectionPollIntervalForTest(5 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)

	waitFor(t, "probe-driven detection", func() bool {
		snap := svc.Snapshot()
		return len(snap.Agents) == 1 && snap.Agents[0].Detected
	})
}

// TestPermissionServiceConflictingAnswersConverge stresses the race where
// both surfaces answer the SAME permission near-simultaneously with
// opposite decisions. State mutations serialize under the state mutex and
// effect batches under the effect mutex, so without the stale-effect skip
// the last EXECUTED effect could belong to the losing answer (state
// granted but Remove ran last, or vice versa). The invariant: after both
// answers settle, the last executed closure effect always matches the
// recorded state.
func TestPermissionServiceConflictingAnswersConverge(t *testing.T) {
	c := &effectCounter{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     &mockPermStore{},
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   nil,
	})
	svc.Start(context.Background())

	for i := 0; i < 100; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = svc.Answer([]services.PermissionAnswer{{Agent: "testagent", Permission: "config", Grant: true}})
		}()
		go func() {
			defer wg.Done()
			_ = svc.Answer([]services.PermissionAnswer{{Agent: "testagent", Permission: "config", Grant: false}})
		}()
		wg.Wait()

		granted := svc.Granted("testagent", "config")
		last := c.lastEffect()
		if granted && last != "apply" {
			t.Fatalf("round %d: state granted but last executed effect was %q", i, last)
		}
		if !granted && last != "remove" {
			t.Fatalf("round %d: state denied but last executed effect was %q", i, last)
		}
	}
}
