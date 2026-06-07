package processlifecycle

import (
	"fmt"
	"testing"

	"irrlicht/core/domain/agent"
)

// fakeObserver is a minimal ProcessObserver for exercising the scanner's
// per-adapter argv exclusion. Only FindByName, CWDOf, and ArgvOf are used by
// poll(); the rest satisfy the interface.
type fakeObserver struct {
	pids []int
	cwd  map[int]string
	argv map[int][]string
}

func (f fakeObserver) FindByName(string) ([]int, error)    { return f.pids, nil }
func (f fakeObserver) FindByCmdline(string) ([]int, error) { return nil, nil }
func (f fakeObserver) ArgvOf(pid int) ([]string, error)    { return f.argv[pid], nil }
func (f fakeObserver) CWDOf(pid int) (string, error)       { return f.cwd[pid], nil }
func (f fakeObserver) WriterOf(string) (int, error)        { return 0, nil }
func (f fakeObserver) EnvOf(int) (map[string]string, error) {
	return map[string]string{}, nil
}

// stubInfraFilter is a deliberately simple stand-in for an adapter's
// Process.ExcludeArgv predicate: these seam tests verify that the scanner
// honors whatever predicate the adapter declares. The real
// claudecode.IsInfraArgv rule is table-tested in its own package and
// intentionally not duplicated here (importing it from this internal test
// would also create an import cycle — claudecode imports processlifecycle).
func stubInfraFilter(argv []string) bool {
	for _, a := range argv {
		if a == "daemon" || a == "--bg-pty-host" || a == "--bg-spare" {
			return true
		}
	}
	return false
}

// countingObserver wraps fakeObserver, counting ArgvOf calls per PID and
// optionally simulating a transiently unreadable argv (nil on first read).
type countingObserver struct {
	fakeObserver
	argvCalls map[int]int
	nilFirst  map[int]bool
}

func (c *countingObserver) ArgvOf(pid int) ([]string, error) {
	c.argvCalls[pid]++
	if c.nilFirst[pid] && c.argvCalls[pid] == 1 {
		return nil, nil
	}
	return c.fakeObserver.argv[pid], nil
}

// TestPoll_ArgvFilterExcludesInfra verifies that the scanner mints a
// pre-session for a real `claude` session PID but not for cc-daemon
// infrastructure PIDs whose argv the adapter predicate rejects (issue #644).
func TestPoll_ArgvFilterExcludesInfra(t *testing.T) {
	const (
		realPID   = 100 // plain interactive `claude` → expect new_session
		daemonPID = 200 // `claude daemon run ...`     → excluded
		ptyPID    = 300 // `... --bg-pty-host ...`     → excluded
		sparePID  = 400 // `... --bg-spare ...`        → excluded
	)

	prev := osProc
	osProc = fakeObserver{
		pids: []int{realPID, daemonPID, ptyPID, sparePID},
		cwd: map[int]string{
			realPID:   "/Users/x/proj",
			daemonPID: "/Users/x",
			ptyPID:    "/Users/x/proj",
			sparePID:  "/Users/x",
		},
		argv: map[int][]string{
			realPID:   {"claude", "--resume", "abc"},
			daemonPID: {"claude", "daemon", "run", "--origin", "transient"},
			ptyPID:    {"claude", "--bg-pty-host", "/tmp/x.sock", "86", "34", "--", "claude", "--resume", "abc"},
			sparePID:  {"claude", "--bg-spare", "/tmp/y.sock"},
		},
	}
	t.Cleanup(func() { osProc = prev })

	s := NewScanner("claude", "claude-code", 0)
	// Mirror the adapter wiring: Process.ExcludeArgv → WithArgvFilter.
	s.WithArgvFilter(stubInfraFilter)

	ch := s.Subscribe()
	s.poll()

	var newSessions []int
	for {
		select {
		case ev := <-ch:
			if ev.Type == agent.EventNewSession {
				// SessionID is "proc-<pid>"; recover the PID from the tracked map.
				newSessions = append(newSessions, pidFromTracked(s, ev.SessionID))
			}
		default:
			goto done
		}
	}
done:

	if len(newSessions) != 1 || newSessions[0] != realPID {
		t.Fatalf("expected exactly one new_session for pid %d, got %v", realPID, newSessions)
	}

	// The infra PIDs must never be tracked — otherwise a later poll would treat
	// them as "exited" and emit a spurious removal.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, infra := range []int{daemonPID, ptyPID, sparePID} {
		if _, ok := s.tracked[infra]; ok {
			t.Errorf("infra pid %d was tracked but should have been excluded", infra)
		}
	}
	if _, ok := s.tracked[realPID]; !ok {
		t.Errorf("real session pid %d should be tracked", realPID)
	}
}

// pidFromTracked finds the PID whose tracked pre-session has the given
// SessionID. Returns -1 if absent.
func pidFromTracked(s *Scanner, sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for pid, proc := range s.tracked {
		if proc.sessionID == sessionID {
			return pid
		}
	}
	return -1
}

// TestPoll_ArgvVerdictCachedPerPID verifies that the scanner reads a PID's
// argv at most once — argv is immutable for a process's lifetime, so the
// verdict is cached and repeated polls must not repeat the (sysctl-backed)
// ArgvOf read for either included or excluded PIDs.
func TestPoll_ArgvVerdictCachedPerPID(t *testing.T) {
	const (
		realPID  = 100
		infraPID = 200
	)
	obs := &countingObserver{
		fakeObserver: fakeObserver{
			pids: []int{realPID, infraPID},
			cwd:  map[int]string{realPID: "/Users/x/proj", infraPID: "/Users/x"},
			argv: map[int][]string{
				realPID:  {"claude", "--resume", "abc"},
				infraPID: {"claude", "daemon", "run"},
			},
		},
		argvCalls: map[int]int{},
		nilFirst:  map[int]bool{},
	}
	prev := osProc
	osProc = obs
	t.Cleanup(func() { osProc = prev })

	s := NewScanner("claude", "claude-code", 0).WithArgvFilter(stubInfraFilter)
	_ = s.Subscribe()

	for i := 0; i < 3; i++ {
		s.poll()
	}

	for _, pid := range []int{realPID, infraPID} {
		if got := obs.argvCalls[pid]; got != 1 {
			t.Errorf("pid %d: ArgvOf called %d times across 3 polls, want 1 (verdict cached)", pid, got)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tracked[infraPID]; ok {
		t.Errorf("infra pid %d tracked despite cached excluded verdict", infraPID)
	}
	if _, ok := s.tracked[realPID]; !ok {
		t.Errorf("real pid %d should remain tracked", realPID)
	}
}

// TestPoll_ExcludedPIDRetiresPreSessionMintedOnNilArgv: a transiently
// unreadable argv (nil) must not be cached as "not infrastructure". Poll 1
// mints a pre-session because there is no evidence to exclude on; poll 2
// reads the real argv, excludes the PID, retires the just-minted pre-session
// with exactly one EventRemoved, and poll 3 serves the cached verdict without
// re-minting or re-removing.
func TestPoll_ExcludedPIDRetiresPreSessionMintedOnNilArgv(t *testing.T) {
	const infraPID = 200

	obs := &countingObserver{
		fakeObserver: fakeObserver{
			pids: []int{infraPID},
			cwd:  map[int]string{infraPID: "/Users/x"},
			argv: map[int][]string{infraPID: {"claude", "daemon", "run"}},
		},
		argvCalls: map[int]int{},
		nilFirst:  map[int]bool{infraPID: true}, // poll 1: argv unreadable
	}
	prev := osProc
	osProc = obs
	t.Cleanup(func() { osProc = prev })

	s := NewScanner("claude", "claude-code", 0).WithArgvFilter(stubInfraFilter)
	ch := s.Subscribe()

	s.poll() // argv nil → no evidence → pre-session minted
	s.poll() // argv readable → excluded → pre-session retired
	s.poll() // cached verdict → no further events

	var minted, removed int
	for {
		select {
		case ev := <-ch:
			switch ev.Type {
			case agent.EventNewSession:
				minted++
			case agent.EventRemoved:
				removed++
				if want := fmt.Sprintf("proc-%d", infraPID); ev.SessionID != want {
					t.Errorf("removal for %q, want %q", ev.SessionID, want)
				}
			}
		default:
			goto done
		}
	}
done:

	if minted != 1 {
		t.Errorf("got %d new_session events, want 1 (nil argv mints once)", minted)
	}
	if removed != 1 {
		t.Errorf("got %d removed events, want exactly 1 (retire once, no flapping)", removed)
	}
	if got := obs.argvCalls[infraPID]; got != 2 {
		t.Errorf("ArgvOf called %d times, want 2 (nil verdict not cached, real verdict cached)", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tracked[infraPID]; ok {
		t.Errorf("excluded pid %d must not remain tracked", infraPID)
	}
}

// TestPoll_ExcludedPIDEmitsRetirementForPersistedGhost: a daemon upgraded to
// the argv filter may still hold a persisted proc-<pid> session minted by
// the pre-filter version (issue #644's live ghosts). The scanner has no
// in-memory state for those, so on the first excluded verdict it must emit
// one retirement EventRemoved for proc-<pid> — the detector deletes the
// persisted pre-session, or no-ops when none exists — and none afterwards.
func TestPoll_ExcludedPIDEmitsRetirementForPersistedGhost(t *testing.T) {
	const infraPID = 200
	prev := osProc
	osProc = fakeObserver{
		pids: []int{infraPID},
		cwd:  map[int]string{infraPID: "/Users/x"},
		argv: map[int][]string{infraPID: {"claude", "daemon", "run"}},
	}
	t.Cleanup(func() { osProc = prev })

	s := NewScanner("claude", "claude-code", 0).WithArgvFilter(stubInfraFilter)
	ch := s.Subscribe()
	s.poll()
	s.poll() // cached verdict — must not emit a second removal

	var removed []string
	for {
		select {
		case ev := <-ch:
			if ev.Type == agent.EventRemoved {
				removed = append(removed, ev.SessionID)
			}
		default:
			goto done
		}
	}
done:
	if len(removed) != 1 || removed[0] != fmt.Sprintf("proc-%d", infraPID) {
		t.Fatalf("expected exactly one retirement removal for proc-%d, got %v", infraPID, removed)
	}
}
