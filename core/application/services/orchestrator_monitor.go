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
		states:      make(map[string]*orchestrator.State),
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

// Run subscribes to all OrchestratorWatcher event streams, fans them into a
// single channel, and broadcasts updates. It blocks until ctx is cancelled.
func (m *OrchestratorMonitor) Run(ctx context.Context) error {
	if len(m.watchers) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}

	merged := make(chan orchestrator.State, 4)
	var wg sync.WaitGroup

	for _, w := range m.watchers {
		ch := w.Subscribe()
		wg.Add(1)
		go func(watcher inbound.OrchestratorWatcher, ch <-chan orchestrator.State) {
			defer wg.Done()
			defer watcher.Unsubscribe(ch)
			for {
				select {
				case <-ctx.Done():
					return
				case state, ok := <-ch:
					if !ok {
						return
					}
					select {
					case merged <- state:
					case <-ctx.Done():
						return
					}
				}
			}
		}(w, ch)
	}

	go func() {
		wg.Wait()
		close(merged)
	}()

	m.log.LogInfo("orchestrator-monitor", "", fmt.Sprintf("started — watching %d orchestrator(s)", len(m.watchers)))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case state, ok := <-merged:
			if !ok {
				return nil
			}
			m.mu.Lock()
			cp := state
			m.states[state.Adapter] = &cp
			m.mu.Unlock()
		}
	}
}
