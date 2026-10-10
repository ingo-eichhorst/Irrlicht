//go:build darwin

package codex

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// probeRuns reads how many children each processlifecycle probe kind has
// started (answered or not). The counters are process-global and only grow,
// so callers compare two readings. Darwin-only: these are lsof child counts,
// and the linux observer reads /proc without starting any child.
func probeRuns() map[string]uint64 {
	runs := map[string]uint64{}
	for _, row := range processlifecycle.ProbeCounts() {
		runs[row.Probe] = row.Answered + row.Unanswered
	}
	return runs
}

// TestOwnsSharedPIDAsksOnlyTheNamedPID pins #2079: the ownership probe asks
// lsof about the one pid it was given (lsof.holds_for_writing), and never runs
// the whole-process-table transcript-writer scan (lsof.writer) that DiscoverPID
// needs. PIDManager makes one such call per non-winning codex root on the
// shared app-server pid, on every same-PID sweep.
func TestOwnsSharedPIDAsksOnlyTheNamedPID(t *testing.T) {
	rollout := filepath.Join(t.TempDir(), "rollout-2079.jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := probeRuns()
	_ = OwnsSharedPID("", rollout, os.Getpid())
	after := probeRuns()

	if n := after["lsof.writer"] - before["lsof.writer"]; n != 0 {
		t.Errorf("OwnsSharedPID ran the system-wide lsof.writer scan %d time(s), want 0", n)
	}
	if n := after["lsof.holds_for_writing"] - before["lsof.holds_for_writing"]; n != 1 {
		t.Errorf("OwnsSharedPID ran the per-pid lsof.holds_for_writing probe %d time(s), want 1", n)
	}
}
