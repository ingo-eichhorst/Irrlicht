//go:build linux

package processlifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// pollTimeout bounds each poll(2) wait so the Run loop can honour context
// cancellation and pick up newly-watched PIDs. It also caps the latency
// between a Watch call and the PID joining the poll set — exits are still
// detected on the next tick regardless.
const pollTimeout = 500 * time.Millisecond

// watchedPID pairs an open pidfd with the session it belongs to.
type watchedPID struct {
	fd        int
	sessionID string
}

// pidMonitor monitors process exit via pidfd_open(2) + poll(2). A pidfd
// becomes readable when its process exits, so we poll the whole set with a
// timeout. It implements outbound.ProcessWatcher. pidfd_open requires Linux
// 5.3+ (2019) and needs no elevated privileges.
type pidMonitor struct {
	mu      sync.Mutex
	watched map[int]watchedPID // pid → {pidfd, sessionID}
	handler exitHandler
}

// NewMonitor creates a pidMonitor. The handler is invoked (in a goroutine)
// whenever a watched process exits.
func NewMonitor(handler exitHandler) (*pidMonitor, error) {
	return &pidMonitor{
		watched: make(map[int]watchedPID),
		handler: handler,
	}, nil
}

// Watch registers a PID for exit monitoring associated with a sessionID.
// If the process has already exited, the handler fires asynchronously.
func (m *pidMonitor) Watch(pid int, sessionID string) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}

	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		// ESRCH means the process is already dead — fire the handler.
		if err == unix.ESRCH {
			go m.handler(pid, sessionID)
			return nil
		}
		return fmt.Errorf("pidfd_open pid %d: %w", pid, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Replace any stale watch for the same PID (PID reuse / re-discovery).
	if prev, ok := m.watched[pid]; ok {
		unix.Close(prev.fd)
	}
	m.watched[pid] = watchedPID{fd: fd, sessionID: sessionID}
	return nil
}

// Unwatch stops monitoring the given PID.
func (m *pidMonitor) Unwatch(pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if w, ok := m.watched[pid]; ok {
		unix.Close(w.fd)
		delete(m.watched, pid)
	}
}

// Run starts the poll(2) event loop. It blocks until ctx is cancelled.
func (m *pidMonitor) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Snapshot the current fd set under lock, then poll without it.
		m.mu.Lock()
		fds := make([]unix.PollFd, 0, len(m.watched))
		pids := make([]int, 0, len(m.watched))
		for pid, w := range m.watched {
			fds = append(fds, unix.PollFd{Fd: int32(w.fd), Events: unix.POLLIN})
			pids = append(pids, pid)
		}
		m.mu.Unlock()

		if len(fds) == 0 {
			// Nothing to watch yet; idle while still honouring ctx.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollTimeout):
			}
			continue
		}

		n, err := unix.Poll(fds, int(pollTimeout.Milliseconds()))
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return fmt.Errorf("poll: %w", err)
		}
		if n == 0 {
			continue
		}

		for i := range fds {
			if fds[i].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) == 0 {
				continue
			}
			pid := pids[i]

			var (
				sessionID string
				fire      bool
			)
			m.mu.Lock()
			// Re-check membership and fd identity in case a concurrent
			// Unwatch/Watch swapped this PID since the snapshot.
			if w, ok := m.watched[pid]; ok && int32(w.fd) == fds[i].Fd {
				unix.Close(w.fd)
				delete(m.watched, pid)
				sessionID, fire = w.sessionID, true
			}
			m.mu.Unlock()

			if fire && m.handler != nil {
				go m.handler(pid, sessionID)
			}
		}
	}
}

// Close releases every open pidfd.
func (m *pidMonitor) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.watched {
		unix.Close(w.fd)
	}
	m.watched = make(map[int]watchedPID)
	return nil
}
