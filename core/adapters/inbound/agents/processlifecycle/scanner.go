package processlifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// defaultInterval is the polling interval used when none is specified.
const defaultInterval = 1 * time.Second

// backoffInterval is the slower polling interval used when the PID set is stable.
const backoffInterval = 5 * time.Second

// stableThreshold is the number of consecutive stable polls before backing off.
const stableThreshold = 5

// trackedProc holds the pre-session metadata for a running process.
type trackedProc struct {
	sessionID         string
	projectDir        string
	superseded        bool   // real transcript exists; keep tracking to prevent re-creation
	cwd               string // captured at first sight; needed for TranscriptFilename probe
	transcriptEmitted bool   // dedupe per-PID transcript_new emissions for CWD-resident transcripts
	transcriptSize    int64  // last-seen size of the CWD-resident transcript; drives EventActivity emission
}

// Scanner polls for agent processes and emits synthetic EventNewSession /
// EventRemoved events so the session can be shown before the first message.
// It implements inbound.Watcher.
type Scanner struct {
	processName        string         // exact process name matched by pgrep -x
	commandLineMatch   string         // if non-empty, used with pgrep -f instead of -x ProcessName
	transcriptFilename string         // if non-empty, scanner checks <CWD>/<filename> for the real transcript
	adapter            string         // adapter label placed on emitted events
	identity           agent.Identity // populated via WithIdentity
	interval           time.Duration

	// sessionChecker is an optional function that reports whether a real
	// (non-proc) session for the given projectDir and PID already exists.
	// Callers typically delegate to HasRealSessionForPID.
	sessionChecker func(projectDir string, pid int) bool

	// argvFilter is an optional per-adapter predicate that excludes a matched
	// PID by inspecting its argv. Set via WithArgvFilter from the adapter's
	// Process.ExcludeArgv declaration. When it returns true the scanner mints
	// no pre-session for that PID — the binary matched but it's infrastructure
	// (e.g. a background daemon/wrapper running the same binary), not a
	// session. Keeping the predicate here (not in poll's matcher) keeps the
	// scanner generic; the format-specific argv shapes live in the adapter.
	argvFilter func(argv []string) bool

	mu      sync.Mutex
	tracked map[int]trackedProc // pid → pre-session

	// argvVerdicts caches the per-PID argvFilter verdict — argv is immutable
	// for a process's lifetime, so each PID pays at most one ArgvOf read.
	// poll prunes entries whose PID no longer matches, so a recycled PID
	// cannot inherit a stale verdict. See argvExcluded.
	argvVerdicts map[int]bool
	subs         []chan agent.Event

	// Adaptive backoff: back off to backoffInterval when PID set is stable.
	lastPIDCount int
	stablePolls  int
}

// NewScanner creates a Scanner for the given agent process.
//   - processName: exact binary name, e.g. "claude"
//   - adapter:     adapter label, e.g. "claude-code"
//   - interval:    how often to poll; pass 0 to use defaultInterval
//
// For agents whose process name on disk differs from their CLI name (e.g.
// Python tools launched via a wrapper), use WithCommandLineMatch to switch
// to a pgrep -f pattern.
func NewScanner(processName, adapter string, interval time.Duration) *Scanner {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Scanner{
		processName:  processName,
		adapter:      adapter,
		interval:     interval,
		tracked:      make(map[int]trackedProc),
		argvVerdicts: make(map[int]bool),
	}
}

// WithCommandLineMatch switches the scanner from `pgrep -x ProcessName` to
// `pgrep -f <pattern>`. Use for agents whose actual OS process is a wrapper
// (e.g. python launching aider). Returns the scanner for chaining.
func (s *Scanner) WithCommandLineMatch(pattern string) *Scanner {
	s.commandLineMatch = pattern
	return s
}

// WithIdentity sets the full agent.Identity for this scanner so it
// satisfies inbound.Watcher. Returns the scanner for chaining.
func (s *Scanner) WithIdentity(id agent.Identity) *Scanner {
	s.identity = id
	return s
}

// Identity returns the agent.Identity supplied via WithIdentity, or the
// zero value if WithIdentity was never called.
func (s *Scanner) Identity() agent.Identity {
	return s.identity
}

// WithTranscriptFilename tells the scanner to additionally probe each
// detected process's CWD for a fixed filename (e.g.
// ".aider.chat.history.md") and emit an EventNewSession with the real
// transcript path when the file appears. Use for agents that write
// transcripts per-project rather than under a fixed RootDir.
func (s *Scanner) WithTranscriptFilename(name string) *Scanner {
	s.transcriptFilename = name
	return s
}

// WithSessionChecker sets a function that reports whether a real
// (non-proc) session exists for the given projectDir and PID. It is the
// sole signal used by hasActiveSession to decide whether a pre-session is
// redundant — discriminating by PID is what prevents historical sessions
// (GH #113) and concurrently-active neighbour sessions from suppressing
// new pre-sessions for freshly-opened processes.
func (s *Scanner) WithSessionChecker(fn func(projectDir string, pid int) bool) *Scanner {
	s.sessionChecker = fn
	return s
}

// WithArgvFilter sets a per-adapter predicate that excludes a matched PID by
// its argv (the adapter's Process.ExcludeArgv). When fn reports true the
// scanner skips the PID entirely — no pre-session is minted and it is never
// tracked. Returns the scanner for chaining.
func (s *Scanner) WithArgvFilter(fn func(argv []string) bool) *Scanner {
	s.argvFilter = fn
	return s
}

// HasRealSessionForPID reports whether sessions contains a real
// (transcript-backed, non-proc) session that belongs to the given PID and
// project directory. It matches by transcript path (Claude Code layout:
// ~/.claude/projects/<projectDir>/) or by CWD (codex/pi layouts where the
// transcript path doesn't encode the project directory). It is the canonical
// predicate the Scanner's sessionChecker should delegate to — encoding the
// "is a proc- pre-session redundant?" policy in one place so production
// callers and tests cannot drift apart.
func HasRealSessionForPID(sessions []*session.SessionState, projectDir string, pid int) bool {
	for _, s := range sessions {
		if strings.HasPrefix(s.SessionID, "proc-") {
			continue
		}
		if s.PID != pid {
			continue
		}
		if s.TranscriptPath == "" {
			continue
		}
		if filepath.Base(filepath.Dir(s.TranscriptPath)) == projectDir {
			return true
		}
		if s.CWD != "" && CWDToProjectDir(s.CWD) == projectDir {
			return true
		}
	}
	return false
}

// Watch begins polling. It runs an immediate scan then continues on the
// configured interval until ctx is cancelled. The interval backs off from
// defaultInterval to backoffInterval when the PID set is stable.
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
				targetInterval = backoffInterval
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
	pids, err := s.findMatchingPIDs()
	if err != nil {
		return
	}

	live := make(map[int]bool, len(pids))
	for _, pid := range pids {
		live[pid] = true
	}

	for _, pid := range pids {
		s.handleMatchedPID(pid, live)
	}

	s.probeCWDResidentTranscripts()
	s.handleExitedPIDs(live)
	s.pruneArgvVerdicts(pids)
}

// findMatchingPIDs finds the PIDs currently matching this scanner's process
// name or command-line pattern.
func (s *Scanner) findMatchingPIDs() ([]int, error) {
	if s.commandLineMatch != "" {
		return osProc.FindByCmdline(s.commandLineMatch)
	}
	return osProc.FindByName(s.processName)
}

// handleMatchedPID processes one PID from the current poll's matched set:
// applying the argv exclusion filter, then either updating its already-tracked
// pre-session or considering it for a brand-new one. live is mutated to drop
// argv-excluded PIDs so they aren't later treated as "exited".
func (s *Scanner) handleMatchedPID(pid int, live map[int]bool) {
	// Per-adapter argv exclusion: a matched binary that is the agent's
	// background infrastructure (daemon/wrapper) rather than a session.
	// Drop it from the live set too so it isn't treated as a tracked PID
	// that "exited" on the next poll. A nil argv (unreadable) is passed
	// through; the predicate must default to not-excluding in that case.
	if s.argvFilter != nil && s.argvExcluded(pid) {
		delete(live, pid)
		return
	}

	s.mu.Lock()
	_, alreadyTracked := s.tracked[pid]
	s.mu.Unlock()

	cwd, err := osProc.CWDOf(pid)
	if err != nil || cwd == "" || cwd == "/" {
		return
	}
	projectDir := CWDToProjectDir(cwd)

	if alreadyTracked {
		s.updateTrackedPID(pid, projectDir)
		return
	}

	s.maybeTrackNewPID(pid, cwd, projectDir)
}

// updateTrackedPID checks whether a real transcript has since appeared for an
// already-tracked pre-session and, if so, marks it superseded and emits its
// removal. Kept in the tracked map (rather than deleted) so the pre-session
// isn't recreated on the next poll.
func (s *Scanner) updateTrackedPID(pid int, projectDir string) {
	s.mu.Lock()
	proc := s.tracked[pid]
	s.mu.Unlock()

	// Already superseded — real transcript took over; nothing to do.
	if proc.superseded {
		return
	}

	// Check whether a real transcript has appeared since we last looked.
	if !s.hasActiveSession(projectDir, pid) {
		return
	}

	s.mu.Lock()
	proc.superseded = true
	s.tracked[pid] = proc
	s.mu.Unlock()
	s.broadcast(agent.Event{
		Type:       agent.EventRemoved,
		SessionID:  proc.sessionID,
		ProjectDir: projectDir,
	})
}

// maybeTrackNewPID starts tracking a freshly-seen PID and emits its
// EventNewSession, unless a real session already covers it or no subscriber
// is attached yet to receive the event.
func (s *Scanner) maybeTrackNewPID(pid int, cwd, projectDir string) {
	// Skip if an active transcript was recently modified in this project
	// dir (the file watcher handles those). Old transcripts from previous
	// sessions are ignored so new processes are still detected.
	if s.hasActiveSession(projectDir, pid) {
		return
	}

	sessionID := fmt.Sprintf("proc-%d", pid)
	ev := agent.Event{
		Type:       agent.EventNewSession,
		SessionID:  sessionID,
		ProjectDir: projectDir,
		CWD:        cwd,
	}

	// Track the PID and emit atomically — but only if subscribers exist.
	// If no subscribers yet (startup race with SessionDetector.Run), skip
	// tracking so the next poll retries rather than silently losing the event.
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) == 0 {
		return
	}
	s.tracked[pid] = trackedProc{sessionID: sessionID, projectDir: projectDir, cwd: cwd}
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// probeCWDResidentTranscripts is the per-adapter opt-in probe for agents that
// write transcripts per-project (e.g. aider's .aider.chat.history.md), rather
// than under a fixed watched RootDir:
//   - First sight of the file → emit EventNewSession with TranscriptPath
//   - Subsequent polls where size grew → emit EventActivity
//
// This bypasses the fswatcher (which only watches one fixed RootDir under
// $HOME and filters to .jsonl) so non-JSONL, per-project agents produce the
// same lifecycle event stream as fswatcher-friendly ones.
func (s *Scanner) probeCWDResidentTranscripts() {
	if s.transcriptFilename == "" {
		return
	}

	s.mu.Lock()
	var emit []agent.Event
	for pid, proc := range s.tracked {
		if ev, ok := s.checkTranscriptFile(&proc); ok {
			emit = append(emit, ev)
			s.tracked[pid] = proc
		}
	}
	subs := s.subs
	s.mu.Unlock()

	for _, ev := range emit {
		for _, ch := range subs {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// checkTranscriptFile stats one tracked PID's CWD-resident transcript file
// and reports the event to emit — EventNewSession on first sight or
// EventActivity on growth — mutating proc's emitted/size bookkeeping in
// place. ok is false when there's nothing to report this poll.
func (s *Scanner) checkTranscriptFile(proc *trackedProc) (agent.Event, bool) {
	if proc.cwd == "" {
		return agent.Event{}, false
	}
	path := filepath.Join(proc.cwd, s.transcriptFilename)
	info, err := os.Stat(path)
	if err != nil {
		return agent.Event{}, false
	}
	size := info.Size()

	switch {
	case !proc.transcriptEmitted:
		// First sight — announce the transcript.
		ev := agent.Event{
			Type:           agent.EventNewSession,
			SessionID:      proc.sessionID,
			ProjectDir:     proc.projectDir,
			TranscriptPath: path,
			Size:           size,
			CWD:            proc.cwd,
		}
		proc.transcriptEmitted = true
		proc.transcriptSize = size
		return ev, true
	case size != proc.transcriptSize:
		// File grew (or shrank, e.g. on rotation) — emit activity.
		ev := agent.Event{
			Type:           agent.EventActivity,
			SessionID:      proc.sessionID,
			ProjectDir:     proc.projectDir,
			TranscriptPath: path,
			Size:           size,
			CWD:            proc.cwd,
		}
		proc.transcriptSize = size
		return ev, true
	}
	return agent.Event{}, false
}

// handleExitedPIDs removes tracked pre-sessions whose PID is no longer live
// and broadcasts their removal.
func (s *Scanner) handleExitedPIDs(live map[int]bool) {
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
			SessionID:  proc.sessionID,
			ProjectDir: proc.projectDir,
		})
	}
}

// pruneArgvVerdicts drops cached argv-filter verdicts for PIDs that no longer
// matched this poll (process exited). Compares against the full matched set,
// not live — excluded PIDs were dropped from live but are still running and
// must keep their cached verdict.
func (s *Scanner) pruneArgvVerdicts(pids []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.argvVerdicts) == 0 {
		return
	}
	matched := make(map[int]bool, len(pids))
	for _, pid := range pids {
		matched[pid] = true
	}
	for pid := range s.argvVerdicts {
		if !matched[pid] {
			delete(s.argvVerdicts, pid)
		}
	}
}

// argvExcluded reports whether pid's argv marks it as agent infrastructure
// per s.argvFilter. Verdicts are cached — argv is immutable for a process's
// lifetime — so each PID pays at most one ArgvOf read. A nil argv
// (unreadable, possibly transiently) is never cached: the predicate sees nil
// (and must not exclude on it) and the read is retried on the next poll.
//
// The first excluded verdict also retires any pre-session already minted for
// this PID — by an earlier poll that couldn't read argv, or persisted by a
// daemon predating the argv filter (issue #644) — with a single
// EventRemoved. The detector deletes pre-sessions on removal and treats a
// removal for an unknown session as a no-op, so the emission is safe when no
// pre-session exists. When no subscriber is attached yet (startup race with
// SessionDetector.Run), the excluded verdict is left uncached so the next
// poll retries the emission instead of silently dropping it.
func (s *Scanner) argvExcluded(pid int) bool {
	s.mu.Lock()
	excluded, cached := s.argvVerdicts[pid]
	s.mu.Unlock()
	if cached {
		return excluded
	}

	argv, _ := osProc.ArgvOf(pid)
	excluded = s.argvFilter(argv)

	s.mu.Lock()
	proc := s.tracked[pid]
	if excluded {
		delete(s.tracked, pid)
	}
	hasSubs := len(s.subs) > 0
	if argv != nil && (!excluded || hasSubs) {
		s.argvVerdicts[pid] = excluded
	}
	s.mu.Unlock()

	if excluded && hasSubs {
		s.broadcast(agent.Event{
			Type:       agent.EventRemoved,
			SessionID:  fmt.Sprintf("proc-%d", pid),
			ProjectDir: proc.projectDir,
		})
	}
	return excluded
}

// hasActiveSession returns true iff the injected sessionChecker reports a
// real session already exists for this projectDir and PID. The per-PID
// discrimination is what prevents GH #113 (historical sessions on disk) and
// the neighbour-session supersession bug.
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
