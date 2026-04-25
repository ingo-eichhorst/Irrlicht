//go:build !darwin

package processlifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// monitorPollInterval is how often the !darwin pidMonitor checks each
// watched PID for liveness. 1s matches the scanner's default poll cadence
// so exit detection latency is comparable across the two paths.
const monitorPollInterval = 1 * time.Second

// exitHandler is called when a watched process exits.
type exitHandler func(pid int, sessionID string)

// pidMonitor is a polling-based ProcessWatcher used on platforms without
// kqueue (linux, windows). It implements outbound.ProcessWatcher with the
// same surface as the darwin kqueue implementation, so callers don't need
// to know which one they got from NewMonitor.
type pidMonitor struct {
	mu       sync.Mutex
	watched  map[int]string // pid → sessionID
	handler  exitHandler
	interval time.Duration
}

// NewMonitor creates a polling pidMonitor. The handler is invoked (in a
// goroutine) whenever a watched process is no longer alive.
func NewMonitor(handler exitHandler) (*pidMonitor, error) {
	return &pidMonitor{
		watched:  make(map[int]string),
		handler:  handler,
		interval: monitorPollInterval,
	}, nil
}

// Watch registers a PID for exit monitoring associated with a sessionID.
// If the process has already exited, the handler fires asynchronously.
func (m *pidMonitor) Watch(pid int, sessionID string) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if !PidAlive(pid) {
		go m.handler(pid, sessionID)
		return nil
	}
	m.watched[pid] = sessionID
	return nil
}

// Unwatch stops monitoring the given PID.
func (m *pidMonitor) Unwatch(pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.watched, pid)
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (m *pidMonitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.sweep()
		}
	}
}

// Close releases resources. The polling monitor has no OS handle to free,
// but the method is required by outbound.ProcessWatcher.
func (m *pidMonitor) Close() error { return nil }

// sweep runs one liveness check across all watched PIDs and fires the
// handler for any that have died.
func (m *pidMonitor) sweep() {
	m.mu.Lock()
	exited := make(map[int]string)
	for pid, sid := range m.watched {
		if !PidAlive(pid) {
			exited[pid] = sid
			delete(m.watched, pid)
		}
	}
	m.mu.Unlock()

	for pid, sid := range exited {
		go m.handler(pid, sid)
	}
}
