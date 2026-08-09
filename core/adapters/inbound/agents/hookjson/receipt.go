// receipt.go counts, per adapter, that the hook channel delivered anything at
// all (issue #1368).
//
// It is the missing half of #1364. That issue made an event we do not
// UNDERSTAND visible; this one makes an event that never ARRIVES visible. The
// two failures look identical from every surface a user can see — permission
// reads `granted`, the config still holds our entries, no error is logged
// anywhere — and they have opposite fixes, so a single "hooks are unhealthy"
// signal would be worse than none.
//
// What this counter is NOT: a count of hook events successfully handled. It is
// deliberately incremented at the earliest point the receiver is allowed to
// observe anything, which is immediately after the consent gate. Three
// consequences, each chosen:
//
//   - A malformed body still counts. A decode failure is a different diagnosis
//     with a different fix, and it is already answered 400. For the question
//     this counter answers — "is the channel delivering?" — a 400 is a yes.
//   - A path-confinement rejection still counts (#1361). Same argument, and
//     confine.go's own counters say what is actually wrong. A channel that is
//     alive but 100% rejected must not be reported as silent, or the watchdog
//     sends its reader to the wrong file.
//   - A consent-denied request does NOT count. Everything an adapter observes
//     must be gated behind a declared permission (AGENTS.md), and "a POST
//     arrived" is an observation. It is also the honest answer: while consent
//     is withheld the daemon has no business claiming to know whether the
//     channel works.
//
// Unlike unknown.go's table this map needs no bound. Its key is the adapter's
// own compile-time AdapterName constant, never caller-supplied input, so the key
// space is closed at the size of the agent registry — the same reason
// unknown.go's per-adapter DROP counter is unbounded while its per-name table is
// not.
package hookjson

import (
	"sync"
	"sync/atomic"
)

var (
	// receiptMu guards slot allocation only. Once an adapter owns a slot every
	// later receipt goes through its atomic under the read lock — the same
	// argument unknown.go makes, and it applies harder here: this increments on
	// EVERY hook, not only on the unrecognized ones, so it sits squarely on the
	// receiver hot path.
	receiptMu sync.RWMutex
	receipts  = make(map[string]*atomic.Uint64)
)

// ObserveHookReceipt records that adapter's hook receiver was handed a
// consent-passed request.
//
// It takes no logger and writes no response on purpose. There is nothing to
// report here: a hook arriving is the healthy case, and the interesting event
// is its ABSENCE, which by definition no call site can observe. The watchdog in
// the application layer is what notices the absence, by reading HookReceipts
// against turns it counted independently.
func ObserveHookReceipt(adapter string) {
	receiptMu.RLock()
	counter := receipts[adapter]
	receiptMu.RUnlock()
	if counter != nil {
		counter.Add(1)
		return
	}

	receiptMu.Lock()
	defer receiptMu.Unlock()
	if counter := receipts[adapter]; counter != nil {
		// Lost the race to insert; the winner's slot is the live one.
		counter.Add(1)
		return
	}
	fresh := &atomic.Uint64{}
	fresh.Add(1)
	receipts[adapter] = fresh
}

// HookReceiptsFor reads one adapter's receipt total.
//
// The single-key read exists because it is the one the watchdog actually makes:
// it wants two atomics per classify pass, and reaching them through HookReceipts
// meant allocating and copying the whole table to index one key — ~19x the cost
// and 256 bytes of garbage per pass, forever, on a path that scales with session
// count rather than hook traffic. HookReceipts stays for the readers that really
// do want every row.
func HookReceiptsFor(adapter string) uint64 {
	receiptMu.RLock()
	counter := receipts[adapter]
	receiptMu.RUnlock()
	if counter == nil {
		return 0
	}
	return counter.Load()
}

// HookReceipts snapshots the per-adapter receipt totals.
//
// Zeros are absent by construction — an adapter gets a slot only when it is
// first counted — which matters to the reader more than it looks. An adapter
// missing from this map has either never received a hook or has no receiver at
// all, and those are the same fact as far as the watchdog is concerned: it only
// arms for adapters whose hook consent is granted and whose install succeeded.
//
// Monotonic for the life of the process. The watchdog compares successive
// readings rather than resetting anything, so nothing here needs to be
// clearable and two readers can never steal each other's deltas.
func HookReceipts() map[string]uint64 {
	receiptMu.RLock()
	defer receiptMu.RUnlock()
	out := make(map[string]uint64, len(receipts))
	for adapter, counter := range receipts {
		out[adapter] = counter.Load()
	}
	return out
}

// HookReceiptTotal is every hook receipt across every adapter, so the
// diagnostics bundle can say "this daemon has served N hooks" without a reader
// summing rows — the same courtesy UnknownEventTotal provides.
func HookReceiptTotal() uint64 {
	receiptMu.RLock()
	defer receiptMu.RUnlock()
	var total uint64
	for _, counter := range receipts {
		total += counter.Load()
	}
	return total
}
