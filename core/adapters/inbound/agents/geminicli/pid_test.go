package geminicli

import "testing"

// stubDiscover swaps the OS-level discovery call for the duration of a test.
// candidates maps each candidate PID to its argv, faithfully modelling
// DiscoverPIDByCWDAndCmdLineExcludingArgv: it applies the excludeArgv predicate
// DiscoverPID forwards (dropping heap-bump workers), then returns whatever the
// disambiguate callback selects from the survivors. Restores the original on
// cleanup.
func stubDiscover(t *testing.T, candidates map[int][]string) {
	t.Helper()
	orig := discoverByCWDAndCmdLine
	discoverByCWDAndCmdLine = func(_, _ string, disambiguate func([]int) int, excludeArgv func([]string) bool) (int, error) {
		var kept []int
		for pid, argv := range candidates {
			if excludeArgv != nil && excludeArgv(argv) {
				continue
			}
			kept = append(kept, pid)
		}
		if disambiguate == nil {
			return 0, nil
		}
		return disambiguate(kept), nil
	}
	t.Cleanup(func() { discoverByCWDAndCmdLine = orig })
}

// launcherArgv / workerArgv build realistic Gemini CLI command lines so
// isHeapBumpWorker can classify them: the launcher is `node .../bin/gemini`,
// the worker re-execs with `--max-old-space-size`.
func launcherArgv() []string { return []string{"node", "/opt/gemini/bin/gemini"} }
func workerArgv() []string {
	return []string{"node", "--max-old-space-size=4096", "/opt/gemini/bin/gemini"}
}

func highest(pids []int) int {
	best := 0
	for _, p := range pids {
		if p > best {
			best = p
		}
	}
	return best
}

// TestDiscoverPID_HonorsPassedDisambiguator proves DiscoverPID forwards the
// caller's claimed-aware disambiguator instead of hardcoding lowest-PID
// (#664). Two same-cwd launchers (lowest=100) are the candidates; a
// disambiguate that prefers the highest must win, yielding 200 — lowestPID
// would have returned 100.
func TestDiscoverPID_HonorsPassedDisambiguator(t *testing.T) {
	stubDiscover(t, map[int][]string{100: launcherArgv(), 200: launcherArgv()})

	pid, err := DiscoverPID("/repo", "", highest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 200 {
		t.Fatalf("got pid=%d, want 200 (passed disambiguator must be honored, not lowestPID)", pid)
	}
}

// TestDiscoverPID_NilDisambiguatorFallsBackToLowest preserves the existing
// behavior for nil callers: bind the lowest-PID launcher ancestor.
func TestDiscoverPID_NilDisambiguatorFallsBackToLowest(t *testing.T) {
	stubDiscover(t, map[int][]string{100: launcherArgv(), 200: launcherArgv()})

	pid, err := DiscoverPID("/repo", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 100 {
		t.Fatalf("got pid=%d, want 100 (nil disambiguator must fall back to lowestPID)", pid)
	}
}

// TestDiscoverPID_ExcludesHeapBumpWorker is the #664 review regression. The raw
// pgrep-style match returns BOTH launchers and their higher-PID heap-bump
// workers (same cwd, same bin/gemini cmdline). With claimed-aware
// disambiguation (prefer highest unclaimed PID) two same-cwd sessions must each
// bind their OWN launcher — never a worker, never a shared PID. Before the fix
// the worker reached the disambiguator and "highest unclaimed" picked it.
//
// Candidate set: launcherA=100, workerA=150, launcherB=200, workerB=250.
func TestDiscoverPID_ExcludesHeapBumpWorker(t *testing.T) {
	const (
		launcherA = 100
		workerA   = 150
		launcherB = 200
		workerB   = 250
	)
	stubDiscover(t, map[int][]string{
		launcherA: launcherArgv(),
		workerA:   workerArgv(),
		launcherB: launcherArgv(),
		workerB:   workerArgv(),
	})

	// Model PIDManager.TryDiscoverPID: prefer the highest PID not already
	// claimed by an earlier session.
	claimed := map[int]bool{}
	highestUnclaimed := func(pids []int) int {
		best := 0
		for _, p := range pids {
			if !claimed[p] && p > best {
				best = p
			}
		}
		return best
	}

	// First same-cwd session binds: workers excluded → {100, 200}; highest
	// unclaimed = launcherB.
	pid1, err := DiscoverPID("/repo", "", highestUnclaimed)
	if err != nil {
		t.Fatalf("session 1: unexpected error: %v", err)
	}
	if pid1 != launcherB {
		t.Fatalf("session 1 bound pid=%d, want launcherB=%d (workers must be excluded)", pid1, launcherB)
	}
	if pid1 == workerA || pid1 == workerB {
		t.Fatalf("session 1 bound a heap-bump worker pid=%d", pid1)
	}
	claimed[pid1] = true

	// Second same-cwd session: launcherB claimed → highest unclaimed = launcherA.
	pid2, err := DiscoverPID("/repo", "", highestUnclaimed)
	if err != nil {
		t.Fatalf("session 2: unexpected error: %v", err)
	}
	if pid2 != launcherA {
		t.Fatalf("session 2 bound pid=%d, want launcherA=%d (its own launcher, not shared)", pid2, launcherA)
	}
	if pid2 == workerA || pid2 == workerB {
		t.Fatalf("session 2 bound a heap-bump worker pid=%d", pid2)
	}
	if pid2 == pid1 {
		t.Fatalf("both sessions bound the same pid=%d (must each bind their own launcher)", pid1)
	}
}
