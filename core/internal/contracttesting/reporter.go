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

// fixtureT is the slice of *testing.T an arm may use to BUILD state, and it is
// deliberately reporting-free: it carries Setenv and nothing that can decide a
// test's verdict.
//
// That narrowing is the whole guarantee. armT has to hand an arm some real
// *testing.T — env relocation is fixture machinery a recorder must not fake,
// because a fixture that cannot be built is a real failure and has to be loud,
// not recorded as the obligation firing (issue #1479's second instance, in
// miniature). But a bare *testing.T field would also carry Errorf and Fatalf,
// and an arm reporting through those bypasses the seam entirely: it could no
// longer be driven by a negative self-test, and nothing would say so.
//
// The first draft policed that with an AST tripwire over the package's own
// sources, including alias tracking for `ft := t.fixtures`. This replaces the
// guard with a type: `t.fixtures.Fatalf(...)` does not compile, in any
// spelling. It is the same move #1390 made for hook path confinement, where a
// wiring obligation ("remember to confine") became a type guarantee
// ("DecodeConfined cannot decode without a *PathConfiner") — and it is the
// better half of that trade for the same reason, since a guard is worth only
// what its deliberate failures prove and a type needs no such proof.
type fixtureT interface {
	Setenv(key, value string)
}

// armT carries both halves an arm can need: where it REPORTS, which a self-test
// swaps, and the fixture machinery it builds state with, which no self-test
// ever swaps.
//
// Only the hook_endpoint family needs this today; the other arms are pure and
// take reporter.
type armT struct {
	// reporter is embedded, so an arm calls t.Helper()/t.Fatalf() exactly as
	// it did when it held a *testing.T, and armT itself satisfies reporter.
	reporter

	// fixtures is reporting-free by type — see fixtureT.
	fixtures fixtureT
}

// realT wraps a *testing.T as the arm-facing pair used in production, where
// both halves are the same object.
func realT(t *testing.T) armT {
	return armT{reporter: t, fixtures: t}
}
