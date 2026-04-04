// Package process implements PID monitoring via kqueue EVFILT_PROC NOTE_EXIT
// and one-time lsof-based PID discovery for sessions missing process info.
package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ExitHandler is called when a watched process exits.
type ExitHandler func(pid int, sessionID string)

// Watcher monitors process PIDs via kqueue EVFILT_PROC NOTE_EXIT.
// It implements outbound.ProcessWatcher.
type Watcher struct {
	kqfd    int
	mu      sync.Mutex
	watched map[int]string // pid → sessionID
	handler ExitHandler
}

// New creates a Watcher backed by a kqueue file descriptor.
// The handler is invoked (in a goroutine) whenever a watched process exits.
func New(handler ExitHandler) (*Watcher, error) {
	kqfd, err := syscall.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("kqueue: %w", err)
	}
	return &Watcher{
		kqfd:    kqfd,
		watched: make(map[int]string),
		handler: handler,
	}, nil
}

// Watch registers a PID for exit monitoring associated with a sessionID.
// If the process has already exited, the handler fires asynchronously.
func (w *Watcher) Watch(pid int, sessionID string) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Register with kqueue.
	ev := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}
	_, err := syscall.Kevent(w.kqfd, []syscall.Kevent_t{ev}, nil, nil)
	if err != nil {
		// ESRCH means the process is already dead — fire the handler.
		if err == syscall.ESRCH {
			go w.handler(pid, sessionID)
			return nil
		}
		return fmt.Errorf("kevent register pid %d: %w", pid, err)
	}

	w.watched[pid] = sessionID
	return nil
}

// Unwatch stops monitoring the given PID.
func (w *Watcher) Unwatch(pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, ok := w.watched[pid]; !ok {
		return
	}

	ev := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_DELETE,
	}
	// Best-effort removal; may fail if process already exited (ESRCH).
	syscall.Kevent(w.kqfd, []syscall.Kevent_t{ev}, nil, nil)
	delete(w.watched, pid)
}

// Run starts the kqueue event loop. It blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	events := make([]syscall.Kevent_t, 8)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Poll with timeout so we can honour context cancellation.
		timeout := syscall.NsecToTimespec(int64(500 * time.Millisecond))
		n, err := syscall.Kevent(w.kqfd, nil, events[:], &timeout)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return fmt.Errorf("kevent wait: %w", err)
		}

		for i := range n {
			pid := int(events[i].Ident)

			w.mu.Lock()
			sessionID, ok := w.watched[pid]
			if ok {
				delete(w.watched, pid)
			}
			w.mu.Unlock()

			if ok && w.handler != nil {
				go w.handler(pid, sessionID)
			}
		}
	}
}

// Close releases the kqueue file descriptor.
func (w *Watcher) Close() error {
	return syscall.Close(w.kqfd)
}

// findClaudeProcesses returns PIDs of running "claude" processes via pgrep.
func findClaudeProcesses() ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pgrep", "-x", "claude").Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// processCWD returns the working directory of pid using lsof.
func processCWD(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof cwd pid %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}
	return "", fmt.Errorf("cwd not found for pid %d", pid)
}

// DiscoverPIDByCWD finds a "claude" process whose CWD matches the given
// directory. When multiple processes match, disambiguate selects one.
// Returns 0, nil when no matching process is found.
func DiscoverPIDByCWD(cwd string, disambiguate func([]int) int) (int, error) {
	if cwd == "" {
		return 0, nil
	}
	pids, err := findClaudeProcesses()
	if err != nil {
		return 0, fmt.Errorf("find claude processes: %w", err)
	}

	myPID := os.Getpid()
	var matches []int
	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		dir, err := processCWD(pid)
		if err != nil {
			continue
		}
		if dir == cwd {
			matches = append(matches, pid)
		}
	}

	switch len(matches) {
	case 0:
		return 0, nil
	case 1:
		return matches[0], nil
	default:
		if disambiguate != nil {
			return disambiguate(matches), nil
		}
		// Default: highest PID (most recently started on macOS).
		best := 0
		for _, p := range matches {
			if p > best {
				best = p
			}
		}
		return best, nil
	}
}

// DiscoverPID uses lsof to find the PID that has filePath open.
// Returns 0, nil when no process has the file open.
func DiscoverPID(filePath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "lsof", "-t", filePath).Output()
	if err != nil {
		// Exit status 1 means no matches — not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return 0, nil
		}
		return 0, fmt.Errorf("lsof %s: %w", filePath, err)
	}

	// Skip our own PID — the daemon reads transcript files for metrics,
	// so lsof often returns the daemon itself. We want the external
	// process (e.g. Claude Code CLI) that owns the session.
	myPID := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		if pid != myPID {
			return pid, nil
		}
	}
	return 0, nil
}
