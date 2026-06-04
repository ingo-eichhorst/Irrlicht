// Package gastown implements the OrchestratorWatcher for Gas Town.
//
// It wraps the collector (filesystem detection + daemon state watching) and
// poller (gt CLI queries for rigs, polecats, convoys) into a single adapter
// that produces standardised orchestrator.State snapshots.
package gastown

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"irrlicht/core/domain/orchestrator"
)

// Adapter implements inbound.OrchestratorWatcher for Gas Town.
type Adapter struct {
	collector *collector
	gtBin     string
	sessions  sessionLister

	mu    sync.RWMutex
	state *orchestrator.State

	subMu sync.Mutex
	subs  []chan orchestrator.State
}

// NewAdapter creates a Gas Town OrchestratorWatcher adapter.
// gtBin is the resolved path to the gt binary (may be empty).
// sessions provides active session data for CWD matching (may be nil).
func NewAdapter(gtBin string, sessions sessionLister) *Adapter {
	return &Adapter{
		collector: New(),
		gtBin:     gtBin,
		sessions:  sessions,
	}
}

// Name returns the orchestrator identifier.
func (a *Adapter) Name() string { return Name }

// Detected returns true if a valid Gas Town installation was found.
func (a *Adapter) Detected() bool { return a.collector.Detected() }

// Root returns the resolved GT_ROOT path, or "" if not detected.
func (a *Adapter) Root() string { return a.collector.Root() }

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
	poller := newPoller(a.collector, a.gtBin, a.sessions)
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
			daemon := a.collector.daemonState()
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

// pollCooldown is the minimum interval between event-driven polls to prevent
// back-to-back CLI spawning when files change rapidly (e.g. during git operations).
const pollCooldown = 2 * time.Second

// runPoller runs the gt CLI poller and converts its output to orchestrator.State.
// It fires immediately when the collector detects a filesystem change, and falls
// back to a 30 s tick to catch polecat/dog/boot state that the collector doesn't watch.
// A 2 s cooldown prevents back-to-back CLI spawning on rapid writes. State is only
// broadcast when it actually changes so idle workspaces produce no downstream work.
func (a *Adapter) runPoller(ctx context.Context, p *poller) error {
	a.setState(p.BuildOrchestratorState(ctx))

	onChange := p.collector.OnChange()
	fallback := time.NewTicker(30 * time.Second)
	defer fallback.Stop()
	lastPoll := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-onChange:
			if gap := pollCooldown - time.Since(lastPoll); gap > 0 {
				select {
				case <-time.After(gap):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			// Drain a fallback tick that may have queued during the cooldown wait
			// to avoid an immediate redundant poll right after this one.
			select {
			case <-fallback.C:
			default:
			}
		case <-fallback.C:
		}
		newState := p.BuildOrchestratorState(ctx)
		lastPoll = time.Now()
		a.mu.RLock()
		changed := stateChanged(a.state, newState)
		a.mu.RUnlock()
		if changed {
			a.setState(newState)
		}
	}
}

// stateChanged reports whether meaningful fields differ between two states.
// WorkUnits is included even though it is not currently populated, so future
// GT commands that set it will broadcast correctly without a code change here.
func stateChanged(prev, curr *orchestrator.State) bool {
	if prev == nil || prev.Running != curr.Running {
		return true
	}
	prevB, err := json.Marshal([3]any{prev.Codebases, prev.GlobalAgents, prev.WorkUnits})
	if err != nil {
		return true // treat as changed so broadcasts are never silently suppressed
	}
	currB, err := json.Marshal([3]any{curr.Codebases, curr.GlobalAgents, curr.WorkUnits})
	if err != nil {
		return true
	}
	return string(prevB) != string(currB)
}

