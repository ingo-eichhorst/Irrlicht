//go:build darwin

package processlifecycle

import (
	"context"
	"testing"

	"irrlicht/core/domain/session"
)

// kittenLsFixture is a minimal `kitten @ ls` response naming one window.
const kittenLsFixture = `[{"tabs":[{"windows":[{"id":7,"pid":4242,"foreground_processes":[]}]}]}]`

// TestKittyWindowIDForPIDVia_NonAnswerIsNotAnAbsentWindow is #1537's sixth
// instance: a `kitten @ ls` that could not be asked reported as "this session
// has no kitty window".
//
// Rows 1-3 are LOCKS: a resolving probe, a probe that ran and matched nothing,
// and a kitten that exited non-zero must all keep reporting probed — the last
// one because a kitty that DECLINES to describe its windows has answered, and
// treating that as a non-answer would poison completeness for a live, working
// host. Row 1 is the vacuity guard in both directions at once: it is the only
// row that produces a non-empty id.
func TestKittyWindowIDForPIDVia_NonAnswerIsNotAnAbsentWindow(t *testing.T) {
	cases := []struct {
		name       string
		build      shelloutCmd
		wantID     string
		wantProbed bool
		why        string
	}{
		{
			name: "kitten answers with a matching window", build: shellCmd("cat <<'J'\n" + kittenLsFixture + "\nJ"),
			wantID: "7", wantProbed: true,
			why: "the vacuity guard: the only row that resolves an id, so a hard-wired \"\" cannot pass the table",
		},
		{
			name: "kitten answers, no window matches", build: shellCmd("echo '[]'"),
			wantID: "", wantProbed: true,
			why: "a LOCK: an empty answer is still an answer",
		},
		{
			name: "kitten exits non-zero", build: shellCmd("exit 1"),
			wantID: "", wantProbed: true,
			why: "kitty declining to describe its windows is a verdict about kitty; this probe takes no exit-code allowlist",
		},
		{
			name: "killed by a signal", build: shellCmd("kill -9 $$"),
			wantID: "", wantProbed: false,
			why: "#1537: a killed kitten knows nothing about which window holds this session",
		},
		{
			name: "binary missing", build: missingCmd("kitten-1537"),
			wantID: "", wantProbed: false,
			why: "a fork/exec failure never reached the question",
		},
	}

	// kittenPath is a package var, and pinning it is what stops this table
	// from evaporating. kittyWindowIDForPIDVia short-circuits on an empty
	// kittenPath BEFORE reaching the injected command, so on a machine
	// without kitty every row below would pass without running — and the
	// CI runner is exactly such a machine. "The probe is absent" and "the
	// probe answered" producing the same green is the shape this whole
	// issue family is about; a test suite is not exempt from it.
	prev := kittenPath
	kittenPath = "/nonexistent/kitten-never-invoked-1537"
	t.Cleanup(func() { kittenPath = prev })

	for _, tc := range cases {
		id, probed := kittyWindowIDForPIDVia(noAggregateBudget(), "unix:/tmp/kitty-1537", 4242, tc.build)
		if id != tc.wantID || probed != tc.wantProbed {
			t.Errorf("%s: kittyWindowIDForPIDVia = (%q, %v), want (%q, %v) — %s",
				tc.name, id, probed, tc.wantID, tc.wantProbed, tc.why)
		}
	}
}

// TestKittyWindowIDForPIDVia_AbsentKittenIsASettledVerdict pins the guard's
// polarity, which is the one judgement call in #1537's sixth instance and the
// opposite of what "could not look" suggests at first reading.
//
// An absent kitten binary or an empty socket is a property of the installation,
// not a read that failed — the same reading osutil_linux.go's stubs give. The
// alternative poisons hostIdentity's completeness permanently on every machine
// where kitten is not on the trusted path, and buys nothing: the value is
// fill-only downstream and re-probed on the next refresh.
func TestKittyWindowIDForPIDVia_AbsentKittenIsASettledVerdict(t *testing.T) {
	prev := kittenPath
	kittenPath = "/nonexistent/kitten-never-invoked-1537"
	t.Cleanup(func() { kittenPath = prev })

	unreachable := missingCmd("kitten-must-not-run-1537")
	for _, tc := range []struct {
		name, socket string
		pid          int
	}{
		{"no socket", "", 4242},
		{"no session pid", "unix:/tmp/kitty-1537", 0},
	} {
		if id, probed := kittyWindowIDForPIDVia(noAggregateBudget(), tc.socket, tc.pid, unreachable); id != "" || !probed {
			t.Errorf("%s: got (%q, %v), want (\"\", true) — nothing was asked, so nothing failed", tc.name, id, probed)
		}
	}
}

// TestApplyAncestryFallbacks_UnreadableKittyWindowIsNotACompleteRead is the
// consumer half. The verdict above is worth nothing unless something reads it,
// and before #1537 nothing did: applyKittyAncestryBackfill assigned the empty
// string into l.KittyWindowID and returned void, so an aborted kitten read and
// a kitty with no matching window were the same fact by the time the caller saw
// them.
//
// This is the same shape as TestHostIdentity_KittyBackfillWalkCountsTowardCompleteness,
// which pins the sibling case — the ancestry WALK's verdict, which used to be
// discarded here too. The ancestry probe is pre-resolved so the block under
// test is the only one that runs: with TermProgram already "kitty" the three
// blocks above it are all skipped, including the one that would shell out.
func TestApplyAncestryFallbacks_UnreadableKittyWindowIsNotACompleteRead(t *testing.T) {
	fixture := func() (*session.Launcher, *ancestryProbe) {
		return &session.Launcher{TermProgram: "kitty", KittyListenOn: "unix:/tmp/kitty-1537"},
			&ancestryProbe{pid: 4242, resolved: true, term: "kitty", hostPID: 999, complete: true}
	}
	answered := func(context.Context, string, int) (string, bool) { return "7", true }
	unanswered := func(context.Context, string, int) (string, bool) { return "", false }

	// Vacuity guard: a kitten that answers must still complete, or a
	// hard-wired "incomplete" would satisfy the assertion below.
	l, ancestry := fixture()
	if complete := applyAncestryFallbacksVia(noAggregateBudget(), l, 4242, ancestry, answered); !complete || l.KittyWindowID != "7" {
		t.Errorf("a kitten that answered: got (window %q, complete %v); want (\"7\", true)", l.KittyWindowID, complete)
	}

	l, ancestry = fixture()
	if complete := applyAncestryFallbacksVia(noAggregateBudget(), l, 4242, ancestry, unanswered); complete {
		t.Error("a kitten that never answered was reported as a complete read (#1537)")
	}
}
