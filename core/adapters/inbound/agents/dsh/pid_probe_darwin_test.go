//go:build darwin

package dsh

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

// TestOwnsSharedPIDAsksOnlyTheNamedPID pins #2079 for dsh: the ownership
// probe asks lsof whether the one given pid holds session.lock
// (lsof.holds_for_writing), and never runs the whole-process-table writer
// scan (lsof.writer) that DiscoverPID needs.
func TestOwnsSharedPIDAsksOnlyTheNamedPID(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	if err := os.WriteFile(filepath.Join(dir, sessionLockFilename), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	before := probeRuns()
	_ = OwnsSharedPID("", transcript, os.Getpid())
	after := probeRuns()

	if n := after["lsof.writer"] - before["lsof.writer"]; n != 0 {
		t.Errorf("OwnsSharedPID ran the system-wide lsof.writer scan %d time(s), want 0", n)
	}
	if n := after["lsof.holds_for_writing"] - before["lsof.holds_for_writing"]; n != 1 {
		t.Errorf("OwnsSharedPID ran the per-pid lsof.holds_for_writing probe %d time(s), want 1", n)
	}
}
