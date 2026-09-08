// herdrselfreport.go holds the herdr pane a process reported about ITSELF,
// for the read that cannot see it from outside (#1936).
//
// # Why a process can know something the daemon cannot
//
// herdrpane.go's header states the constraint this file relaxes: for a Node
// agent that sets process.title, sysctl(kern.procargs2) returns an empty
// environment, so $HERDR_PANE_ID is invisible from outside the process. It is
// not invisible from INSIDE — irrlicht already runs code there, because the
// only way to observe pi's lifecycle at all is an extension module
// (agents/pi/extension.js). That extension now reads the two herdr variables
// out of its own process.env and posts them with the turn-end payload it was
// already sending.
//
// # Why it is still verified rather than believed
//
// A self-report is better evidence than herdrPaneForPID's scan — it is the
// pane, not a pane whose process list happens to name this pid — but it is
// evidence about a MOMENT, and this map is keyed by pid, which the kernel
// reuses. So the rule herdrpane.go established is kept exactly: cwd narrows,
// pid decides. A remembered pane is confirmed with one pane.process_info call
// against the remembered socket before it is used, and that is also what makes
// eviction a non-problem — a stale entry cannot be believed, it can only be
// dropped.
//
// The confirmation is one request on a known socket for a known pane, against
// herdrPaneForPID's pane.list plus up to maxPaneCandidates process_info calls.
// That difference, and not the correctness, is the reason to prefer the
// self-report: the answer is the same, the work to reach it is not.
package processlifecycle

import (
	"context"
	"sync"
)

// maxRememberedPanes bounds the map. Entries are one per agent process
// currently reporting itself, so the steady state is the number of live pi
// sessions — single digits on the machine #1936 was measured on. The cap is a
// guard against unbounded growth if eviction were ever to stop working, not a
// budget anyone is expected to reach, and reaching it costs a self-report
// rather than a pane: adoptHerdrPane falls through to the scan.
const maxRememberedPanes = 512

// herdrSelfReport is one process's own answer to "which pane am I in".
type herdrSelfReport struct {
	paneID     string
	socketPath string
}

// rememberedHerdrPanes is the pid → self-report map. Package-level mutable
// state with its own mutex, matching clientHostMemo in osutil_darwin.go: the
// writer is the hook receiver's goroutine and the readers are PID discovery
// and the liveness sweep, so it is reached from several goroutines by
// construction.
//
// It carries no TTL, and that is the difference from clientHostMemo rather
// than an omission. #1544's rule is that a memo's lifetime follows its
// subject's: an attached client changes by the second, so that memo expires,
// while the pane a process is in is fixed for that process's life. Expiring
// this one would drop a valid pane between two turns of a quiet session, which
// is exactly the session that has nothing else to re-report it.
var rememberedHerdrPanes = struct {
	mu      sync.Mutex
	entries map[int]herdrSelfReport
}{entries: map[int]herdrSelfReport{}}

// RememberHerdrPane records the pane pid reported for itself. Exported because
// the caller is the daemon wiring (cmd/irrlichd), which routes it here from
// the pi hook receiver under the launcher consent — the same gate every other
// launcher-identity read is taken under.
//
// Both fields are required and are stored as one: they are the pair
// clientHostFor needs, and half of it addresses nothing.
func RememberHerdrPane(pid int, paneID, socketPath string) {
	if pid <= 0 || paneID == "" || socketPath == "" {
		return
	}
	rememberedHerdrPanes.mu.Lock()
	defer rememberedHerdrPanes.mu.Unlock()
	if _, known := rememberedHerdrPanes.entries[pid]; !known &&
		len(rememberedHerdrPanes.entries) >= maxRememberedPanes {
		dropDeadHerdrReportsLocked()
		if len(rememberedHerdrPanes.entries) >= maxRememberedPanes {
			return
		}
	}
	rememberedHerdrPanes.entries[pid] = herdrSelfReport{paneID: paneID, socketPath: socketPath}
}

// dropDeadHerdrReportsLocked removes every entry whose process is gone. Called
// only when the map is at its cap, because IsAlive is a signal-0 syscall per
// entry and the ordinary path must not pay a scan.
//
// Liveness rather than age is the right eviction key for the same reason the
// map has no TTL: an entry is stale when its process ends, and nothing else
// about elapsed time says so.
func dropDeadHerdrReportsLocked() {
	for pid := range rememberedHerdrPanes.entries {
		if !IsAlive(pid) {
			delete(rememberedHerdrPanes.entries, pid)
		}
	}
}

// forgetHerdrPane drops pid's entry. Called when a report is CONTRADICTED —
// the server answered and the pane does not hold this pid — never when it
// merely could not be checked.
func forgetHerdrPane(pid int) {
	rememberedHerdrPanes.mu.Lock()
	defer rememberedHerdrPanes.mu.Unlock()
	delete(rememberedHerdrPanes.entries, pid)
}

// rememberedHerdrPane returns pid's self-report, if any.
func rememberedHerdrPane(pid int) (herdrSelfReport, bool) {
	rememberedHerdrPanes.mu.Lock()
	defer rememberedHerdrPanes.mu.Unlock()
	report, ok := rememberedHerdrPanes.entries[pid]
	return report, ok
}

// selfReportedPane returns the pane pid reported for itself, confirmed against
// the herdr server, or ok=false when there is nothing usable.
//
// The three outcomes are deliberately not two:
//
//   - No report: this process never told us, so there is nothing to confirm.
//   - Reported and confirmed: the pane holds this pid. Used.
//   - Reported and CONTRADICTED — the server answered, and the pane's process
//     list does not name pid. That is the pid-reuse case, and the entry is
//     dropped so it cannot be offered again.
//
// A server that does not answer is none of those: the report keeps its entry
// and this returns ok=false, so the caller falls through to the scan exactly
// as it would have without a report at all. Forgetting on an unanswered probe
// would discard good evidence on the strength of a probe that never ran, which
// is #1485's mistake in a different map.
func selfReportedPane(ctx context.Context, pid int) (paneID, socketPath string, ok bool) {
	report, known := rememberedHerdrPane(pid)
	if !known {
		return "", "", false
	}
	info, answered := herdrPaneProcessInfo(ctx, report.socketPath, report.paneID)
	if !answered {
		return "", "", false
	}
	if !processInfoNames(info, pid) {
		forgetHerdrPane(pid)
		return "", "", false
	}
	return report.paneID, report.socketPath, true
}
