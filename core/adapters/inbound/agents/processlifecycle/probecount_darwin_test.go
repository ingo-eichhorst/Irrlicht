//go:build darwin

package processlifecycle

import (
	"context"
	"testing"

	"irrlicht/core/domain/session"
)

// This file is the darwin half of #1534's coverage: the runtime proof that the
// PRODUCTION call sites reach the counter under their own kind, rather than the
// mechanism-only proof probecount_test.go gives by calling runProbe directly.
//
// It covers four of the eight sites — the four whose shellout is injected, one
// per tool, and both classifier polarities (probeAnswered's empty form at
// ps.tty and plutil.bundle_id, the allowlist form at lsof.writer and
// pgrep.discover). The other four (ps.proc_info, lsof.cwd, lsof.herdr_clients,
// kitten.window) take no builder, so what each of them PASSES is graded
// statically by TestEveryProbeSiteDeclaresItsOwnKind instead. Saying which half
// covers which is the point: a reader must not have to assume the runtime test
// reaches sites it cannot.

// TestProductionProbeSitesCountUnderTheirOwnKind drives each injectable site
// once answering and once not, and asserts the delta lands on that site's kind
// and nowhere else.
func TestProductionProbeSitesCountUnderTheirOwnKind(t *testing.T) {
	ctx := context.Background()

	t.Run("ps.tty", func(t *testing.T) {
		before := readProbeLedger()
		if _, probed := processTTYVia(ctx, 1, answeringCmd("0")); !probed {
			t.Fatal("a ps that exited 0 must report probed=true")
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"ps.tty": {Probe: "ps.tty", Answered: 1},
		}, "processTTYVia's shellout is the ps.tty probe")

		before = readProbeLedger()
		if _, probed := processTTYVia(ctx, 1, missingBinaryCmd()); probed {
			t.Fatal("a ps that never started must report probed=false — #1533")
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"ps.tty": {Probe: "ps.tty", Unanswered: 1},
		}, "the counter must agree with the site's own probed bit on the case that matters")
	})

	t.Run("plutil.bundle_id", func(t *testing.T) {
		answering := func(plist string) shelloutCmd { return answeringCmd("0") }
		missing := func(plist string) shelloutCmd { return missingBinaryCmd() }

		before := readProbeLedger()
		if _, err := bundleIDVia(ctx, "/Applications/Nothing.app", answering); err != nil {
			t.Fatalf("a plutil that exited 0 must not error: %v", err)
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"plutil.bundle_id": {Probe: "plutil.bundle_id", Answered: 1},
		}, "bundleIDVia's shellout is the plutil.bundle_id probe")

		before = readProbeLedger()
		if _, err := bundleIDVia(ctx, "/Applications/Nothing.app", missing); err == nil {
			t.Fatal("a plutil that never started must return an error — #1524")
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"plutil.bundle_id": {Probe: "plutil.bundle_id", Unanswered: 1},
		}, "the plutil non-answer is the one #1524's fail-open admission gate turns on")
	})

	t.Run("lsof.writer", func(t *testing.T) {
		before := readProbeLedger()
		if _, err := writerOfVia("/tmp/probe-1534", answeringCmd("0")); err != nil {
			t.Fatalf("an lsof that exited 0 must not error: %v", err)
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"lsof.writer": {Probe: "lsof.writer", Answered: 1},
		}, "writerOfVia's shellout is the lsof.writer probe")

		// Exit 1 is lsofNothingToReport — an ANSWER at the call site and an
		// answer here too, which is the one place the two definitions could
		// have been made to disagree and deliberately were not.
		before = readProbeLedger()
		if _, err := writerOfVia("/tmp/probe-1534", answeringCmd("1")); err != nil {
			t.Fatalf("lsof exit 1 is 'nothing to report', an answer: %v", err)
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"lsof.writer": {Probe: "lsof.writer", Answered: 1},
		}, "lsof's 'nothing to report' ran and answered; counting it as a non-answer would report a healthy machine as failing")
	})

	t.Run("pgrep.discover", func(t *testing.T) {
		before := readProbeLedger()
		if _, err := runPgrepVia("-x", "nothing", answeringCmd("1")); err != nil {
			t.Fatalf("pgrep exit 1 is 'no match', an answer: %v", err)
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"pgrep.discover": {Probe: "pgrep.discover", Answered: 1},
		}, "runPgrepVia's shellout is the pgrep.discover probe")

		before = readProbeLedger()
		if _, err := runPgrepVia("-x", "nothing", missingBinaryCmd()); err == nil {
			t.Fatal("a pgrep that never started must return an error")
		}
		assertProbesMoved(t, before, map[string]ProbeCount{
			"pgrep.discover": {Probe: "pgrep.discover", Unanswered: 1},
		}, "a pgrep the ceiling killed reports 'no such process' to discovery unless something counts it")
	})
}

// TestBundleIDMemoHitIsCounted drives the process-global memo #1544 put in
// FRONT of the plutil probe. Its first resolve reaches the injected probe (not
// runProbe, so nothing is counted for it here); its second is served from the
// memo, which is the call runProbe's counter cannot see.
//
// A fresh memo rather than the package-global bundleIDs, for the reason that
// memo's own comment gives: nothing in the suite should depend on the order
// tests run in.
func TestBundleIDMemoHitIsCounted(t *testing.T) {
	memo := &bundleIDMemo{ids: map[string]string{}}
	probe := func(ctx context.Context, appPath string) (string, error) { return "md.obsidian", nil }
	ctx := context.Background()
	if _, err := memo.resolve(ctx, "/Applications/Obsidian.app", probe); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	before := readProbeLedger()
	if _, err := memo.resolve(ctx, "/Applications/Obsidian.app", probe); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"plutil.bundle_id": {Probe: "plutil.bundle_id", MemoHits: 1},
	}, "#1544's hand-back: a memo hides how often the probe RUNS, so a hit is its own outcome rather than an unreported one")
}

// TestHerdrCandidateAbandonmentIsCounted is #1558 inside #1534.
//
// The loop's budget break produces the same (nil, false) a failed lsof
// produces, so on a machine where the scan is chronically slow-but-successful a
// herdr host would never resolve and nothing anywhere would say why. The
// probed figure is the denominator: without it "12 abandoned" cannot be read.
func TestHerdrCandidateAbandonmentIsCounted(t *testing.T) {
	identify := func(ctx context.Context, pid int) (*session.Launcher, bool) {
		return &session.Launcher{}, true
	}

	t.Run("a live budget probes every candidate and abandons none", func(t *testing.T) {
		before := HerdrCandidates()
		if _, readAll := resolveClientHostIdentityVia(context.Background(), []int{11, 12}, identify); !readAll {
			t.Fatal("every candidate was read and none has a host: that is an ANSWER (#1492)")
		}
		got := HerdrCandidates()
		if probed := got.Probed - before.Probed; probed != 2 {
			t.Errorf("probed moved by %d, want 2 — the denominator must count what the loop actually asked about", probed)
		}
		if abandoned := got.AbandonedOnBudget - before.AbandonedOnBudget; abandoned != 0 {
			t.Errorf("abandoned moved by %d, want 0 — a loop that ran to the end abandoned nothing, and a counter that fires anyway measures nothing", abandoned)
		}
	})

	t.Run("a spent budget abandons once and probes nothing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		before := HerdrCandidates()
		if _, readAll := resolveClientHostIdentityVia(ctx, []int{11, 12}, identify); readAll {
			t.Fatal("candidates we declined to look at must poison the answer (#1529)")
		}
		got := HerdrCandidates()
		if abandoned := got.AbandonedOnBudget - before.AbandonedOnBudget; abandoned != 1 {
			t.Errorf("abandoned moved by %d, want 1 — one event per loop, not one per candidate left unread", abandoned)
		}
		if probed := got.Probed - before.Probed; probed != 0 {
			t.Errorf("probed moved by %d, want 0 — the loop broke before asking about anything", probed)
		}
	})
}
