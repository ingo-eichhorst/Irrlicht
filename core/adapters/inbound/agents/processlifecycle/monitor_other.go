//go:build !darwin && !linux

package processlifecycle

import "context"

// pidMonitor is a no-op exit watcher for platforms without a wired-up
// process-exit primitive (e.g. Windows, until process_windows.go lands).
// Sessions are still observed; their PIDs are simply never reaped via an
// exit signal — the periodic scanner removes them when they vanish. It
// implements outbound.ProcessWatcher.
type pidMonitor struct {
	handler exitHandler
}

// NewMonitor creates a no-op pidMonitor.
func NewMonitor(handler exitHandler) (*pidMonitor, error) {
	return &pidMonitor{handler: handler}, nil
}

func (m *pidMonitor) Watch(pid int, sessionID string) error { return nil }

func (m *pidMonitor) Unwatch(pid int) {}

// Run blocks until ctx is cancelled, matching the other implementations.
func (m *pidMonitor) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (m *pidMonitor) Close() error { return nil }
