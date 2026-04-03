// Package processscanner detects running agent processes before they create a
// transcript file, so the session appears in the UI as soon as the agent
// starts — not only after the first message is sent.
//
// It polls for matching processes on a fixed interval, checks whether a real
// transcript (.jsonl) already exists for the process's working directory, and
// emits EventNewSession / EventRemoved agent events that flow into the normal
// SessionDetector pipeline.
//
// Session IDs for these synthetic pre-sessions use the form "proc-<pid>" to
// distinguish them from UUID-based transcript sessions.
package processscanner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/agent"
)

// DefaultInterval is the polling interval used when none is specified.
const DefaultInterval = 1 * time.Second

// trackedProc holds the pre-session metadata for a running process.
type trackedProc struct {
	sessionID  string
	projectDir string
	superseded bool // real transcript exists; keep tracking to prevent re-creation
}

// Scanner polls for agent processes and emits synthetic EventNewSession /
// EventRemoved events so the session can be shown before the first message.
// It implements inbound.AgentWatcher.
type Scanner struct {
	processName  string        // exact process name matched by pgrep -x
	adapter      string        // adapter label placed on emitted events
	projectsRoot string        // absolute path to ~/.claude/projects (or equivalent)
	interval     time.Duration

	// sessionChecker is an optional function that reports whether any
	// non-proc session already exists for the given encoded projectDir.
	// When set, it is consulted before the file-modification-time fallback
	// in hasActiveSession so that idle (but alive) sessions suppress ghost
	// proc creation.
	sessionChecker func(projectDir string) bool

	mu      sync.Mutex
	tracked map[int]trackedProc // pid → pre-session
	subs    []chan agent.Event
}

// New creates a Scanner for the given agent process.
//   - processName: exact binary name, e.g. "claude"
//   - adapter:     adapter label, e.g. "claude-code"
//   - projectsRoot: absolute path to the projects directory, e.g. ~/.claude/projects
//   - interval:    how often to poll; pass 0 to use DefaultInterval
func New(processName, adapter, projectsRoot string, interval time.Duration) *Scanner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Scanner{
		processName:  processName,
		adapter:      adapter,
		projectsRoot: projectsRoot,
		interval:     interval,
		tracked:      make(map[int]trackedProc),
	}
}

// WithSessionChecker sets a function that reports whether any non-proc session
// exists for the given encoded projectDir (e.g. "-Users-ingo-projects-foo").
// When set, hasActiveSession consults it before the file-modification fallback,
// preventing ghost proc sessions for directories with idle real sessions.
func (s *Scanner) WithSessionChecker(fn func(projectDir string) bool) *Scanner {
	s.sessionChecker = fn
	return s
}

// Watch begins polling. It runs an immediate scan then continues on the
// configured interval until ctx is cancelled.
func (s *Scanner) Watch(ctx context.Context) error {
	s.poll()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.poll()
		}
	}
}

// Subscribe returns a channel that receives agent events from this scanner.
func (s *Scanner) Subscribe() <-chan agent.Event {
	ch := make(chan agent.Event, 4)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()
	return ch
}

// Unsubscribe removes a previously subscribed channel and closes it.
func (s *Scanner) Unsubscribe(ch <-chan agent.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.subs {
		if c == ch {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			close(c)
			return
		}
	}
}

// poll runs one detection cycle: finds live processes, emits new-session events
// for newcomers without transcripts, and removes pre-sessions that have exited
// or whose real transcript has appeared.
func (s *Scanner) poll() {
	pids, err := findProcesses(s.processName)
	if err != nil {
		return
	}

	live := make(map[int]bool, len(pids))
	for _, pid := range pids {
		live[pid] = true
	}

	// --- handle newly-seen PIDs ---
	for _, pid := range pids {
		s.mu.Lock()
		_, alreadyTracked := s.tracked[pid]
		s.mu.Unlock()

		cwd, err := processCWD(pid)
		if err != nil || cwd == "" {
			continue
		}
		projectDir := cwdToProjectDir(cwd)

		if alreadyTracked {
			s.mu.Lock()
			proc := s.tracked[pid]
			s.mu.Unlock()

			// Already superseded — real transcript took over; nothing to do.
			if proc.superseded {
				continue
			}

			// Check whether a real transcript has appeared since we last looked.
			if s.hasActiveSession(projectDir) {
				// Mark as superseded and emit removal. Keep in tracked map
				// so we don't recreate the pre-session on the next poll.
				s.mu.Lock()
				proc.superseded = true
				s.tracked[pid] = proc
				s.mu.Unlock()
				s.broadcast(agent.Event{
					Type:       agent.EventRemoved,
					Adapter:    s.adapter,
					SessionID:  proc.sessionID,
					ProjectDir: projectDir,
				})
			}
			continue
		}

		// Skip if an active transcript was recently modified in this project
		// dir (the file watcher handles those). Old transcripts from previous
		// sessions are ignored so new processes are still detected.
		if s.hasActiveSession(projectDir) {
			continue
		}

		sessionID := fmt.Sprintf("proc-%d", pid)
		ev := agent.Event{
			Type:       agent.EventNewSession,
			Adapter:    s.adapter,
			SessionID:  sessionID,
			ProjectDir: projectDir,
			CWD:        cwd,
		}

		// Track the PID and emit atomically — but only if subscribers exist.
		// If no subscribers yet (startup race with SessionDetector.Run), skip
		// tracking so the next poll retries rather than silently losing the event.
		s.mu.Lock()
		if len(s.subs) > 0 {
			s.tracked[pid] = trackedProc{sessionID: sessionID, projectDir: projectDir}
			for _, ch := range s.subs {
				select {
				case ch <- ev:
				default:
				}
			}
		}
		s.mu.Unlock()
	}

	// --- handle exited PIDs ---
	s.mu.Lock()
	var exited []trackedProc
	for pid, proc := range s.tracked {
		if !live[pid] {
			exited = append(exited, proc)
			delete(s.tracked, pid)
		}
	}
	s.mu.Unlock()

	for _, proc := range exited {
		s.broadcast(agent.Event{
			Type:       agent.EventRemoved,
			Adapter:    s.adapter,
			SessionID:  proc.sessionID,
			ProjectDir: proc.projectDir,
		})
	}
}

// hasActiveSession returns true if the project directory has an active session.
// It first consults the injected sessionChecker (if set) to catch idle sessions
// whose transcripts haven't been written recently, then falls back to checking
// for a recently-modified .jsonl file (within 60 seconds).
func (s *Scanner) hasActiveSession(projectDir string) bool {
	if projectDir == "" {
		return false
	}
	if s.sessionChecker != nil && s.sessionChecker(projectDir) {
		return true
	}
	if s.projectsRoot == "" {
		return false
	}
	dir := filepath.Join(s.projectsRoot, projectDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	cutoff := time.Now().Add(-60 * time.Second)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

// broadcast sends an event to all subscribers non-blocking (drops if full).
func (s *Scanner) broadcast(ev agent.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// --- helpers ----------------------------------------------------------------

// findProcesses returns PIDs of processes whose name exactly matches name.
func findProcesses(name string) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pgrep", "-x", name).Output()
	if err != nil {
		// pgrep exits 1 when there are no matches — not an error.
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil
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

// cwdToProjectDir converts a working directory path to the directory name used
// by Claude Code under ~/.claude/projects/. Claude Code replaces both "/" and
// "." with "-", so "/Users/ingo/projects/foo" becomes "-Users-ingo-projects-foo"
// and "/path/.hidden/sub" becomes "-path--hidden-sub".
func cwdToProjectDir(cwd string) string {
	s := strings.ReplaceAll(cwd, "/", "-")
	return strings.ReplaceAll(s, ".", "-")
}
