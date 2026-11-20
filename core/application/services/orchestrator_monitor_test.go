package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/orchestrator"
)

// orchTestLog is a no-op outbound.Logger for the monitor tests.
type orchTestLog struct{}

func (orchTestLog) LogInfo(_, _, _ string)                                  {}
func (orchTestLog) LogError(_, _, _ string)                                 {}
func (orchTestLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (orchTestLog) Close() error                                            { return nil }

// fakeOrchWatcher is an inbound.OrchestratorWatcher whose snapshots the test
// emits by hand.
type fakeOrchWatcher struct {
	name string
	mu   sync.Mutex
	subs []chan orchestrator.State
}

func (f *fakeOrchWatcher) Name() string                          { return f.name }
func (f *fakeOrchWatcher) Detected() bool                        { return true }
func (f *fakeOrchWatcher) State() *orchestrator.State            { return nil }
func (f *fakeOrchWatcher) Watch(ctx context.Context) error       { <-ctx.Done(); return ctx.Err() }
func (f *fakeOrchWatcher) Unsubscribe(<-chan orchestrator.State) {}

func (f *fakeOrchWatcher) Subscribe() <-chan orchestrator.State {
	ch := make(chan orchestrator.State, 1)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch
}

// emit hands s to every subscriber, failing the test if one has not taken
// the previous snapshot yet: a dropped emit would make a later assertion
// pass or fail for the wrong reason.
func (f *fakeOrchWatcher) emit(t *testing.T, s orchestrator.State) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.subs) == 0 {
		t.Fatalf("%s: emit with no subscriber", f.name)
	}
	for _, ch := range f.subs {
		select {
		case ch <- s:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: subscriber did not take the previous snapshot within 2s", f.name)
		}
	}
}

// waitSubscribed waits until the monitor's drain goroutine has subscribed.
func (f *fakeOrchWatcher) waitSubscribed(t *testing.T) {
	t.Helper()
	waitOrch(t, f.name+" subscribed", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.subs) > 0
	})
}

// waitOrch polls cond to a 2s deadline and fails with the elapsed time.
func waitOrch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: not observed after %v", what, time.Since(start))
}

// startOrchRun runs m until the test ends.
func startOrchRun(t *testing.T, m *OrchestratorMonitor) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = m.Run(ctx) }()
}

// queueSnapshot registers w under ctx, emits s, and waits until the drain
// goroutine has parked s in the merged channel. With Run not started yet, s
// is then a snapshot in flight: forwarded by the drain, not yet stored.
func queueSnapshot(t *testing.T, m *OrchestratorMonitor, ctx context.Context, w *fakeOrchWatcher, s orchestrator.State) {
	t.Helper()
	before := len(m.merged)
	m.AddWatcher(ctx, w)
	w.waitSubscribed(t)
	w.emit(t, s)
	waitOrch(t, w.name+" snapshot queued", func() bool { return len(m.merged) == before+1 })
}

// Issue #2148 — revoking an orchestrator's permission cancels its watch
// context, and from then on the monitor must not serve that orchestrator's
// snapshot. Before the fix Run stored states[adapter] and nothing ever deleted
// it, so /api/v1/sessions kept rendering a revoked orchestrator.
//
// Red-first on 796a07307: this file compiles there (it uses only AddWatcher,
// Run and State) and the first two tests fail; the third is a lock there.

// The drop is synchronous: no poll between ending the registration and the
// assertion, because the permission service's revoke returns as soon as the
// stop effect has cancelled the context.
func TestOrchestratorMonitor_RegistrationEndDropsStateAtOnce(t *testing.T) {
	m := NewOrchestratorMonitor(nil, nil, orchTestLog{})
	startOrchRun(t, m)

	w := &fakeOrchWatcher{name: "workerlist"}
	watchCtx, endRegistration := context.WithCancel(context.Background())
	defer endRegistration()
	m.AddWatcher(watchCtx, w)
	w.waitSubscribed(t)
	w.emit(t, orchestrator.State{Adapter: "workerlist", Running: true})
	waitOrch(t, "state stored", func() bool { return m.State("workerlist") != nil })

	endRegistration()
	if m.State("workerlist") != nil {
		t.Fatal("State(workerlist) still served right after its registration ended")
	}
}

// A snapshot the drain had already forwarded when the registration ended is
// stored by Run afterwards; it must not bring the revoked orchestrator back.
func TestOrchestratorMonitor_SnapshotInFlightAtRevokeStaysDropped(t *testing.T) {
	m := NewOrchestratorMonitor(nil, nil, orchTestLog{})
	revoked := &fakeOrchWatcher{name: "workerlist"}
	watchCtx, endRegistration := context.WithCancel(context.Background())
	defer endRegistration()
	queueSnapshot(t, m, watchCtx, revoked, orchestrator.State{Adapter: "workerlist", Running: true})
	endRegistration()

	sentinel := &fakeOrchWatcher{name: "gastown"}
	liveCtx, stopLive := context.WithCancel(context.Background())
	defer stopLive()
	queueSnapshot(t, m, liveCtx, sentinel, orchestrator.State{Adapter: "gastown", Running: true})
	startOrchRun(t, m)
	// merged is one FIFO channel and the revoked snapshot was queued before the
	// sentinel's, so once the sentinel is stored Run has handled both.
	waitOrch(t, "sentinel stored", func() bool { return m.State("gastown") != nil })
	if m.State("workerlist") != nil {
		t.Fatal("a snapshot in flight at revoke was stored and served after the registration ended")
	}
}

// LOCK on 796a07307 (it never dropped anything, so a re-grant stored again):
// a new registration under the same adapter name serves its own snapshots,
// and the old registration's late snapshot, queued before them, does not
// replace them. Guards a drop keyed on the adapter name instead of on the
// registration.
func TestOrchestratorMonitor_RegrantServesNewRegistrationOnly(t *testing.T) {
	m := NewOrchestratorMonitor(nil, nil, orchTestLog{})
	oldCtx, endOld := context.WithCancel(context.Background())
	defer endOld()
	queueSnapshot(t, m, oldCtx, &fakeOrchWatcher{name: "workerlist"},
		orchestrator.State{Adapter: "workerlist", Running: true, Root: "/old"})
	endOld()

	newCtx, endNew := context.WithCancel(context.Background())
	defer endNew()
	queueSnapshot(t, m, newCtx, &fakeOrchWatcher{name: "workerlist"},
		orchestrator.State{Adapter: "workerlist", Running: true, Root: "/new"})
	startOrchRun(t, m)

	waitOrch(t, "re-granted state stored", func() bool { return m.State("workerlist") != nil })
	if got := m.State("workerlist").Root; got != "/new" {
		t.Errorf("State(workerlist).Root = %q, want /new (the re-granted registration's snapshot)", got)
	}
}

// An ended registration's late snapshot that reaches Run AFTER the live
// registration's must not replace it: the orchestrator would vanish until its
// next snapshot. Red-first on 796a07307, which stored whatever came last.
func TestOrchestratorMonitor_EndedSnapshotDoesNotReplaceLiveOne(t *testing.T) {
	m := NewOrchestratorMonitor(nil, nil, orchTestLog{})
	liveCtx, endLive := context.WithCancel(context.Background())
	defer endLive()
	queueSnapshot(t, m, liveCtx, &fakeOrchWatcher{name: "workerlist"},
		orchestrator.State{Adapter: "workerlist", Running: true, Root: "/live"})
	endedCtx, endEnded := context.WithCancel(context.Background())
	defer endEnded()
	queueSnapshot(t, m, endedCtx, &fakeOrchWatcher{name: "workerlist"},
		orchestrator.State{Adapter: "workerlist", Running: true, Root: "/ended"})
	endEnded()
	sentinelCtx, stopSentinel := context.WithCancel(context.Background())
	defer stopSentinel()
	queueSnapshot(t, m, sentinelCtx, &fakeOrchWatcher{name: "gastown"},
		orchestrator.State{Adapter: "gastown", Running: true})
	startOrchRun(t, m)

	waitOrch(t, "sentinel stored", func() bool { return m.State("gastown") != nil })
	got := m.State("workerlist")
	if got == nil || got.Root != "/live" {
		t.Fatalf("State(workerlist) = %+v, want the live registration's snapshot (Root /live)", got)
	}
}
