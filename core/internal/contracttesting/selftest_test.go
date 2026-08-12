// selftest_test.go is the harness every family's negative self-tests are
// written against, plus the harness's own deliberate mutation.
//
// Each <family>_selftest_test.go drives one obligation against a fixture that
// is WRONG in exactly one way and asserts the obligation reports it. That turns
// the mutation evidence AGENTS.md requires — until now a paragraph in a merged
// PR body, re-run by nothing — into a lock that runs on every build (#1479).
//
// # Why an obligation must be named, not merely "something failed"
//
// verdictReported takes a fragment of the obligation's OWN failure message and
// will not accept a bare failure. That is not fussiness: it is the second live
// instance #1479 records. In #1453, `--port 7837` left the obligation that was
// supposed to catch it (delivery_carries_no_address) GREEN — the run failed
// incidentally elsewhere, so that obligation's own power was never
// demonstrated, and a self-test asserting only "the arm failed" would have
// certified it. The same trap has a fixture-shaped twin, recorded in #1446: a
// mutation harness whose mutations silently stopped applying reported "all
// green" for the two that mattered most.
//
// # Why the harness has a mutation of its own
//
// A recorder that stopped observing would be the failure being fixed,
// reproduced inside its own fix. Two broken recorders are therefore COMMITTED
// below — deafRecorder and criesWolfRecorder — and driven through the same
// verdict functions the real self-tests use, so the claim "these self-tests
// depend on what was actually observed" is itself re-run rather than asserted
// in prose. See TestTheVerdictsDependOnWhatTheRecorderSaw and
// TestTheRecorderObservesWhatAnArmReports, which cover the two halves: the
// decision procedure, and the recorder feeding it.
package contracttesting

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// --- the recorder ---

// recordingT captures what a contract arm reports instead of failing the
// enclosing test. It is the swappable half of armT; the fixture half stays a
// real *testing.T, so a fixture that cannot be built still fails loudly.
type recordingT struct {
	mu      sync.Mutex
	errs    []string
	fatal   bool
	helpers int
}

func newRecordingT() *recordingT { return &recordingT{} }

// Helper is counted rather than ignored, so the tripwire test can confirm the
// arms still mark themselves — a missing t.Helper() moves a failure's reported
// line into this file and makes every contract failure point at the harness.
func (r *recordingT) Helper() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.helpers++
}

func (r *recordingT) Error(args ...any) { r.record(fmt.Sprint(args...)) }

func (r *recordingT) Errorf(format string, args ...any) {
	r.record(fmt.Sprintf(format, args...))
}

func (r *recordingT) Fatal(args ...any) {
	r.record(fmt.Sprint(args...))
	r.abort()
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.record(fmt.Sprintf(format, args...))
	r.abort()
}

func (r *recordingT) record(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, msg)
}

// abort reproduces *testing.T's Fatal semantics. A recorder that merely noted
// the failure and returned would let the arm keep running past a precondition
// it just declared unmet — so an obligation whose Fatalf guards the assertions
// below it would be graded on state its own guard rejected.
func (r *recordingT) abort() {
	r.mu.Lock()
	r.fatal = true
	r.mu.Unlock()
	runtime.Goexit()
}

func (r *recordingT) reported() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs) > 0
}

func (r *recordingT) report() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.errs, "; ")
}

func (r *recordingT) aborted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fatal
}

// --- driving an arm ---

// observe runs arm against a recorder and returns what it reported.
//
// The goroutine is not incidental: Fatal and Fatalf end in runtime.Goexit, so
// an arm that hits a fatal must terminate a goroutine that is not the test's.
// The deferred close runs on that path too, which is what makes an aborted arm
// observable rather than a hang.
func observe(t *testing.T, arm func(armT)) *recordingT {
	t.Helper()
	rec := newRecordingT()
	done := make(chan struct{})
	go func() {
		defer close(done)
		arm(armT{reporter: rec, fixtures: t})
	}()
	<-done
	return rec
}

// --- the verdicts every negative self-test goes through ---

// observation is what a self-test reads back off a driven arm. It is an
// interface so the committed broken recorders below can be substituted for the
// real one, which is how the harness proves its own verdicts are not vacuous.
type observation interface {
	reported() bool
	report() string
}

// verdictReported reports why obs fails to demonstrate that the obligation
// under test fired, or "" when it does.
//
// want is a fragment of that obligation's own failure message. It is required,
// because "the arm failed" and "this obligation failed" are different claims
// and only the second is evidence — see the file comment.
func verdictReported(obs observation, want string) string {
	if want == "" {
		return "no expected message fragment was named — a negative self-test that accepts any " +
			"failure certifies an obligation that may not have run at all (#1453); pass a " +
			"distinctive fragment of the obligation's own message"
	}
	if !obs.reported() {
		return fmt.Sprintf("the obligation reported nothing against a fixture that is wrong for it — "+
			"either the fixture is no longer wrong, or the obligation has stopped discriminating "+
			"(expected a message containing %q)", want)
	}
	if !strings.Contains(obs.report(), want) {
		return fmt.Sprintf("the arm failed, but NOT on the obligation under test: wanted a message "+
			"containing %q, got: %s", want, obs.report())
	}
	return ""
}

// verdictSilent reports why obs fails to demonstrate that a CORRECT fixture is
// passed, or "" when it does. Every family needs at least one: without it an
// obligation that reported unconditionally would satisfy every negative case
// and read as excellent coverage.
func verdictSilent(obs observation) string {
	if obs.reported() {
		return "the obligation reported against a fixture that is CORRECT for it — an assertion " +
			"that fires unconditionally satisfies every negative self-test while discriminating " +
			"nothing: " + obs.report()
	}
	return ""
}

// mustReport fails the enclosing test unless the obligation named by want was
// the one that fired. what names the mutation, so the failure says which
// deliberate wrong fixture stopped being caught.
func mustReport(t *testing.T, obs observation, want, what string) {
	t.Helper()
	if v := verdictReported(obs, want); v != "" {
		t.Fatalf("mutation %q: %s", what, v)
	}
}

// mustBeSilent fails the enclosing test unless the obligation passed the
// correct fixture — the per-family vacuity guard.
func mustBeSilent(t *testing.T, obs observation, what string) {
	t.Helper()
	if v := verdictSilent(obs); v != "" {
		t.Fatalf("vacuity guard %q: %s", what, v)
	}
}

// --- the harness's own deliberate mutation, committed ---

// brokenRecorder is a recorder that does not work, kept in the tree rather than
// described in a PR body — which is the whole point of #1479 applied to the fix
// itself. The two instances below are the only two ways a recorder can be
// broken: it can fail to observe, or it can observe indiscriminately.
type brokenRecorder struct {
	name string
	did  bool
	text string
}

func (b brokenRecorder) reported() bool { return b.did }

func (b brokenRecorder) report() string { return b.text }

var (
	// deafRecorder records nothing, whatever the arm reports. This is the
	// failure #1479's acceptance criterion names: "a recording-T that records
	// nothing would make every negative self-test pass". It does not, and
	// TestTheVerdictsDependOnWhatTheRecorderSaw is why — but the claim has to
	// be re-run, not believed.
	deafRecorder = brokenRecorder{name: "deaf", did: false, text: ""}

	// criesWolfRecorder reports unconditionally, with text that names no
	// obligation. It is the opposite break and the more dangerous one: it
	// satisfies "something failed" for every mutation ever written, so a
	// harness that only asked that question would grade every obligation as
	// discriminating while none of them did.
	criesWolfRecorder = brokenRecorder{name: "cries-wolf", did: true, text: "something went wrong"}

	// faithfulRecording is what a working recorder produces for the mutation
	// the two above are graded against: the obligation's own message.
	faithfulRecording = brokenRecorder{name: "faithful", did: true, text: harnessProbeMessage}
)

// harnessProbeMessage stands in for an obligation's failure text. It is
// deliberately not any real obligation's message: the harness is being graded
// here, not a family.
const harnessProbeMessage = "effect observed while permission is pending"

// TestTheVerdictsDependOnWhatTheRecorderSaw is the first half of the harness's
// own mutation proof: the decision procedure every negative self-test runs
// through, driven against two committed broken recorders.
//
// Read the deaf row as the answer to #1479's acceptance criterion. A recorder
// that stops recording does not make the self-tests pass — it makes
// verdictReported non-empty, so every one of them goes RED and says the
// obligation observed nothing. That direction is the safe one, and it is
// asserted here rather than reasoned about.
func TestTheVerdictsDependOnWhatTheRecorderSaw(t *testing.T) {
	t.Run("a_faithful_recording_is_accepted", func(t *testing.T) {
		if v := verdictReported(faithfulRecording, harnessProbeMessage); v != "" {
			t.Fatalf("a recorder that saw the obligation's own message was rejected: %s", v)
		}
	})

	t.Run("a_deaf_recorder_turns_a_negative_self_test_red", func(t *testing.T) {
		if verdictReported(deafRecorder, harnessProbeMessage) == "" {
			t.Fatal("a recorder that records NOTHING was accepted as evidence that an " +
				"obligation fired — every negative self-test in this package would then pass " +
				"while observing nothing, which is the failure #1479 exists to remove")
		}
	})

	t.Run("a_cries_wolf_recorder_cannot_fake_an_obligation", func(t *testing.T) {
		if verdictReported(criesWolfRecorder, harnessProbeMessage) == "" {
			t.Fatal("a recorder that reports unconditionally was accepted as evidence that a " +
				"SPECIFIC obligation fired — this is #1453's second instance, where a run " +
				"failed incidentally elsewhere and the obligation was graded on it")
		}
	})

	t.Run("a_cries_wolf_recorder_fails_the_vacuity_guard", func(t *testing.T) {
		if verdictSilent(criesWolfRecorder) == "" {
			t.Fatal("a recorder that reports unconditionally passed the vacuity guard — the " +
				"guard is what stops an assertion that fires on everything from looking like " +
				"coverage")
		}
		if verdictSilent(deafRecorder) != "" {
			t.Fatal("the vacuity guard rejected a silent observation, so no correct fixture " +
				"could ever pass it")
		}
	})

	t.Run("an_unnamed_obligation_is_refused", func(t *testing.T) {
		if verdictReported(faithfulRecording, "") == "" {
			t.Fatal("a self-test that named no expected message was accepted — that is the " +
				"\"any failure will do\" shape #1453's second instance slipped through")
		}
	})
}

// TestTheRecorderObservesWhatAnArmReports is the second half: the real
// recordingT, driven by arms whose behaviour is known, so that neutering any of
// its methods goes red here rather than quietly weakening every family.
func TestTheRecorderObservesWhatAnArmReports(t *testing.T) {
	t.Run("errorf_is_recorded_and_does_not_abort", func(t *testing.T) {
		ran := false
		rec := observe(t, func(at armT) {
			at.Errorf("probe: %s", "one")
			ran = true
		})
		if !rec.reported() {
			t.Fatal("Errorf recorded nothing — the recorder is deaf, and every negative " +
				"self-test in this package is being graded by it")
		}
		if !strings.Contains(rec.report(), "probe: one") {
			t.Fatalf("Errorf lost the message it was handed: %s", rec.report())
		}
		if !ran {
			t.Fatal("Errorf aborted the arm; only Fatal/Fatalf may")
		}
	})

	t.Run("fatalf_is_recorded_and_aborts", func(t *testing.T) {
		past := false
		rec := observe(t, func(at armT) {
			at.Fatalf("probe: %s", "fatal")
			past = true
		})
		if !rec.reported() {
			t.Fatal("Fatalf recorded nothing")
		}
		if past {
			t.Fatal("Fatalf did not abort the arm — an obligation whose fatal guards the " +
				"assertions below it would then be graded on state its own guard rejected")
		}
		if !rec.aborted() {
			t.Fatal("an aborted arm was not reported as aborted")
		}
	})

	t.Run("a_silent_arm_records_nothing", func(t *testing.T) {
		rec := observe(t, func(armT) {})
		if rec.reported() {
			t.Fatalf("a silent arm was reported as failing (%s) — a recorder that reports "+
				"unconditionally satisfies every mutation case in this package", rec.report())
		}
	})

	t.Run("helper_is_still_called_by_arms", func(t *testing.T) {
		rec := observe(t, func(at armT) { at.Helper() })
		if rec.helpers == 0 {
			t.Fatal("Helper was not recorded")
		}
	})
}

// TestNoArmReportsThroughTheFixtureT is the tripwire for the one way this seam
// can be silently un-done: armT carries the real *testing.T so an arm can build
// fixtures with it, and reporting through that field instead of through the
// embedded reporter bypasses the recorder entirely. Such an arm cannot be
// driven by a negative self-test — it would fail the enclosing test — so the
// mutation evidence would go back to being unrunnable without anything saying
// so.
//
// It reads the package's own non-test sources, because a convention that lives
// only in a doc comment is the thing this package exists to replace.
func TestNoArmReportsThroughTheFixtureT(t *testing.T) {
	const field = "fixtures"
	reportingMethods := map[string]bool{
		"Error": true, "Errorf": true, "Fatal": true, "Fatalf": true, "Skip": true, "Skipf": true,
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no packages — this tripwire would pass vacuously")
	}

	seenArmT := false
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "armT" {
					seenArmT = true
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				outer, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !reportingMethods[outer.Sel.Name] {
					return true
				}
				inner, ok := outer.X.(*ast.SelectorExpr)
				if !ok || inner.Sel.Name != field {
					return true
				}
				t.Errorf("%s: reports through .%s.%s — that bypasses the reporter seam, so "+
					"this obligation cannot be driven by a negative self-test; report through "+
					"the embedded reporter and keep .%s for fixture machinery only",
					fset.Position(call.Pos()), field, outer.Sel.Name, field)
				return true
			})
		}
	}
	if !seenArmT {
		t.Fatal("the package no longer mentions armT — this tripwire is scanning for a shape " +
			"that has been renamed, and would pass whatever the sources do")
	}
}
