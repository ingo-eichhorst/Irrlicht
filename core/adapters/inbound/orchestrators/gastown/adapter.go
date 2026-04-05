// Package gastown implements the OrchestratorWatcher for Gas Town.
//
// It wraps the Collector (filesystem detection + daemon state watching) and
// Poller (gt CLI queries for rigs, polecats, convoys) into a single adapter
// that produces standardised orchestrator.State snapshots.
package gastown

import (
	"context"
	"reflect"
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
// It backs off from the base interval to 3× when the state is stable.
func (a *Adapter) runPoller(ctx context.Context, p *Poller) error {
	// Initial poll.
	a.setState(p.BuildOrchestratorState(ctx))

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	stablePolls := 0
	currentInterval := a.interval
	backoffInterval := a.interval * 3 // e.g. 5s → 15s

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			newState := p.BuildOrchestratorState(ctx)

			a.mu.RLock()
			changed := a.state == nil || stateChanged(a.state, newState)
			a.mu.RUnlock()

			a.setState(newState)

			if changed {
				stablePolls = 0
			} else {
				stablePolls++
			}

			var targetInterval time.Duration
			if stablePolls >= 3 {
				targetInterval = backoffInterval
			} else {
				targetInterval = a.interval
			}
			if targetInterval != currentInterval {
				ticker.Reset(targetInterval)
				currentInterval = targetInterval
			}
		}
	}
}

// stateChanged returns true if meaningful fields differ between two states.
// Uses reflect.DeepEqual for correctness — catches content changes within
// same-length slices (e.g., agent status transitions).
func stateChanged(prev, curr *orchestrator.State) bool {
	if prev.Running != curr.Running {
		return true
	}
	if !reflect.DeepEqual(prev.Codebases, curr.Codebases) ||
		!reflect.DeepEqual(prev.GlobalAgents, curr.GlobalAgents) ||
		!reflect.DeepEqual(prev.WorkUnits, curr.WorkUnits) {
		return true
	}
	return false
}
