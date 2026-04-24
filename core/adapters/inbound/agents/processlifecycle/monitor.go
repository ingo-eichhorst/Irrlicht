package processlifecycle

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"
)

// exitHandler is called when a watched process exits.
type exitHandler func(pid int, sessionID string)

// pidMonitor monitors process PIDs via kqueue EVFILT_PROC NOTE_EXIT.
// It implements outbound.ProcessWatcher.
type pidMonitor struct {
	kqfd    int
	mu      sync.Mutex
	watched map[int]string // pid → sessionID
	handler exitHandler
}

// NewMonitor creates a pidMonitor backed by a kqueue file descriptor.
// The handler is invoked (in a goroutine) whenever a watched process exits.
func NewMonitor(handler exitHandler) (*pidMonitor, error) {
	kqfd, err := syscall.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("kqueue: %w", err)
	}
	return &pidMonitor{
		kqfd:    kqfd,
		watched: make(map[int]string),
		handler: handler,
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

	// Register with kqueue.
	ev := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}
	_, err := syscall.Kevent(m.kqfd, []syscall.Kevent_t{ev}, nil, nil)
	if err != nil {
		// ESRCH means the process is already dead — fire the handler.
		if err == syscall.ESRCH {
			go m.handler(pid, sessionID)
			return nil
		}
		return fmt.Errorf("kevent register pid %d: %w", pid, err)
	}

	m.watched[pid] = sessionID
	return nil
}

// Unwatch stops monitoring the given PID.
func (m *pidMonitor) Unwatch(pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.watched[pid]; !ok {
		return
	}

	ev := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_DELETE,
	}
	// Best-effort removal; may fail if process already exited (ESRCH).
	syscall.Kevent(m.kqfd, []syscall.Kevent_t{ev}, nil, nil)
	delete(m.watched, pid)
}

// Run starts the kqueue event loop. It blocks until ctx is cancelled.
func (m *pidMonitor) Run(ctx context.Context) error {
	events := make([]syscall.Kevent_t, 8)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Poll with timeout so we can honour context cancellation.
		timeout := syscall.NsecToTimespec(int64(500 * time.Millisecond))
		n, err := syscall.Kevent(m.kqfd, nil, events[:], &timeout)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return fmt.Errorf("kevent wait: %w", err)
		}

		for i := range n {
			pid := int(events[i].Ident)

			m.mu.Lock()
			sessionID, ok := m.watched[pid]
			if ok {
				delete(m.watched, pid)
			}
			m.mu.Unlock()

			if ok && m.handler != nil {
				go m.handler(pid, sessionID)
			}
		}
	}
}

// Close releases the kqueue file descriptor.
func (m *pidMonitor) Close() error {
	return syscall.Close(m.kqfd)
}
