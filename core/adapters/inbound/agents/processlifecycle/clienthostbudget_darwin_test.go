//go:build darwin

package processlifecycle

import (
	"context"
	"slices"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// This file is #1529's behavioural half: what the attached-client loop does
// when the ONE aggregate deadline covering it runs out. The value half — that
// the deadline is small enough to be worth having — is
// TestClientHostReadFitsTheLivenessSweepTick, platform-neutral beside the
// constant.
//
// The loop is shared by both producers since #1501, so every assertion here is
// a lock on tmux's indirection as much as on herdr's; only the last test, which
// pins the DERIVATION of the budget, has to be stated once per producer.
//
// Every assertion here turns on the BUDGET rather than on a wall clock, which
// is what the issue asked for and what keeps these tests instant: the loop
// consults ctx.Err(), so a test that cancels the context at a chosen candidate
// drives the exact state a loaded machine would take seconds to reach. Nothing
// here sleeps.
//
// The probes are injected because no arrangement of live processes can be made
// to exhaust a shared budget at candidate 2 of 4 on purpose — the same reason
// ttyProbe, kittyWindowProbe and resolveHostBundleIDVia's pair are injected.

// readableHostlessCandidate is the answer a candidate gives when it WAS read
// and genuinely has no local GUI window: a real pty, no TermProgram, no
// HostBundleID, and complete=true. It is the one candidate shape that leaves
// readAll alone, so it is the only fixture with which an abandonment is the
// sole thing that can move the verdict — an unreadable candidate would poison
// readAll by itself (#1492) and hide what these tests are about.
func readableHostlessCandidate() (*session.Launcher, bool) {
	return &session.Launcher{TTY: "/dev/ttys001"}, true
}

// exhaustAt returns a hostIdentityProbe that records every candidate it is
// asked about and spends the whole remaining budget on the nth one. Cancelling
// the context is how "this candidate consumed the budget" is expressed without
// waiting for it: the loop reads ctx.Err(), which cancellation and a fired
// deadline set alike.
//
// n <= 0 never exhausts anything, which is how each test below gets its vacuity
// guard from the same fixture it makes its claim with.
func exhaustAt(n int, cancel context.CancelFunc, probed *[]int) hostIdentityProbe {
	return func(_ context.Context, pid int) (*session.Launcher, bool) {
		*probed = append(*probed, pid)
		if n > 0 && len(*probed) == n {
			cancel()
		}
		return readableHostlessCandidate()
	}
}

// TestResolveClientHostIdentity_ChecksTheBudgetBeforeEachCandidate is #1529's
// point 2, and the reason the loop tests ctx.Err() itself instead of letting
// the children inherit the deadline.
//
// Inheriting alone is not nothing — an expired context does kill every child —
// but it produces the WRONG FACT. Each remaining candidate still runs its two
// ancestry walks and its tty `ps`, each returns "I could not be read", and the
// loop then reports "I looked at all four and none was readable" where the
// truth is "I stopped looking after two". Detecting exhaustion is also the only
// way the aggregate becomes a bound a caller can observe rather than an
// accident of the children's own ceilings.
func TestResolveClientHostIdentity_ChecksTheBudgetBeforeEachCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var probed []int
	_, _ = resolveClientHostIdentityVia(ctx, clientLoopHerdr, []int{11, 12, 13, 14}, exhaustAt(2, cancel, &probed))

	if want := []int{11, 12}; !slices.Equal(probed, want) {
		t.Errorf("the budget ran out at candidate 2 and the loop went on to probe %v, want %v: "+
			"an aggregate deadline that is only INHERITED by the children is not detected by the loop, "+
			"so every remaining candidate is still probed and reported as unreadable (#1529)", probed, want)
	}

	// Vacuity guard: the same fixture with nothing exhausting the budget must
	// reach every candidate, or the assertion above would pass for a loop that
	// simply stops early.
	probed = nil
	stillLive, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	_, _ = resolveClientHostIdentityVia(stillLive, clientLoopHerdr, []int{11, 12, 13, 14}, exhaustAt(0, cancelLive, &probed))
	if want := []int{11, 12, 13, 14}; !slices.Equal(probed, want) {
		t.Errorf("with budget to spare the loop probed %v, want %v — the test above proves nothing "+
			"if the loop truncates regardless", probed, want)
	}
}

// TestResolveClientHostIdentity_AbandonedCandidateIsANonAnswer is #1529's point
// 1, and the composition with #1492 that the whole fix rests on.
//
// A candidate abandoned because the budget ran out is a candidate we DECLINED
// TO LOOK AT — the maxClientCandidates truncation's case, not the detach case.
// So it must poison readAll and the caller must be told (nil, false) = "I could
// not look". Getting this backwards is not a missing nicety: (nil, true) means
// "every attached client was read and none has a local window", which is
// exactly what makes AdoptHostIdentity clear TermProgram / HostBundleID /
// ITermSessionID / the kitty selectors from a session whose client is attached
// right now — the misroute #1348 was opened to remove and #1492 narrowed. A fix
// for a latency bug that reintroduced it would be a bad trade.
//
// The fixture is deliberately one readable, genuinely hostless candidate ahead
// of the exhaustion, so readAll is TRUE at the moment the budget runs out and
// the abandonment is the only thing that can move it.
func TestResolveClientHostIdentity_AbandonedCandidateIsANonAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var probed []int
	host, hostKnown := resolveClientHostIdentityVia(ctx, clientLoopHerdr, []int{11, 12}, exhaustAt(1, cancel, &probed))

	if host != nil {
		t.Errorf("an abandoned run named a host %+v; no candidate resolved one", host)
	}
	if hostKnown {
		t.Error("candidate 12 was never looked at because the aggregate budget ran out, and the loop " +
			"still answered (nil, true) = 'every attached client was read and none has a local window'. " +
			"That answer clears a stored host (AdoptHostIdentity), so a budget that abandons without " +
			"poisoning turns #1529's latency fix into the #1348/#1492 misroute (#1529)")
	}

	// Vacuity guard: the identical candidates read within budget ARE a complete
	// look, and must still answer "nobody has a window" — otherwise the
	// assertion above would hold for a loop that never reports a complete read.
	probed = nil
	stillLive, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	if _, hostKnown := resolveClientHostIdentityVia(stillLive, clientLoopHerdr, []int{11, 12}, exhaustAt(0, cancelLive, &probed)); !hostKnown {
		t.Error("two readable, genuinely hostless candidates read within budget is an ANSWER — " +
			"if this fails, the assertion above passes for a loop that never says 'I looked'")
	}
}

// TestResolveHerdrClientLauncher_DerivesOneBudgetOverScanAndCandidates pins the
// two structural claims the loop tests above cannot make, because those supply
// their own context: that the production resolve derives a bounded one at all,
// and that the SAME one spans both stages.
//
// One context rather than one per stage is the decision, not an implementation
// detail: two deadlines would read as a bound while summing to twice the
// budget, and the lsof scan running long is precisely the case in which the
// candidate loop needs less. What the herdr indirection costs is one number.
func TestResolveHerdrClientLauncher_DerivesOneBudgetOverScanAndCandidates(t *testing.T) {
	var scanDeadline, candidateDeadline time.Time
	var scanBounded, candidateBounded bool

	before := time.Now()
	scan := func(ctx context.Context, _ string) ([]int, bool) {
		scanDeadline, scanBounded = ctx.Deadline()
		return []int{11}, true
	}
	identify := func(ctx context.Context, _ int) (*session.Launcher, bool) {
		candidateDeadline, candidateBounded = ctx.Deadline()
		return readableHostlessCandidate()
	}
	_, _ = resolveHerdrClientLauncherVia("/tmp/irrlicht-1529/herdr.sock", scan, identify)
	after := time.Now()

	if !scanBounded {
		t.Fatal("the lsof scan ran under a context with no deadline: the herdr indirection is unbounded again (#1529)")
	}
	if !candidateBounded {
		t.Fatal("the candidate loop ran under a context with no deadline: the herdr indirection is unbounded again (#1529)")
	}
	if !scanDeadline.Equal(candidateDeadline) {
		t.Errorf("the scan and the candidate loop ran under different deadlines (%v apart): "+
			"one deadline per stage sums to twice clientHostBudget and is not the aggregate this fix states (#1529)",
			candidateDeadline.Sub(scanDeadline))
	}
	if scanDeadline.After(after.Add(clientHostBudget)) || !scanDeadline.After(before) {
		t.Errorf("the derived deadline is %v from the call, want a positive span no longer than clientHostBudget (%v)",
			scanDeadline.Sub(before), clientHostBudget)
	}
}

// TestResolveTmuxClientLauncher_DerivesOneBudgetOverScanAndCandidates is the
// #1501 twin of the test above, and it is duplicated rather than shared for the
// one reason the two resolves are not merged: what it pins is that THIS
// producer derives the budget itself. A table over both would be driven by a
// helper that derives it once, which is precisely the thing that must be
// asserted separately at each entry point.
func TestResolveTmuxClientLauncher_DerivesOneBudgetOverScanAndCandidates(t *testing.T) {
	var scanDeadline, candidateDeadline time.Time
	var scanBounded, candidateBounded bool

	before := time.Now()
	scan := func(ctx context.Context, _ string) ([]int, bool) {
		scanDeadline, scanBounded = ctx.Deadline()
		return []int{11}, true
	}
	identify := func(ctx context.Context, _ int) (*session.Launcher, bool) {
		candidateDeadline, candidateBounded = ctx.Deadline()
		return readableHostlessCandidate()
	}
	_, _ = resolveTmuxClientLauncherVia("/private/tmp/tmux-501/default", scan, identify)
	after := time.Now()

	if !scanBounded || !candidateBounded {
		t.Fatal("the tmux client indirection ran unbounded: `list-clients` is cheap, but the candidate " +
			"loop behind it is the same two ancestry walks per candidate herdr pays (#1529)")
	}
	if !scanDeadline.Equal(candidateDeadline) {
		t.Errorf("the scan and the candidate loop ran under different deadlines (%v apart): "+
			"one deadline per stage sums to twice clientHostBudget", candidateDeadline.Sub(scanDeadline))
	}
	if scanDeadline.After(after.Add(clientHostBudget)) || !scanDeadline.After(before) {
		t.Errorf("the derived deadline is %v from the call, want a positive span no longer than clientHostBudget (%v)",
			scanDeadline.Sub(before), clientHostBudget)
	}
}
