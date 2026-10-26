package processlifecycle

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// FakeProcess is one row of the table SetFakeProcessesForTest installs.
type FakeProcess struct {
	Name string // the executable base name FindByName matches
	CWD  string
	Argv []string
	// Cmdline is what FindByCmdline matches; empty means the space-joined
	// Argv. Set it with a nil Argv for a process whose command line pgrep
	// lists but whose argv cannot be read (a hardened-runtime binary).
	Cmdline string
}

// SetFakeProcessesForTest replaces the process observer with a fixed table for
// the external test package processlifecycle_test, restoring it on cleanup.
// Adapter packages import this one, so an external test here is the only
// place an adapter's real process reads (its process name, its argv
// predicate, ReadArgv) can run against a fake table without an import cycle
// (#2083).
func SetFakeProcessesForTest(t testing.TB, procs map[int]FakeProcess) {
	t.Helper()
	prev := osProc
	osProc = tableObserver(procs)
	t.Cleanup(func() { osProc = prev })
}

type tableObserver map[int]FakeProcess

func (o tableObserver) FindByName(name string) ([]int, error) {
	var pids []int
	for pid, p := range o {
		if p.Name == name {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

// FindByCmdline matches pattern against each row's space-joined argv — the
// Linux observer's reading of `pgrep -f` (process_linux.go) — or its Cmdline
// when set, so an adapter declaring a CommandPattern runs its real pattern
// against the table (#2106).
func (o tableObserver) FindByCmdline(pattern string) ([]int, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	var pids []int
	for pid, p := range o {
		cmdline := p.Cmdline
		if cmdline == "" {
			cmdline = strings.Join(p.Argv, " ")
		}
		if re.MatchString(cmdline) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}
func (o tableObserver) ParentPIDOf(context.Context, int) (int, error) {
	return 0, fmt.Errorf("parent not configured")
}
func (o tableObserver) ArgvOf(pid int) ([]string, error) { return o[pid].Argv, nil }
func (o tableObserver) CWDOf(pid int) (string, error) {
	p, ok := o[pid]
	if !ok {
		return "", fmt.Errorf("no pid %d", pid)
	}
	return p.CWD, nil
}
func (o tableObserver) WriterOf(string) (int, error)              { return 0, nil }
func (o tableObserver) HoldsForWriting(int, string) (bool, error) { return false, nil }
func (o tableObserver) EnvOf(int, map[string]struct{}) (map[string]string, error) {
	return map[string]string{}, nil
}

// PollForTest runs one scan synchronously, so an external test can drive an
// adapter's real argv predicate through the scanner without Watch's ticker
// (#2105).
func (s *Scanner) PollForTest() { s.poll() }
