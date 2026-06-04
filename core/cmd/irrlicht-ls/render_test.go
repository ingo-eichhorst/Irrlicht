package main

import (
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

func fixture(id, state, project string) *session.SessionState {
	return &session.SessionState{
		SessionID:   id,
		State:       state,
		ProjectName: project,
		UpdatedAt:   time.Now().Unix(),
	}
}

func render(t *testing.T, sessions []*session.SessionState, useColor bool) string {
	t.Helper()
	var sb strings.Builder
	renderGroups(&sb, session.BuildDashboard(sessions, nil), useColor)
	return sb.String()
}

func TestPressureColor(t *testing.T) {
	cases := map[string]string{
		"critical": ansiRed,
		"warning":  ansiBrightRed,
		"high":     ansiBrightRed,
		"caution":  ansiYellow,
		"medium":   ansiYellow,
		"safe":     ansiGreen,
		"unknown":  ansiGreen,
		"":         ansiGreen,
	}
	for level, want := range cases {
		if got := pressureColor(level); got != want {
			t.Errorf("pressureColor(%q) = %q, want %q", level, got, want)
		}
	}
}

func TestColorizeDisabled(t *testing.T) {
	if got := colorize("42%", "critical", false); got != "42%" {
		t.Errorf("colorize with useColor=false = %q, want plain string", got)
	}
}

func TestFormatCostUSD(t *testing.T) {
	cases := []struct {
		usd  float64
		want string
	}{
		{0, ""},
		{-1, ""},
		{1.5, "$1.50"},
		{0.004, "$0.00"},
		{12.345, "$12.35"},
	}
	for _, c := range cases {
		if got := formatCostUSD(c.usd); got != c.want {
			t.Errorf("formatCostUSD(%v) = %q, want %q", c.usd, got, c.want)
		}
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("ab"); got != "ab" {
		t.Errorf("shortID short input = %q, want %q", got, "ab")
	}
	if got := shortID("0123456789"); got != "01234567" {
		t.Errorf("shortID long input = %q, want %q", got, "01234567")
	}
}

func TestFormatRowNilMetrics(t *testing.T) {
	a := &session.Agent{SessionState: fixture("abcd1234-rest", session.StateReady, "proj")}
	row := formatRow(a, false)
	for _, want := range []string{"ready", "proj", "abcd1234", "claude-code", "ago"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q does not contain %q", row, want)
		}
	}
	for _, banned := range []string{"$", "%"} {
		if strings.Contains(row, banned) {
			t.Errorf("row %q unexpectedly contains %q", row, banned)
		}
	}
}

func TestFormatRowFullMetrics(t *testing.T) {
	s := fixture("abcd1234-rest", session.StateWorking, "proj")
	s.Adapter = "codex"
	s.Metrics = &session.SessionMetrics{
		ModelName:          "gpt-5",
		ContextWindow:      400000,
		ContextUtilization: 47.4,
		PressureLevel:      "caution",
		EstimatedCostUSD:   3.21,
	}
	row := formatRow(&session.Agent{SessionState: s}, true)
	for _, want := range []string{"gpt-5", ansiYellow + "47%" + ansiReset, "400k", "$3.21", "codex"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q does not contain %q", row, want)
		}
	}

	plain := formatRow(&session.Agent{SessionState: s}, false)
	if strings.Contains(plain, "\033[") {
		t.Errorf("row %q contains ANSI escapes with useColor=false", plain)
	}
}

func TestRenderGroupsHierarchy(t *testing.T) {
	parent := fixture("parent-12345678", session.StateWorking, "proj")
	childA := fixture("child-a-12345678", session.StateWorking, "proj")
	childA.ParentSessionID = parent.SessionID
	childB := fixture("child-b-12345678", session.StateReady, "proj")
	childB.ParentSessionID = parent.SessionID
	standalone := fixture("solo-12345678", session.StateReady, "proj")

	out := render(t, []*session.SessionState{parent, childA, childB, standalone}, false)

	// Single group: no header.
	if strings.Contains(out, "(4 sessions)") {
		t.Errorf("single group should not render a header:\n%s", out)
	}
	// Parent carries the badge.
	if !strings.Contains(out, "[2 agents: 1w/1r]") {
		t.Errorf("output missing subagent badge:\n%s", out)
	}
	// Children are indented.
	for _, child := range []string{"child-a-", "child-b-"} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, child) && strings.HasPrefix(line, "  ") {
				found = true
			}
		}
		if !found {
			t.Errorf("child %q not rendered indented:\n%s", child, out)
		}
	}
	// Standalone is not indented.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "solo-123") && strings.HasPrefix(line, " ") {
			t.Errorf("standalone session unexpectedly indented:\n%s", out)
		}
	}
}

func TestRenderGroupsHeaders(t *testing.T) {
	parent := fixture("parent-12345678", session.StateWorking, "alpha")
	child := fixture("child-12345678", session.StateWorking, "alpha")
	child.ParentSessionID = parent.SessionID
	other := fixture("other-12345678", session.StateReady, "beta")

	out := render(t, []*session.SessionState{parent, child, other}, false)

	if !strings.Contains(out, "alpha (2 sessions)") {
		t.Errorf("missing alpha header with child counted:\n%s", out)
	}
	if !strings.Contains(out, "beta (1 session)") {
		t.Errorf("missing singular beta header:\n%s", out)
	}
}

func TestRenderGroupsWaitingQuestion(t *testing.T) {
	waiting := fixture("wait-12345678", session.StateWaiting, "proj")
	waiting.Metrics = &session.SessionMetrics{LastAssistantText: "Should I delete\nthe legacy migration?"}
	working := fixture("work-12345678", session.StateWorking, "proj")
	working.Metrics = &session.SessionMetrics{LastAssistantText: "On it."}

	out := render(t, []*session.SessionState{waiting, working}, false)

	if !strings.Contains(out, "? Should I delete the legacy migration?") {
		t.Errorf("missing waiting question line (newlines collapsed):\n%s", out)
	}
	if strings.Contains(out, "On it.") {
		t.Errorf("non-waiting session should not render its last text:\n%s", out)
	}
}

func TestRenderGroupsTaskProgress(t *testing.T) {
	s := fixture("task-12345678", session.StateWorking, "proj")
	s.Metrics = &session.SessionMetrics{Tasks: []session.Task{
		{ID: "1", Subject: "a", Status: "completed"},
		{ID: "2", Subject: "b", Status: "in_progress"},
		{ID: "3", Subject: "c", Status: "pending"},
	}}

	out := render(t, []*session.SessionState{s}, false)
	if !strings.Contains(out, "1/3 completed") {
		t.Errorf("missing task progress line:\n%s", out)
	}
}

func TestFilterSessions(t *testing.T) {
	a := fixture("AAAA1111-x", session.StateWorking, "irrlicht")
	a.Adapter = "" // legacy claude-code
	b := fixture("bbbb2222-x", session.StateWaiting, "api-server")
	b.Adapter = "codex"
	child := fixture("cccc3333-x", session.StateWorking, "irrlicht")
	child.ParentSessionID = a.SessionID
	all := []*session.SessionState{a, b, child}

	cases := []struct {
		name string
		f    filterSpec
		want []string
	}{
		{"none", filterSpec{}, []string{"AAAA1111-x", "bbbb2222-x", "cccc3333-x"}},
		{"id case-insensitive prefix", filterSpec{idPrefix: "aaaa"}, []string{"AAAA1111-x"}},
		{"state", filterSpec{state: session.StateWaiting}, []string{"bbbb2222-x"}},
		{"project substring", filterSpec{project: "irr"}, []string{"AAAA1111-x", "cccc3333-x"}},
		{"adapter exact", filterSpec{adapter: "codex"}, []string{"bbbb2222-x"}},
		{"adapter claude-code matches empty", filterSpec{adapter: "claude-code"}, []string{"AAAA1111-x", "cccc3333-x"}},
	}
	for _, c := range cases {
		got := filterSessions(all, c.f)
		var ids []string
		for _, s := range got {
			ids = append(ids, s.SessionID)
		}
		if strings.Join(ids, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, ids, c.want)
		}
	}
}

// A child whose parent is filtered away is promoted to a top-level row.
func TestFilteredChildPromoted(t *testing.T) {
	parent := fixture("parent-12345678", session.StateReady, "proj")
	child := fixture("child-12345678", session.StateWorking, "proj")
	child.ParentSessionID = parent.SessionID

	filtered := filterSessions([]*session.SessionState{parent, child}, filterSpec{state: session.StateWorking})
	out := render(t, filtered, false)

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "child-12") && strings.HasPrefix(line, " ") {
			t.Errorf("orphaned child should render at top level:\n%s", out)
		}
	}
	if !strings.Contains(out, "child-12") {
		t.Errorf("filtered child missing from output:\n%s", out)
	}
}
