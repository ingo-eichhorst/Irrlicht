package processlifecycle

import (
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
	// Mirror the adapter wiring: Process.ExcludeArgv → WithArgvFilter. The
	// predicate matches claudecode.IsInfraArgv's rule (kept here to avoid a
	// cross-package import in this seam test).
	s.WithArgvFilter(func(argv []string) bool {
		if len(argv) < 2 {
			return false
		}
		if len(argv) >= 3 && argv[1] == "daemon" && argv[2] == "run" {
			return true
		}
		for _, a := range argv[1:] {
			if a == "--bg-pty-host" || a == "--bg-spare" {
				return true
			}
		}
		return false
	})

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
