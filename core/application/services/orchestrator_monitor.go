// OrchestratorMonitor subscribes to OrchestratorWatchers and broadcasts
// state updates via PushBroadcaster. It mirrors SessionDetector's pattern
// for the orchestrator category of inbound adapters.
package services

import (
	"context"
	"fmt"
	"sort"
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
	merged chan registeredState

	mu     sync.RWMutex
	states map[string]registeredState // adapter name → latest state
}

// registeredState is one snapshot together with the context of the
// registration that delivered it (#2148). A registration ends when that
// context is cancelled — the permission service's revoke does it through the
// stop effect — and from that moment the snapshot is not served, and a later
// one from the same registration is not stored.
type registeredState struct {
	state orchestrator.State
	reg   context.Context
}

// live reports whether r's registration has not ended.
func (r registeredState) live() bool { return r.reg.Err() == nil }

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
		merged:      make(chan registeredState, 4),
		states:      make(map[string]registeredState),
	}
}

// AddWatcher registers an orchestrator watcher with the running (or
// not-yet-running) monitor: a drain goroutine forwards its state snapshots
// into the merged channel until ctx is cancelled. The caller owns the
// watcher's Watch lifecycle under the same ctx — this is how the
// permission service starts/stops orchestrator monitoring on grant/revoke
// (#570).
//
// ctx is also the registration: once it is cancelled, State and States stop
// serving this watcher's snapshot at once, with no wait on the drain
// goroutine, and a snapshot still in flight is discarded when Run reaches it
// (#2148; TestOrchestratorMonitor_RegistrationEndDropsStateAtOnce and
// TestOrchestratorMonitor_SnapshotInFlightAtRevokeStaysDropped).
func (m *OrchestratorMonitor) AddWatcher(ctx context.Context, w inbound.OrchestratorWatcher) {
	go m.drainWatcher(ctx, w)
}

// drainWatcher subscribes to one watcher and forwards its snapshots into
// the merged channel until ctx is cancelled or the subscription closes. Each
// snapshot carries ctx as its registration.
func (m *OrchestratorMonitor) drainWatcher(ctx context.Context, w inbound.OrchestratorWatcher) {
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)
	defer m.forget(ctx, w.Name())
	for {
		select {
		case <-ctx.Done():
			return
		case state, ok := <-ch:
			if !ok {
				return
			}
			select {
			case m.merged <- registeredState{state: state, reg: ctx}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// forget deletes name's stored snapshot when it came from the registration
// reg, keeping the map to live registrations. It only tidies: State and
// States already skip a snapshot whose registration has ended.
func (m *OrchestratorMonitor) forget(reg context.Context, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rs, ok := m.states[name]; ok && rs.reg == reg {
		delete(m.states, name)
	}
}

// store records rs as its adapter's latest snapshot unless rs's registration
// has ended — a snapshot that was in flight when the registration ended.
func (m *OrchestratorMonitor) store(rs registeredState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rs.live() {
		m.states[rs.state.Adapter] = rs
	}
}

// State returns the latest state for the given orchestrator name, or nil
// when there is none or its registration has ended.
func (m *OrchestratorMonitor) State(name string) *orchestrator.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if rs, ok := m.states[name]; ok && rs.live() {
		cp := rs.state
		return &cp
	}
	return nil
}

// States returns a copy of every orchestrator's latest state whose
// registration has not ended, sorted by adapter name so the dashboard's
// group order does not depend on registration order (#2148).
func (m *OrchestratorMonitor) States() []*orchestrator.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*orchestrator.State, 0, len(m.states))
	for _, rs := range m.states {
		if rs.live() {
			cp := rs.state
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Adapter < out[j].Adapter })
	return out
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
		case rs := <-m.merged:
			m.store(rs)
		}
	}
}
