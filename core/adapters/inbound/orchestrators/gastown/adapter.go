// Package gastown implements the OrchestratorWatcher for Gas Town.
//
// It wraps the Collector (filesystem detection + daemon state watching) and
// Poller (gt CLI queries for rigs, polecats, convoys) into a single adapter
// that produces standardised orchestrator.State snapshots.
package gastown

import (
	"context"
	"sync"
	"time"

	"irrlicht/core/domain/orchestrator"
)

// Adapter implements inbound.OrchestratorWatcher for Gas Town.
type Adapter struct {
	collector *Collector
	gtBin     string
	interval  time.Duration
	sessions  SessionLister

	mu    sync.RWMutex
	state *orchestrator.State

	subMu sync.Mutex
	subs  []chan orchestrator.State
}

// NewAdapter creates a Gas Town OrchestratorWatcher adapter.
// gtBin is the resolved path to the gt binary (may be empty).
// interval is how often the poller queries gt CLI.
// sessions provides active session data for CWD matching (may be nil).
func NewAdapter(gtBin string, interval time.Duration, sessions SessionLister) *Adapter {
	return &Adapter{
		collector: New(),
		gtBin:     gtBin,
		interval:  interval,
		sessions:  sessions,
	}
}

// Name returns the orchestrator identifier.
func (a *Adapter) Name() string { return "gastown" }

// Detected returns true if a valid Gas Town installation was found.
func (a *Adapter) Detected() bool { return a.collector.Detected() }

// Root returns the resolved GT_ROOT path, or "" if not detected.
func (a *Adapter) Root() string { return a.collector.Root() }

// Collector returns the underlying Collector (needed for legacy REST handler
// until fully migrated).
func (a *Adapter) Collector() *Collector { return a.collector }

// State returns the latest orchestrator state snapshot, or nil.
func (a *Adapter) State() *orchestrator.State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state != nil {
		cp := *a.state
		return &cp
	}
	return nil
}

// Watch starts the collector watcher and poller, blocks until ctx is cancelled.
func (a *Adapter) Watch(ctx context.Context) error {
	if !a.collector.Detected() {
		<-ctx.Done()
		return ctx.Err()
	}

	// Start collector watch in background.
	go func() {
		_ = a.collector.Watch(ctx)
	}()

	// If no gt binary, just watch daemon state and emit minimal snapshots.
	if a.gtBin == "" {
		return a.watchDaemonOnly(ctx)
	}

	// Run poller loop.
	poller := NewPoller(a.collector, a.gtBin, a.interval, a.sessions)
	return a.runPoller(ctx, poller)
}

// Subscribe returns a channel that receives orchestrator state snapshots.
func (a *Adapter) Subscribe() <-chan orchestrator.State {
	ch := make(chan orchestrator.State, 1)
	a.subMu.Lock()
	a.subs = append(a.subs, ch)
	a.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (a *Adapter) Unsubscribe(ch <-chan orchestrator.State) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for i, s := range a.subs {
		if s == ch {
			a.subs = append(a.subs[:i], a.subs[i+1:]...)
			close(s)
			return
		}
	}
}

// broadcast sends a state snapshot to all subscribers (non-blocking).
func (a *Adapter) broadcast(state orchestrator.State) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for _, ch := range a.subs {
		select {
		case ch <- state:
		default:
		}
	}
}

// setState stores a new state and broadcasts it.
func (a *Adapter) setState(state *orchestrator.State) {
	a.mu.Lock()
	a.state = state
	a.mu.Unlock()
	a.broadcast(*state)
}

// watchDaemonOnly watches daemon state without the poller (no gt binary).
func (a *Adapter) watchDaemonOnly(ctx context.Context) error {
	ch := a.collector.Subscribe()
	defer a.collector.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			now := time.Now().UTC()
			daemon := a.collector.DaemonState()
			running := daemon != nil && daemon.Running
			a.setState(&orchestrator.State{
				Adapter:   "gastown",
				Running:   running,
				Root:      a.collector.Root(),
				UpdatedAt: now,
			})
		}
	}
}

// runPoller runs the gt CLI poller and converts its output to orchestrator.State.
func (a *Adapter) runPoller(ctx context.Context, p *Poller) error {
	// Initial poll.
	a.setState(p.BuildOrchestratorState(ctx))

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			a.setState(p.BuildOrchestratorState(ctx))
		}
	}
}
