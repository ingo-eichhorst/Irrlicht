package antigravity

import "testing"

// stubDiscover swaps the OS-level process-name+cwd match for a fixed candidate
// set and returns whatever the disambiguator selects. Restores the original on
// cleanup.
func stubDiscover(t *testing.T, candidates []int) {
	t.Helper()
	orig := discoverByCWD
	discoverByCWD = func(_, _ string, disambiguate func([]int) int) (int, error) {
		if disambiguate == nil {
			return 0, nil
		}
		return disambiguate(candidates), nil
	}
	t.Cleanup(func() { discoverByCWD = orig })
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
// caller's claimed-aware disambiguator rather than hardcoding lowest-PID.
func TestDiscoverPID_HonorsPassedDisambiguator(t *testing.T) {
	stubDiscover(t, []int{100, 200})
	pid, err := DiscoverPID("/repo", "", highest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 200 {
		t.Fatalf("got pid=%d, want 200 (passed disambiguator must be honored)", pid)
	}
}

// TestDiscoverPID_NilDisambiguatorFallsBackToLowest preserves the nil-caller
// behavior: bind the lowest-PID match.
func TestDiscoverPID_NilDisambiguatorFallsBackToLowest(t *testing.T) {
	stubDiscover(t, []int{100, 200})
	pid, err := DiscoverPID("/repo", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 100 {
		t.Fatalf("got pid=%d, want 100 (nil disambiguator falls back to lowestPID)", pid)
	}
}

func TestLowestPID(t *testing.T) {
	if got := lowestPID([]int{7015, 6185}); got != 6185 {
		t.Errorf("lowestPID([7015 6185]) = %d, want 6185", got)
	}
	if got := lowestPID(nil); got != 0 {
		t.Errorf("lowestPID(nil) = %d, want 0", got)
	}
}
