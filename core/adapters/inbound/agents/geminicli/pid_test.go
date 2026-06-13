package geminicli

import "testing"

// stubDiscover swaps the OS-level discovery call for the duration of a test,
// capturing the disambiguate callback DiscoverPID forwards and returning
// whatever that callback selects from candidates. Restores the original on
// cleanup.
func stubDiscover(t *testing.T, candidates []int) {
	t.Helper()
	orig := discoverByCWDAndCmdLine
	discoverByCWDAndCmdLine = func(_, _ string, disambiguate func([]int) int) (int, error) {
		if disambiguate == nil {
			return 0, nil
		}
		return disambiguate(candidates), nil
	}
	t.Cleanup(func() { discoverByCWDAndCmdLine = orig })
}

// TestDiscoverPID_HonorsPassedDisambiguator proves DiscoverPID forwards the
// caller's claimed-aware disambiguator instead of hardcoding lowest-PID
// (#664). Two same-cwd launchers (lowest=100) are the candidates; a
// disambiguate that prefers the highest must win, yielding 200 — lowestPID
// would have returned 100.
func TestDiscoverPID_HonorsPassedDisambiguator(t *testing.T) {
	stubDiscover(t, []int{100, 200})

	highest := func(pids []int) int {
		best := 0
		for _, p := range pids {
			if p > best {
				best = p
			}
		}
		return best
	}

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
	stubDiscover(t, []int{100, 200})

	pid, err := DiscoverPID("/repo", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 100 {
		t.Fatalf("got pid=%d, want 100 (nil disambiguator must fall back to lowestPID)", pid)
	}
}
