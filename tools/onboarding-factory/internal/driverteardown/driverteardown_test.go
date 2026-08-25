package driverteardown

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("resolving the repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// loadFixture reads one testdata driver plus the one shell library it sources.
func loadFixture(t *testing.T, name, lib string) (File, []File) {
	t.Helper()
	path := filepath.Join("testdata", name)
	src, err := os.ReadFile(path) // #nosec G304 -- a literal testdata path
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	var libs []File
	if lib != "" {
		lp := filepath.Join("testdata", "lib", lib)
		lsrc, err := os.ReadFile(lp) // #nosec G304 -- a literal testdata path
		if err != nil {
			t.Fatalf("reading fixture lib %s: %v", lib, err)
		}
		libs = append(libs, File{Path: lp, Src: string(lsrc)})
	}
	return File{Path: path, Src: string(src)}, libs
}

// TestCheckerGradesEveryFixture is the committed mutation evidence this
// package owes under AGENTS.md.
//
// A tripwire is a check the change ADDS, so it has no "before the fix" to run
// red — the rule is to mutate what it protects and confirm the check goes red,
// and to commit that mutation as a fixture rather than describing it in a PR
// body nothing re-runs. Every row below is one such mutation of
// testdata/good_driver.sh, differing from it in as close to a single line as
// the invariant allows, so a row that stops firing points at the checker
// rather than at the fixture.
//
// The want-clean rows carry as much weight as the want-red ones.
// inv1_step_guard_ok.sh is the one this package would be worst without: it is
// kiro-cli's legitimate step-level SES_ALIVE guard, and a checker that failed
// it would be a checker nobody keeps.
//
// HOW A TEARDOWN SITE IS TOLD APART FROM A STEP-LEVEL GUARD — and what that
// choice costs — is written out on checkTeardownUngated. In one line: the
// distinction is STRUCTURAL (the EXIT-trap handler and the top-level end-of-run
// sweep are teardown; anything inside an ordinary function is not), because
// both spellings of the gate are byte-identical, and the cost is that a driver
// which moved its end-of-run sweep into a helper function would have that
// sweep read as step-level. The flag NAME the gate is matched on
// (`aliveGate`) carries its own tradeoff note.
func TestCheckerGradesEveryFixture(t *testing.T) {
	cases := []struct {
		fixture string
		lib     string
		want    []string // one invariant name per expected finding, in order
	}{
		{fixture: "good_driver.sh", lib: "slots.sh"},
		{fixture: "inv1_step_guard_ok.sh", lib: "slots.sh"},
		{fixture: "inv3_alloc_arg2_good.sh", lib: "slots_pos2.sh"},
		{fixture: "no_tmux_exempt.sh"},
		{fixture: "inv4_renamed_sentinel_ok.sh", lib: "slots.sh"},
		{fixture: "inv4_fail_closed_ok.sh", lib: "slots.sh"},
		{fixture: "inv4_literal_verdict_ok.sh", lib: "slots.sh"},

		{fixture: "inv1_gated_trap.sh", lib: "slots.sh", want: []string{"INV-1"}},
		{fixture: "inv1_gated_final_sweep.sh", lib: "slots.sh", want: []string{"INV-1"}},
		{fixture: "inv2_no_trap.sh", want: []string{"INV-2"}},
		{fixture: "inv2_trap_without_teardown.sh", want: []string{"INV-2"}},
		{fixture: "inv3_no_pid.sh", lib: "slots.sh", want: []string{"INV-3"}},
		{fixture: "inv3_glued_pid.sh", lib: "slots.sh", want: []string{"INV-3"}},
		{fixture: "inv3_alloc_arg2_bad.sh", lib: "slots_pos2.sh", want: []string{"INV-3"}},
		{fixture: "inv4_unconditional_write.sh", lib: "slots.sh", want: []string{"INV-4"}},
		{fixture: "inv4_guard_consults_only_itself.sh", lib: "slots.sh", want: []string{"INV-4"}},
		{fixture: "inv4_sentinel_never_set.sh", lib: "slots.sh", want: []string{"INV-4"}},
		{fixture: "inv4_guard_reassigns_initial.sh", lib: "slots.sh", want: []string{"INV-4"}},
		{fixture: "inv4_case_arm_is_not_the_epilogue.sh", lib: "slots.sh", want: []string{"INV-4"}},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			driver, libs := loadFixture(t, tc.fixture, tc.lib)
			got, err := CheckDriver(driver, libs)
			if err != nil {
				t.Fatalf("checking %s: %v", tc.fixture, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("%s: got %d finding(s), want %d\n%s",
					tc.fixture, len(got), len(tc.want), joinFindings(got))
			}
			for i, f := range got {
				if f.Invariant != tc.want[i] {
					t.Errorf("%s: finding %d is %s, want %s\n%s",
						tc.fixture, i, f.Invariant, tc.want[i], f)
				}
			}
		})
	}
}

// TestCheckerRefusesRatherThanReportingClean pins the failures that make every
// clean verdict above mean something. AGENTS.md: absence of a finding and
// inability to look must never produce the same output.
func TestCheckerRefusesRatherThanReportingClean(t *testing.T) {
	cases := []struct {
		fixture string
		lib     string
		wantErr string // a fragment of the checker's own message
	}{
		{fixture: "vacuous_empty.sh", wantErr: "is empty"},
		{fixture: "vacuous_no_teardown.sh", wantErr: "INV-1 graded nothing here"},
		{fixture: "vacuous_unnamed_session.sh", wantErr: "with no `-s <name>`"},
		{fixture: "vacuous_unclosed_function.sh", wantErr: "is never closed by a `}`"},
		{fixture: "vacuous_uninitialised_verdict.sh", lib: "slots.sh",
			wantErr: "INV-4 has no initial value to compare against"},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			driver, libs := loadFixture(t, tc.fixture, tc.lib)
			got, err := CheckDriver(driver, libs)
			if err == nil {
				t.Fatalf("%s returned no error (%d finding(s)) — this input could not be graded, "+
					"and a check that cannot run must say so\n%s", tc.fixture, len(got), joinFindings(got))
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: refusal %q does not contain %q", tc.fixture, err, tc.wantErr)
			}
		})
	}
}

// TestExemptionIsDerivedNotListed grades the derived exemption on both halves
// at once: no tmux launch means no findings, AND the file was still read.
// Without the second half, a driver that failed to load would be indexed as
// "exempt" — the shape #1423, #1611 and #1684 all took, where a selection rule
// that silently drops a whole family reads exactly like a gate that passed.
func TestExemptionIsDerivedNotListed(t *testing.T) {
	driver, _ := loadFixture(t, "no_tmux_exempt.sh", "")
	if strings.TrimSpace(driver.Src) == "" {
		t.Fatal("the exempt fixture is empty — it proves nothing about exemption")
	}
	if n := LaunchCount(driver.Src); n != 0 {
		t.Fatalf("the exempt fixture launches %d tmux session(s) — it is not exempt", n)
	}
	got, err := CheckDriver(driver, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("a driver that launches no tmux session must be exempt: err=%v findings=%s",
			err, joinFindings(got))
	}
}

// TestCarriesPIDFieldNamesEverySpelling is the corpus for INV-3's predicate,
// and the want:false rows are where its value is.
//
// "The name carries the driver's PID" is the claim whose FALSE POSITIVE is
// expensive: run-cell.sh's post-run assertion splits a live session name on
// `-` and compares fields against the PID it captured, so a glued `$$` is not
// merely untidy — it never matches, and a name ending in a longer number whose
// tail happens to equal the PID would match a DIFFERENT run's session. A
// predicate that accepted a bare substring would report every driver compliant
// and leave the assertion silently matching nothing.
func TestCarriesPIDFieldNamesEverySpelling(t *testing.T) {
	cases := []struct {
		name    string
		literal string
		want    bool
	}{
		// --- the spellings the eleven shipped drivers actually use ---
		{"trailing field", "aider-onboard-${UUID:0:8}-$$", true},
		{"middle field, before a date", "geminidrv-$$-$(date +%s)-r${ACTIVE}", true},
		{"middle field, after a date", "codex-onboard-$(date +%s)-$$-r${ACTIVE}", true},
		{"followed by an arithmetic expansion", "copilotdrv-$$-$(date +%s)-$((N_SLOTS + 1))", true},
		{"adapter slug that itself contains a dash", "kiro-clidrv-$$-$(date +%s)-${idx}", true},

		// --- want:false ---
		{"no pid at all", "fixture-onboard-$(date +%s)", false},
		{"glued to the prefix", "fixture-onboard$$-$(date +%s)", false},
		{"glued to the suffix", "fixture-onboard-$$$(date +%s)", false},
		{"underscore is not the delimiter", "fixture_onboard_$$_$(date +%s)", false},
		{"a single $ is the shell's, not the pid", "fixture-onboard-$-$(date +%s)", false},
		{"buried inside an expansion", "fixture-onboard-${FOO:-$$}", false},
		{"the empty name", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := carriesPIDField(tc.literal); got != tc.want {
				t.Errorf("carriesPIDField(%q) = %v, want %v", tc.literal, got, tc.want)
			}
		})
	}
}

// TestVerdictFilenameIsTheOneRunCellReads anchors INV-4's derived precondition.
//
// The whole invariant binds on one literal: a handler that redirects into a
// path ending in `driver.exit-reason`. That is a filename rather than a
// variable name, and the reason matching it is defensible is that it is a
// CONTRACT between the driver and run-cell.sh — a rename would have to touch
// both. This test is what makes that argument checkable instead of asserted:
// if run-cell.sh stops reading that name, INV-4 has silently stopped binding on
// every driver in the fleet, and a whole invariant switching itself off must be
// a loud failure rather than eleven quiet passes.
func TestVerdictFilenameIsTheOneRunCellReads(t *testing.T) {
	path := filepath.Join(repoRoot(t), "tools", "onboarding-factory", "scripts", "run-cell.sh")
	src, err := os.ReadFile(path) // #nosec G304 -- built from the repo root
	if err != nil {
		t.Fatalf("reading run-cell.sh: %v — INV-4's anchor cannot be checked, and a check that "+
			"cannot run must say so", err)
	}
	if !strings.Contains(string(src), exitReasonFile) {
		t.Fatalf("run-cell.sh no longer mentions %q. INV-4 binds only on a handler that "+
			"redirects into a path ending in that name, so it is now binding on nothing — "+
			"update exitReasonFile to whatever the staging contract calls the verdict file.",
			exitReasonFile)
	}
	// The reader is what makes the initialiser dangerous: it turns a MISSING
	// file into `unknown`, so a handler that writes the initialiser converts a
	// non-verdict into a verdict. Pin that the fallback still exists.
	if !strings.Contains(string(src), `|| echo "unknown"`) {
		t.Errorf("run-cell.sh no longer falls back to \"unknown\" when %s is absent. INV-4's "+
			"rationale rests on that fallback — re-read run-cell.sh and restate the invariant "+
			"before assuming it still holds.", exitReasonFile)
	}
}

// TestAdaptersRefusesAnEmptyCatalog pins the refusal the live arm below rests
// on: a tree with no driver in it would satisfy that arm perfectly by having
// nothing to grade.
func TestAdaptersRefusesAnEmptyCatalog(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, AgentsDir, "somewhere"), 0o750); err != nil {
		t.Fatalf("building the empty catalog: %v", err)
	}
	if _, err := Adapters(dir); err == nil {
		t.Fatal("a catalog with no driver in it returned a clean empty list; " +
			"'no adapter ships a driver' and 'the scan broke' must not be the same answer")
	}
}

// TestEveryDriverTearsDownEverySession is the live arm: every adapter that
// ships a recording driver, read from disk so an adapter onboarded tomorrow is
// graded by existing rather than by whoever adds it remembering this package.
//
// It is a STATIC check on purpose. The recording rig needs tmux and a live
// agent CLI, and `tmux` appears nowhere under .github/ — the rig has never run
// in CI and is not going to. This arm reads source text, so it runs everywhere
// `go test` does, which is the only place a teardown regression can be caught
// before it costs a recording run.
//
// Every per-adapter vacuity guard lives inside CheckDriver, which returns an
// ERROR rather than an empty list when it found nothing to grade: an empty
// driver, a `tmux new-session` it could not parse or that carries no `-s`, a
// tmux-launching driver with no teardown site, one whose session names could
// not be traced back to the text that mints them. The guard here is the
// fleet-level one — that at least one adapter launched tmux at all.
func TestEveryDriverTearsDownEverySession(t *testing.T) {
	root := repoRoot(t)
	adapters, err := Adapters(root)
	if err != nil {
		t.Fatalf("scanning the adapter catalog: %v — a check that cannot run must say so", err)
	}

	graded := 0
	for _, adapter := range adapters {
		t.Run(adapter, func(t *testing.T) {
			driver, libs, err := LoadDriver(root, adapter)
			if err != nil {
				t.Fatalf("%v — an adapter whose driver cannot be read is not exempt, and a "+
					"check that cannot run must say so", err)
			}
			findings, err := CheckDriver(driver, libs)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if LaunchCount(driver.Src) == 0 {
				t.Logf("%s launches no tmux session — exempt by derivation from its source, "+
					"not by a list", adapter)
				return
			}
			graded++
			for _, f := range findings {
				t.Errorf("%s", f)
			}
		})
	}
	if graded == 0 {
		t.Errorf("none of the %d adapter driver(s) launches tmux at all — this arm graded nothing",
			len(adapters))
	}
}

func joinFindings(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("  " + f.String() + "\n")
	}
	return b.String()
}
