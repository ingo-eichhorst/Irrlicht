package processlifecycle

import "testing"

// cmdlineObserver reuses fakeObserver but returns its candidate PIDs from
// FindByCmdline (which fakeObserver leaves nil) — DiscoverPIDByCWDAndCmdLine*
// narrows the FindByCmdline result, not FindByName. ArgvOf/CWDOf are reused.
type cmdlineObserver struct {
	fakeObserver
}

func (c cmdlineObserver) FindByCmdline(string) ([]int, error) { return c.fakeObserver.pids, nil }

// TestDiscoverExcludingArgv_DropsWorkersBeforeDisambiguate drives the real
// DiscoverPIDByCWDAndCmdLineExcludingArgv loop in discovery.go (no stub of the
// discoverByCWDAndCmdLine var — that path is what the geminicli regression test
// short-circuits). Two same-cmdline, same-cwd PIDs share the launcher's cwd:
// a launcher (lower PID) and a heap-bump worker (higher PID). The default
// disambiguator picks the highest PID, so without the argv filter it would pick
// the worker. The excludeArgv predicate drops the worker before
// disambiguation; we assert the worker never reaches disambiguate and the
// launcher is returned (#664).
func TestDiscoverExcludingArgv_DropsWorkersBeforeDisambiguate(t *testing.T) {
	const (
		launcherPID = 100 // gemini launcher
		workerPID   = 200 // heap-bump worker, same cmdline + cwd, higher PID
	)
	const cwd = "/Users/x/proj"

	prev := osProc
	osProc = cmdlineObserver{fakeObserver{
		pids: []int{launcherPID, workerPID},
		cwd:  map[int]string{launcherPID: cwd, workerPID: cwd},
		argv: map[int][]string{
			launcherPID: {"node", "/path/gemini", "--foo"},
			workerPID:   {"node", "--max-old-space-size=4096", "/path/gemini"},
		},
	}}
	defer func() { osProc = prev }()

	// excludeArgv mirrors the adapter's heap-bump worker predicate.
	excludeArgv := func(argv []string) bool {
		for _, a := range argv {
			if a == "--max-old-space-size=4096" {
				return true
			}
		}
		return false
	}

	var seenByDisambiguate []int
	disambiguate := func(pids []int) int {
		seenByDisambiguate = append(seenByDisambiguate, pids...)
		// Highest-PID, same as the production default.
		best := 0
		for _, p := range pids {
			if p > best {
				best = p
			}
		}
		return best
	}

	got, err := DiscoverPIDByCWDAndCmdLineExcludingArgv("gemini", cwd, disambiguate, excludeArgv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, p := range seenByDisambiguate {
		if p == workerPID {
			t.Errorf("excluded worker pid %d reached disambiguate (%v)", workerPID, seenByDisambiguate)
		}
	}
	if got != launcherPID {
		t.Fatalf("got pid %d, want launcher %d (worker must be filtered before disambiguation)", got, launcherPID)
	}
}

// TestDiscoverExcludingArgv_NilArgvNotExcluded asserts the nil-argv-never-
// excludes contract: a candidate whose ArgvOf is nil/empty is passed to the
// predicate but must survive the filter. The single survivor (the daemon's own
// PID is excluded by narrowByCWD) is returned without calling disambiguate.
func TestDiscoverExcludingArgv_NilArgvNotExcluded(t *testing.T) {
	const nilArgvPID = 100
	const cwd = "/Users/x/proj"

	prev := osProc
	osProc = cmdlineObserver{fakeObserver{
		pids: []int{nilArgvPID},
		cwd:  map[int]string{nilArgvPID: cwd},
		argv: map[int][]string{nilArgvPID: nil}, // unreadable argv
	}}
	defer func() { osProc = prev }()

	// Per the ExcludeArgv contract the predicate must not exclude on nil argv.
	excludeArgv := func(argv []string) bool { return len(argv) > 0 }

	got, err := DiscoverPIDByCWDAndCmdLineExcludingArgv("gemini", cwd, nil, excludeArgv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nilArgvPID {
		t.Fatalf("got pid %d, want %d (nil argv must not be excluded)", got, nilArgvPID)
	}
}
