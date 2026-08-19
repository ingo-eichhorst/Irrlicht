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
		// A pid this table does not carry is a pid that does not exist, which is
		// the fact readProcInfo reports with errProcessGone since #1574 — an
		// ANSWER, and the one thing that separates this return from the killed
		// `ps` above. It said "no such process" in a bare error before, which
		// read the same to a human and to nothing else.
		return 0, "", errProcessGone
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

// TestAncestryReadsRecordWhichKindOfFailureEndedAWalk is #1574's half of this
// memo: the walks return a bare completeness bit, and this is where the REASON
// behind a false one is kept, because the memo is the only object that sees
// every per-PID read of one evaluation.
//
// The three arms are three different states of the same flag and each is
// load-bearing. A memo that reported gone unconditionally would satisfy the
// third arm alone, and every session on a healthy machine would then be filed
// under admitted.process_gone — the mirror of the defect #1574 fixed, with the
// two rows swapped. The killed-`ps` arm is the one that would go wrong if the
// flag were set for any error rather than for this one.
func TestAncestryReadsRecordWhichKindOfFailureEndedAWalk(t *testing.T) {
	ctx := context.Background()

	fresh := newAncestryReadsVia(newScriptedProcTable().read)
	if fresh.sawProcessGone() {
		t.Error("a memo that has read nothing reports a gone process — every gate evaluation would be filed under admitted.process_gone before a single `ps` ran")
	}

	answered := newScriptedProcTable()
	answered.rows[4242] = procInfoAnswer{ppid: 900, cmd: "/bin/zsh"}
	reads := newAncestryReadsVia(answered.read)
	if _, _, err := reads.probe(ctx, 4242); err != nil {
		t.Fatalf("probe of a live pid: %v", err)
	}
	if reads.sawProcessGone() {
		t.Error("a read that ANSWERED with an ancestor set the gone flag — the walk did not even end here")
	}

	killed := newScriptedProcTable()
	killed.rows[4242] = procInfoAnswer{ppid: 900, cmd: "/bin/zsh"}
	killed.fails[4242] = 1
	reads = newAncestryReadsVia(killed.read)
	if _, _, err := reads.probe(ctx, 4242); err == nil {
		t.Fatal("a killed `ps` must stay a non-answer")
	}
	if reads.sawProcessGone() {
		t.Error("a `ps` killed by its ceiling was recorded as a gone process — that is the #1534 counter and the gate row disagreeing again, in the other direction: the daemon would report a race where it has a probe that is dying")
	}

	reads = newAncestryReadsVia(newScriptedProcTable().read)
	if _, _, err := reads.probe(ctx, 4242); !errors.Is(err, errProcessGone) {
		t.Fatalf("probe of a pid the table does not carry returned %v, want errProcessGone", err)
	}
	if !reads.sawProcessGone() {
		t.Error("an ANSWERED \"no such process\" left no trace — the gate is then back to reporting it as a walk that could not be answered, which is #1574")
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
