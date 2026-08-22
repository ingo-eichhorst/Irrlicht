package main

import (
	"encoding/json"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/hookcov"
)

// TestCoverageHooksCounts pins the per-adapter hook numbers against richRepo.
// As with --summary the counts are the product, so they are literal here.
//
// The `declares` column is NOT fixture data — it is joined at runtime from the
// daemon's adapter registry, where claudecode, codex and antigravity declare a
// "hooks" permission and aider does not. That join is the part of this command
// that can rot silently (a renamed permission key, a changed adapter slug), so
// it is asserted rather than assumed.
func TestCoverageHooksCounts(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("coverage", "--hooks", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var rep hookcov.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}

	want := map[string]hookcov.AdapterCoverage{
		"aider":       {Adapter: "aider", DeclaresHooks: false, Cells: 1, Recordings: 1, WithHooks: 0, Status: hookcov.StatusNone},
		"claudecode":  {Adapter: "claudecode", DeclaresHooks: true, Cells: 7, Recordings: 2, WithHooks: 1, Status: hookcov.StatusOK},
		"codex":       {Adapter: "codex", DeclaresHooks: true, Cells: 2, Recordings: 1, WithHooks: 0, Status: hookcov.StatusGap},
		"antigravity": {Adapter: "antigravity", DeclaresHooks: true, Cells: 1, Recordings: 1, WithHooks: 0, Status: hookcov.StatusGap},
	}
	got := map[string]hookcov.AdapterCoverage{}
	for _, a := range rep.Adapters {
		got[a.Adapter] = a
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("missing adapter row %q", name)
			continue
		}
		if g != w {
			t.Errorf("%s: got %+v want %+v", name, g, w)
		}
	}
	if rep.Totals.Cells != 11 || rep.Totals.Recordings != 5 || rep.Totals.WithHooks != 1 {
		t.Errorf("totals: got %+v want cells=11 recordings=5 with-hooks=1", rep.Totals)
	}
}

// TestCoverageHooksDistinguishesGapFromNoHooks is the reason this command
// exists. "Declares hooks and has zero hook-bearing recordings" (codex) is the
// loud failure; "declares no hooks" (aider) is fine. Both have WithHooks==0,
// so a report keying only on that number would conflate them.
//
// aider took the second role from antigravity in #1723 and is where it stops:
// it is the last registry adapter declaring no hooks. See richRepo's comment
// for what that costs.
func TestCoverageHooksDistinguishesGapFromNoHooks(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("coverage", "--hooks", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var rep hookcov.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	byName := map[string]hookcov.AdapterCoverage{}
	for _, a := range rep.Adapters {
		byName[a.Adapter] = a
	}
	if byName["codex"].WithHooks != 0 || byName["aider"].WithHooks != 0 {
		t.Fatalf("fixture precondition: both codex and aider should have zero hook-bearing recordings")
	}
	if byName["codex"].Status == byName["aider"].Status {
		t.Errorf("gap and no-hooks-declared must not share a status; both are %q", byName["codex"].Status)
	}
	if byName["aider"].Status != hookcov.StatusNone {
		t.Errorf("aider declares no hooks and has none — want %q, got %q", hookcov.StatusNone, byName["aider"].Status)
	}
	if byName["codex"].Status != hookcov.StatusGap {
		t.Errorf("codex declares hooks with zero hook recordings — want %q, got %q", hookcov.StatusGap, byName["codex"].Status)
	}
	// antigravity is a gap too since #1723 — it declares hooks and the fixture
	// gives it a hook-free recording — so the assertion is that codex is
	// LISTED, not that it is alone.
	gaps := map[string]bool{}
	for _, g := range rep.Gaps() {
		gaps[g] = true
	}
	if !gaps["codex"] {
		t.Errorf("codex should be listed as a gap, got %v", rep.Gaps())
	}
	if gaps["aider"] {
		t.Errorf("aider declares no hooks and must never be listed as a gap, got %v", rep.Gaps())
	}
}

// TestCoverageHooksText checks the loud rendering: the GAP must be legible at
// a terminal, not only in the JSON.
func TestCoverageHooksText(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("coverage", "--hooks", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	if !strings.Contains(out, "GAP") {
		t.Errorf("text output must shout about the gap:\n%s", out)
	}
	if !strings.Contains(out, "codex") {
		t.Errorf("text output must name the gapped adapter:\n%s", out)
	}
	// The scope caveat is part of the output, not a code comment: the count is
	// over catalog cells, and a reader grepping replaydata/ would otherwise get
	// a different number from the non-catalog regressions/ tree.
	if !strings.Contains(out, "regressions") {
		t.Errorf("text output must state which tree is out of scope:\n%s", out)
	}
}

// TestCoveragePlainStillRollup locks the pre-existing behavior: `of coverage`
// with no flag keeps emitting the derived rollup JSON. This one passes on main
// by construction — it is a lock, not a red-first defect test.
func TestCoveragePlainStillRollup(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("coverage", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("plain coverage should still be rollup JSON: %v\n%s", err, out)
	}
	if _, ok := doc["agents"]; !ok {
		t.Errorf("rollup shape lost its agents key: %v", doc)
	}
}
