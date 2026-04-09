package processlifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"irrlicht/core/domain/agent"
)

// DefaultInterval is the polling interval used when none is specified.
const DefaultInterval = 1 * time.Second

// BackoffInterval is the slower polling interval used when the PID set is stable.
const BackoffInterval = 5 * time.Second

// stableThreshold is the number of consecutive stable polls before backing off.
const stableThreshold = 5

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

	// sessionChecker is an optional function that reports whether a
	// non-proc session already exists for the given encoded projectDir and
	// belongs to the given live PID. When set, it is consulted before the
	// file-modification-time fallback in hasActiveSession so that idle (but
	// alive) sessions suppress ghost proc creation for the same process.
	// Discriminating by PID ensures historical sessions from prior daemon
	// runs do not block pre-session creation for new processes in the same
	// project (GH #113).
	sessionChecker func(projectDir string, pid int) bool

	mu      sync.Mutex
	tracked map[int]trackedProc // pid → pre-session
	subs    []chan agent.Event

	// Adaptive backoff: back off to BackoffInterval when PID set is stable.
	lastPIDCount int
	stablePolls  int
}

// NewScanner creates a Scanner for the given agent process.
//   - processName: exact binary name, e.g. "claude"
//   - adapter:     adapter label, e.g. "claude-code"
//   - projectsRoot: absolute path to the projects directory, e.g. ~/.claude/projects
//   - interval:    how often to poll; pass 0 to use DefaultInterval
func NewScanner(processName, adapter, projectsRoot string, interval time.Duration) *Scanner {
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

// WithSessionChecker sets a function that reports whether a non-proc session
// exists for the given encoded projectDir (e.g. "-Users-ingo-projects-foo")
// AND belongs to the given PID. When set, hasActiveSession consults it before
// the file-modification fallback, preventing ghost proc sessions for the same
// live process without also suppressing pre-sessions for new processes in
// projects that merely have historical sessions on disk.
func (s *Scanner) WithSessionChecker(fn func(projectDir string, pid int) bool) *Scanner {
	s.sessionChecker = fn
	return s
}

// Watch begins polling. It runs an immediate scan then continues on the
// configured interval until ctx is cancelled. The interval backs off from
// DefaultInterval to BackoffInterval when the PID set is stable.
func (s *Scanner) Watch(ctx context.Context) error {
	s.poll()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	currentInterval := s.interval

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.poll()

			// Adaptive backoff: count how many polls the PID set stayed the same.
			s.mu.Lock()
			pidCount := len(s.tracked)
			s.mu.Unlock()

			if pidCount == s.lastPIDCount {
				s.stablePolls++
			} else {
				s.stablePolls = 0
				s.lastPIDCount = pidCount
			}

			var targetInterval time.Duration
			if s.stablePolls >= stableThreshold {
				targetInterval = BackoffInterval
			} else {
				targetInterval = s.interval
			}
			if targetInterval != currentInterval {
				ticker.Reset(targetInterval)
				currentInterval = targetInterval
			}
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
	pids, err := FindProcesses(s.processName)
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

		cwd, err := ProcessCWD(pid)
		if err != nil || cwd == "" {
			continue
		}
		projectDir := CWDToProjectDir(cwd)

		if alreadyTracked {
			s.mu.Lock()
			proc := s.tracked[pid]
			s.mu.Unlock()

			// Already superseded — real transcript took over; nothing to do.
			if proc.superseded {
				continue
			}

			// Check whether a real transcript has appeared since we last looked.
			if s.hasActiveSession(projectDir, pid) {
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
		if s.hasActiveSession(projectDir, pid) {
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

// hasActiveSession returns true if the project directory has an active session
// belonging to the given PID, as reported by the injected sessionChecker. The
// checker discriminates by PID so historical sessions from prior daemon runs
// don't block pre-session creation (GH #113) and so unrelated activity from
// *other* live sessions in the same project doesn't prematurely supersede this
// pre-session.
//
// A previous version of this function fell back to a project-wide .jsonl mtime
// check when sessionChecker returned false. That fallback was fundamentally
// wrong for the supersession path: it picked up writes from any session in the
// project, not just the transcript owned by this pid, and silently deleted
// freshly-created pre-sessions whenever a neighbour session was actively being
// written to. The sessionChecker is now trusted as the sole signal.
func (s *Scanner) hasActiveSession(projectDir string, pid int) bool {
	if projectDir == "" {
		return false
	}
	if s.sessionChecker == nil {
		return false
	}
	return s.sessionChecker(projectDir, pid)
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
