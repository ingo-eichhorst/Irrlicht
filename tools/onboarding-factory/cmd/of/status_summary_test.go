package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStatusSummaryCounts pins the per-agent bucket counts against richRepo's
// documented cell layout. The counts ARE the product of `--summary`, so they
// are written out literally here rather than recomputed from the matrix — a
// test that re-derives them would pass against any derivation, correct or not.
func TestStatusSummaryCounts(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("status", "--summary", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var v summaryView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}

	want := map[string]agentSummary{
		"aider":      {Agent: "aider", Recorded: 1, Total: 1},
		"claudecode": {Agent: "claudecode", Recorded: 1, Pending: 1, Blocked: 2, Unobservable: 1, NotApplicable: 1, Unknown: 1, Total: 7},
		"codex":      {Agent: "codex", Recorded: 1, Pending: 1, Total: 2},
		"opencode":   {Agent: "opencode", Recorded: 1, Total: 1},
	}
	if len(v.Agents) != len(want) {
		t.Fatalf("want %d agent rows, got %d: %+v", len(want), len(v.Agents), v.Agents)
	}
	for _, got := range v.Agents {
		w, ok := want[got.Agent]
		if !ok {
			t.Fatalf("unexpected agent row %q", got.Agent)
		}
		if got != w {
			t.Errorf("%s: got %+v want %+v", got.Agent, got, w)
		}
	}

	wantTotal := agentSummary{Agent: "total", Recorded: 4, Pending: 2, Blocked: 2, Unobservable: 1, NotApplicable: 1, Unknown: 1, Total: 11}
	if v.Total != wantTotal {
		t.Errorf("total: got %+v want %+v", v.Total, wantTotal)
	}
}

// TestStatusSummaryIsTotalPreserving is the anti-lossy lock. The onboarding
// skill warns that "lossy summaries drop cells": a bucket set that does not
// cover every value matrix.DeriveDisplayState can return would silently
// under-report progress, which is the exact failure a summary must not have.
// Asserted per agent AND on the total row, so a state dropped from only one
// of the two aggregations still fails.
func TestStatusSummaryIsTotalPreserving(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("status", "--summary", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var v summaryView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	for _, a := range append(append([]agentSummary{}, v.Agents...), v.Total) {
		if sum := a.buckets(); sum != a.Total {
			t.Errorf("%s: buckets sum to %d but total is %d — a display state is unbucketed", a.Agent, sum, a.Total)
		}
	}
}

// TestStatusSummaryText checks the human-readable rendering, which is the form
// the skill and a human at a terminal actually read.
func TestStatusSummaryText(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("status", "--summary", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	for _, want := range []string{"recorded", "blocked", "unobservable", "claudecode", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary text missing %q:\n%s", want, out)
		}
	}
	// The claudecode row must carry its seven cells.
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "claudecode") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("no claudecode row:\n%s", out)
	}
	if fields := strings.Fields(row); fields[len(fields)-1] != "7" {
		t.Errorf("claudecode row should end in its 7-cell total, got %q", row)
	}
}

// TestStatusSummaryAgentFilter confirms --summary composes with --agent rather
// than silently ignoring it.
func TestStatusSummaryAgentFilter(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("status", "--summary", "--agent", "codex", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var v summaryView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if len(v.Agents) != 1 || v.Agents[0].Agent != "codex" {
		t.Fatalf("agent filter not applied: %+v", v.Agents)
	}
	if v.Total.Total != 2 {
		t.Errorf("filtered total should count only codex's 2 cells, got %d", v.Total.Total)
	}
}
