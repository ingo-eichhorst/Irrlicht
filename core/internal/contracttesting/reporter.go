// reporter.go holds the seam every contract family's obligations are driven
// through, and it exists for the reason issue #1479 was filed: a contract
// assertion passes by construction against a correct adapter, so its whole
// value is that it CAN fail — and until this file, the evidence that it can was
// a paragraph in a merged pull request, re-run by nothing.
//
// AGENTS.md requires a new or reworked contract assertion to land with the
// deliberate mutation seen red for each obligation. That rule works; it caught
// real vacuity twice in one run (#1453). But its product is prose, so an
// assertion that silently stops discriminating — a refactor that makes a
// comparison trivially true, a helper that starts returning the same value on
// both sides — goes on passing and looks exactly like health. That is the same
// shape as a linter that fails to run and returns empty (#1423), or a mutation
// harness that silently stops mutating (#1390, #1450): a verification mechanism
// must fail loudly when it cannot run.
//
// The fix is to let each obligation be driven against a deliberately-wrong
// fixture and to assert what it reports, WITHOUT failing the enclosing test.
// That needs one thing from the arms: they report through a narrow interface
// rather than through *testing.T directly. The seam was introduced for
// AssertPermissionGated by #1475/PR #1489 (as gateReporter) with exactly this
// generalization in mind; this file is that generalization.
//
// Where the negative self-tests live: <family>_selftest_test.go, one per
// family, plus selftest_test.go for the harness itself.
package contracttesting

import "testing"

// reporter is the slice of *testing.T a contract-family arm uses to REPORT.
//
// *testing.T satisfies it directly, so a production call site passes t
// unchanged and no family's exported entry point changes shape. A negative
// self-test passes a recorder instead, and reads back what the arm said.
//
// It is deliberately reporting-only. An arm that needs fixture machinery
// (t.TempDir, t.Setenv, a *testing.T-typed wiring field) takes armT below,
// which keeps the two halves separate on purpose — see armT.
type reporter interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// armT carries both halves an arm can need: where it REPORTS, which a self-test
// swaps, and the *testing.T its FIXTURES are built against, which no self-test
// ever swaps.
//
// The split is the point. A negative self-test substitutes the reporting so an
// obligation's failure can be observed instead of failing the run; it must NOT
// substitute t.TempDir/t.Setenv/t.Cleanup, because a fixture that cannot be
// built is a real failure of the self-test itself and has to be loud. Folding
// both into one recorder would swallow setup breakage as if it were the
// obligation firing — which is the second live instance issue #1479 records
// (an obligation graded green while the run failed incidentally elsewhere).
//
// Only the hook_endpoint family needs this today; the other arms are pure and
// take reporter.
type armT struct {
	// reporter is embedded, so an arm calls t.Helper()/t.Fatalf() exactly as
	// it did when it held a *testing.T, and armT itself satisfies reporter.
	reporter

	// fixtures is the real *testing.T. Use it ONLY for fixture machinery.
	// Reporting through it bypasses the seam and silently un-does this file;
	// TestNoArmReportsThroughTheFixtureT is the tripwire that says so.
	fixtures *testing.T
}

// realT wraps a *testing.T as the arm-facing pair used in production, where
// both halves are the same object.
func realT(t *testing.T) armT {
	return armT{reporter: t, fixtures: t}
}
