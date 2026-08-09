package hookjson

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// receiptTestSeq keeps every test's adapter name unique. The counters are
// process-global and never reset — the same constraint unknown_test.go works
// under — so a fixed literal would make two tests in one binary share a slot
// and steal each other's deltas.
var receiptTestSeq atomic.Uint64

func receiptAdapter(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("irr-receipt-test-%d", receiptTestSeq.Add(1))
}

func TestObserveHookReceipt_CountsPerAdapter(t *testing.T) {
	a, b := receiptAdapter(t), receiptAdapter(t)

	for i := 0; i < 3; i++ {
		ObserveHookReceipt(a)
	}
	ObserveHookReceipt(b)

	snap := HookReceipts()
	if snap[a] != 3 {
		t.Errorf("adapter %q counted %d, want 3", a, snap[a])
	}
	if snap[b] != 1 {
		t.Errorf("adapter %q counted %d, want 1 — a second adapter must get its own key, or one live channel would vouch for a dead one", b, snap[b])
	}
}

// An adapter that has received nothing must be ABSENT rather than present with
// a zero. The watchdog reads a missing key and a zero identically, but a human
// reading hooks.json does not: an explicit zero from a counter looks like a
// measurement, and this counter cannot distinguish "no hooks yet" from "no
// receiver". The row that carries that judgement is the watchdog's, which knows
// whether the channel was armed.
func TestHookReceipts_OmitsAdaptersWithNoTraffic(t *testing.T) {
	quiet := receiptAdapter(t)
	if _, ok := HookReceipts()[quiet]; ok {
		t.Errorf("adapter %q with no receipts appears in the snapshot", quiet)
	}
}

func TestHookReceipts_SnapshotDoesNotAliasLiveState(t *testing.T) {
	a := receiptAdapter(t)
	ObserveHookReceipt(a)

	snap := HookReceipts()
	snap[a] = 999
	if HookReceipts()[a] != 1 {
		t.Error("mutating a snapshot changed the live counters — the aliasing bug this repo has already paid for once")
	}
}

func TestHookReceiptTotal_SumsEveryAdapter(t *testing.T) {
	a, b := receiptAdapter(t), receiptAdapter(t)
	before := HookReceiptTotal()

	ObserveHookReceipt(a)
	ObserveHookReceipt(a)
	ObserveHookReceipt(b)

	if got := HookReceiptTotal() - before; got != 3 {
		t.Errorf("total rose by %d, want 3", got)
	}
}

// The receiver is an HTTP handler, so concurrent increments are the normal
// case, and the first ones race to allocate the adapter's slot. Under -race
// this fails on an unsynchronised map and on a lost-update allocation.
func TestObserveHookReceipt_IsConcurrencySafe(t *testing.T) {
	a := receiptAdapter(t)
	const goroutines, each = 8, 250

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				ObserveHookReceipt(a)
			}
		}()
	}
	wg.Wait()

	if got := HookReceipts()[a]; got != goroutines*each {
		t.Errorf("counted %d, want %d — a lost update makes a busy channel look quieter than it is", got, goroutines*each)
	}
}
