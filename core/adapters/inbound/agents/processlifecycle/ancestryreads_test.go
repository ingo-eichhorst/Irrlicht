package processlifecycle

import (
	"context"
	"errors"
	"testing"
)

// This file covers ancestryReads (osutil.go) — #1544's per-resolve dedup of the
// `ps` reads the two ancestry walks share. It is deliberately cross-platform:
// the memo itself makes no shellout, and its two rules — what it admits, and
// how long it lives — are the whole of the correctness claim.
//
// The lifetime rule is the one that is a DEFECT rather than a slowdown when it
// is got wrong, so TestAncestryReadsAreScopedToOneResolveNotTheProcess is the
// assertion to keep if any of these are ever pruned.

// scriptedProcTable is a mutable stand-in for the process table: entries can be
// changed between resolves, which is the property a per-resolve memo must
// observe and a process-global one cannot.
type scriptedProcTable struct {
	rows  map[int]procInfoAnswer
	fails map[int]int // pid -> how many more reads must fail before answering
	reads map[int]int // pid -> how many times the underlying read actually ran
}

func newScriptedProcTable() *scriptedProcTable {
	return &scriptedProcTable{
		rows:  map[int]procInfoAnswer{},
		fails: map[int]int{},
		reads: map[int]int{},
	}
}

func (p *scriptedProcTable) read(ctx context.Context, pid int) (int, string, error) {
	p.reads[pid]++
	if p.fails[pid] > 0 {
		p.fails[pid]--
		// The shape of a `ps` the ceiling killed: no answer, nothing learned
		// about this pid.
		return 0, "", errors.New("ps: signal: killed")
	}
	row, ok := p.rows[pid]
	if !ok {
		return 0, "", errors.New("no such process")
	}
	return row.ppid, row.cmd, nil
}

func TestAncestryReadsCollapseRepeatReadsOfOnePID(t *testing.T) {
	table := newScriptedProcTable()
	table.rows[4242] = procInfoAnswer{ppid: 900, cmd: "/bin/zsh"}
	table.rows[900] = procInfoAnswer{ppid: 1, cmd: "/sbin/launchd"}
	reads := newAncestryReadsVia(table.read)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		ppid, cmd, err := reads.probe(ctx, 4242)
		if err != nil || ppid != 900 || cmd != "/bin/zsh" {
			t.Fatalf("probe %d returned (%d, %q, %v), want (900, %q, nil)", i, ppid, cmd, err, "/bin/zsh")
		}
	}
	if _, _, err := reads.probe(ctx, 900); err != nil {
		t.Fatalf("probe of a second pid: %v", err)
	}

	if table.reads[4242] != 1 {
		t.Fatalf("five probes of pid 4242 ran %d underlying reads, want 1", table.reads[4242])
	}
	// The vacuity guard: a memo that answered EVERYTHING from one entry would
	// also satisfy the assertion above.
	if table.reads[900] != 1 {
		t.Fatalf("a distinct pid ran %d underlying reads, want 1", table.reads[900])
	}
}

func TestAncestryReadsNeverMemoizeANonAnswer(t *testing.T) {
	table := newScriptedProcTable()
	table.rows[4242] = procInfoAnswer{ppid: 900, cmd: "/bin/zsh"}
	table.fails[4242] = 2 // the first two reads are killed by their ceiling
	reads := newAncestryReadsVia(table.read)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, _, err := reads.probe(ctx, 4242); err == nil {
			t.Fatalf("probe %d: a killed `ps` must stay a non-answer", i)
		}
	}
	// The walk that comes after it must get a real second chance. Memoizing the
	// failure would freeze one loaded moment into a verdict for the whole
	// resolve — the #1524 collapse, one shellout over.
	ppid, cmd, err := reads.probe(ctx, 4242)
	if err != nil || ppid != 900 || cmd != "/bin/zsh" {
		t.Fatalf("after two non-answers, probe returned (%d, %q, %v), want (900, %q, nil)", ppid, cmd, err, "/bin/zsh")
	}
	if table.reads[4242] != 3 {
		t.Fatalf("underlying read ran %d times, want 3 (two non-answers re-probed, then one answer)", table.reads[4242])
	}
	// And the answer that finally arrived IS memoized.
	if _, _, err := reads.probe(ctx, 4242); err != nil {
		t.Fatalf("fourth probe: %v", err)
	}
	if table.reads[4242] != 3 {
		t.Fatalf("the answer was not memoized: underlying read ran %d times, want still 3", table.reads[4242])
	}
}

// TestAncestryReadsAreScopedToOneResolveNotTheProcess is the correctness claim
// of #1544's second half, and it is the one thing the two memos in that change
// must NOT have in common.
//
// bundleIDForAppPath memoizes an immutable fact for the life of the process. A
// ppid is not immutable: the process table changes constantly, and a PID that
// is reaped is REUSED. A process-global memo here would answer the next
// resolve with the ancestry of whatever process last held that number — which
// resolves a session's click-to-focus target to an unrelated window, and feeds
// the #784 admission gate a lineage that is not the candidate's.
func TestAncestryReadsAreScopedToOneResolveNotTheProcess(t *testing.T) {
	table := newScriptedProcTable()
	table.rows[4242] = procInfoAnswer{ppid: 900, cmd: "/bin/zsh"}
	ctx := context.Background()

	first := newAncestryReadsVia(table.read)
	if ppid, _, err := first.probe(ctx, 4242); err != nil || ppid != 900 {
		t.Fatalf("first resolve: (%d, %v), want (900, nil)", ppid, err)
	}

	// 4242 exits, is reaped, and the number is handed to an unrelated process.
	table.rows[4242] = procInfoAnswer{ppid: 7, cmd: "/usr/bin/unrelated"}

	second := newAncestryReadsVia(table.read)
	ppid, cmd, err := second.probe(ctx, 4242)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if ppid != 7 || cmd != "/usr/bin/unrelated" {
		t.Fatalf("second resolve saw (%d, %q) — a memo that outlived its resolve answered from the reaped process; want (7, %q)",
			ppid, cmd, "/usr/bin/unrelated")
	}
}
