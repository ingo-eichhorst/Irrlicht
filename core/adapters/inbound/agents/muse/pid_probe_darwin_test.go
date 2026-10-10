//go:build darwin

package muse

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

// TestOwnsSharedPIDAsksOnlyTheNamedPID pins #2079 for muse: the ownership
// probe asks lsof about the one given pid (lsof.holds_for_writing), once for
// the .session.lock and once for the session.jsonl fallback when the pid
// holds neither, and never runs the whole-process-table writer scan
// (lsof.writer) that DiscoverPID needs.
func TestOwnsSharedPIDAsksOnlyTheNamedPID(t *testing.T) {
	transcript := newSessionDir(t, filepath.Join(t.TempDir(), "01a1181a-0000-7000-8000-000000002079"))
	before := probeRuns()
	_ = OwnsSharedPID("", transcript, os.Getpid())
	after := probeRuns()

	if n := after["lsof.writer"] - before["lsof.writer"]; n != 0 {
		t.Errorf("OwnsSharedPID ran the system-wide lsof.writer scan %d time(s), want 0", n)
	}
	if n := after["lsof.holds_for_writing"] - before["lsof.holds_for_writing"]; n != 2 {
		t.Errorf("OwnsSharedPID ran the per-pid lsof.holds_for_writing probe %d time(s), want 2 (lock, then transcript)", n)
	}
}
