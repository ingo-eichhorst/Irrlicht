// OrchestratorMonitor subscribes to OrchestratorWatchers and broadcasts
// state updates via PushBroadcaster. It mirrors SessionDetector's pattern
// for the orchestrator category of inbound adapters.
package services

import (
	"context"
	"fmt"
	"sync"

	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// OrchestratorMonitor watches all OrchestratorWatchers and broadcasts
// state updates to WebSocket clients.
type OrchestratorMonitor struct {
	watchers    []inbound.OrchestratorWatcher
	broadcaster outbound.PushBroadcaster
	log         outbound.Logger

	// merged is created at construction (not in Run) so watchers can be
	// registered via AddWatcher before or after Run starts — orchestrator
	// monitoring is consent-gated and starts/stops at grant/revoke time
	// (#570). Never closed; Run exits on ctx cancellation.
	merged chan orchestrator.State

	mu     sync.RWMutex
	states map[string]*orchestrator.State // name → latest state
}

// NewOrchestratorMonitor creates a monitor for the given orchestrator watchers.
func NewOrchestratorMonitor(
	watchers []inbound.OrchestratorWatcher,
	broadcaster outbound.PushBroadcaster,
	log outbound.Logger,
) *OrchestratorMonitor {
	return &OrchestratorMonitor{
		watchers:    watchers,
		broadcaster: broadcaster,
		log:         log,
		merged:      make(chan orchestrator.State, 4),
		states:      make(map[string]*orchestrator.State),
	}
}

// AddWatcher registers an orchestrator watcher with the running (or
// not-yet-running) monitor: a drain goroutine forwards its state snapshots
// into the merged channel until ctx is cancelled. The caller owns the
// watcher's Watch lifecycle under the same ctx — this is how the
// permission service starts/stops orchestrator monitoring on grant/revoke
// (#570).
func (m *OrchestratorMonitor) AddWatcher(ctx context.Context, w inbound.OrchestratorWatcher) {
	go m.drainWatcher(ctx, w)
}

// drainWatcher subscribes to one watcher and forwards its snapshots into
// the merged channel until ctx is cancelled or the subscription closes.
func (m *OrchestratorMonitor) drainWatcher(ctx context.Context, w inbound.OrchestratorWatcher) {
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case state, ok := <-ch:
			if !ok {
				return
			}
			select {
			case m.merged <- state:
			case <-ctx.Done():
				return
			}
		}
	}
}

// State returns the latest state for the given orchestrator name, or nil.
func (m *OrchestratorMonitor) State(name string) *orchestrator.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.states[name]; ok {
		cp := *s
		return &cp
	}
	return nil
}

// Run drains all OrchestratorWatcher event streams into the merged channel
// and records state updates. It blocks until ctx is cancelled. Watchers
// registered later via AddWatcher feed the same channel.
func (m *OrchestratorMonitor) Run(ctx context.Context) error {
	for _, w := range m.watchers {
		go m.drainWatcher(ctx, w)
	}

	m.log.LogInfo("orchestrator-monitor", "", fmt.Sprintf("started — watching %d orchestrator(s)", len(m.watchers)))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case state := <-m.merged:
			m.mu.Lock()
			cp := state
			m.states[state.Adapter] = &cp
			m.mu.Unlock()
		}
	}
}
