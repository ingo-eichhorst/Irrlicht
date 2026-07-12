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
		fds, pids := m.snapshotWatched()

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

		m.handleReadyFDs(fds, pids)
	}
}

// snapshotWatched copies the current watched pidfd set under lock into the
// slices poll(2) expects, so the (potentially blocking) poll call itself runs
// without holding the lock.
func (m *pidMonitor) snapshotWatched() ([]unix.PollFd, []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fds := make([]unix.PollFd, 0, len(m.watched))
	pids := make([]int, 0, len(m.watched))
	for pid, w := range m.watched {
		fds = append(fds, unix.PollFd{Fd: int32(w.fd), Events: unix.POLLIN})
		pids = append(pids, pid)
	}
	return fds, pids
}

// handleReadyFDs processes one poll(2) result: for every fd with a
// ready/hangup/error/invalid event, retires the matching watch (if it hasn't
// already been swapped out by a concurrent Watch/Unwatch) and fires its exit
// handler.
func (m *pidMonitor) handleReadyFDs(fds []unix.PollFd, pids []int) {
	for i := range fds {
		// POLLNVAL covers an fd a concurrent Unwatch closed between the
		// snapshot and this poll; it counts toward poll's return, so
		// include it here and let the membership/fd re-check below drop
		// it rather than letting the loop spin re-polling a stale set.
		if fds[i].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) == 0 {
			continue
		}
		pid := pids[i]

		sessionID, fire := m.retireWatch(pid, fds[i].Fd)
		if fire && m.handler != nil {
			go m.handler(pid, sessionID)
		}
	}
}

// retireWatch removes pid's watch if it still matches fd — guarding against a
// concurrent Unwatch/Watch having swapped this PID since the snapshot — closes
// the fd, and reports whether the exit handler should fire.
func (m *pidMonitor) retireWatch(pid int, fd int32) (sessionID string, fire bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.watched[pid]
	if !ok || int32(w.fd) != fd {
		return "", false
	}
	unix.Close(w.fd)
	delete(m.watched, pid)
	return w.sessionID, true
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
