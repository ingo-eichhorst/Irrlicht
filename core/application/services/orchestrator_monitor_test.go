package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// chanPush is a race-safe PushBroadcaster that forwards every broadcast to a
// buffered channel so tests can observe broadcasts emitted from Run's goroutine.
type chanPush struct{ got chan outbound.PushMessage }

func newChanPush() *chanPush { return &chanPush{got: make(chan outbound.PushMessage, 8)} }

func (p *chanPush) Broadcast(m outbound.PushMessage)        { p.got <- m }
func (p *chanPush) Subscribe() chan outbound.PushMessage    { return make(chan outbound.PushMessage) }
func (p *chanPush) Unsubscribe(_ chan outbound.PushMessage) {}

// fakeOrchWatcher feeds states through a channel the test controls.
type fakeOrchWatcher struct{ ch chan orchestrator.State }

func (w *fakeOrchWatcher) Name() string                            { return "gastown" }
func (w *fakeOrchWatcher) Detected() bool                          { return true }
func (w *fakeOrchWatcher) Watch(ctx context.Context) error         { <-ctx.Done(); return ctx.Err() }
func (w *fakeOrchWatcher) Subscribe() <-chan orchestrator.State    { return w.ch }
func (w *fakeOrchWatcher) Unsubscribe(_ <-chan orchestrator.State) {}
func (w *fakeOrchWatcher) State() *orchestrator.State              { return nil }

func TestOrchestratorMonitor_BroadcastsStateUpdates(t *testing.T) {
	watcher := &fakeOrchWatcher{ch: make(chan orchestrator.State, 1)}
	push := newChanPush()
	mon := services.NewOrchestratorMonitor([]inbound.OrchestratorWatcher{watcher}, push, stubLog{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mon.Run(ctx)

	watcher.ch <- orchestrator.State{Adapter: "gastown", Running: true}

	select {
	case msg := <-push.got:
		if msg.Type != outbound.PushTypeOrchestratorState {
			t.Fatalf("want type %q, got %q", outbound.PushTypeOrchestratorState, msg.Type)
		}
		if msg.Orchestrator == nil || msg.Orchestrator.Adapter != "gastown" || !msg.Orchestrator.Running {
			t.Fatalf("orchestrator payload not carried: %+v", msg.Orchestrator)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for orchestrator_state broadcast")
	}

	// The latest state is also queryable via State().
	if s := mon.State("gastown"); s == nil || !s.Running {
		t.Fatalf("State(gastown) = %+v, want running", s)
	}
}
