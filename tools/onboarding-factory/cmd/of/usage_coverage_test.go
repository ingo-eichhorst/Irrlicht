package main

import (
	"fmt"
	"strings"
	"testing"

	coreagents "irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/shard"
)

// usageCoverageFindings checks each adapter's usage declaration
// (agent.Agent.Usage, #2061) against the onboarding matrix's coarse
// token_usage capability in replaydata/agents/adapters.json:
//
//   - an adapter whose column says token_usage is "absent" — the agent has
//     no token reporting at all — must declare no token type, since a
//     declared type tells consumers its zeros are counted zeros;
//   - every declared type is a member of session.TokenTypes;
//   - every column resolves to a registered adapter, so a rename cannot make
//     a column silently stop being checked.
//
// "untraced" is deliberately NOT treated like "absent": aider is untraced
// because the token-accounting scenario needs cache figures aider writes only
// to its terminal, yet its parser genuinely reads input and output.
//
// It returns the findings and how many columns it checked.
func usageCoverageFindings(model *matrix.CapabilityModel, all []agent.Agent) (findings []string, checked int) {
	byName := adaptersByName(all)
	for _, column := range model.AdapterNames() {
		name := shard.DaemonAdapterForSlug(column)
		a, ok := byName[name]
		if !ok {
			findings = append(findings, fmt.Sprintf("column %q maps to adapter %q, which is not registered", column, name))
			continue
		}
		checked++
		findings = append(findings, declarationFindings(model, column, a)...)
	}
	return findings, checked
}

func adaptersByName(all []agent.Agent) map[string]agent.Agent {
	byName := make(map[string]agent.Agent, len(all))
	for _, a := range all {
		byName[a.Identity.Name] = a
	}
	return byName
}

// declarationFindings checks one registered adapter's usage declaration
// against its column; an adapter that declares nothing has nothing to check.
func declarationFindings(model *matrix.CapabilityModel, column string, a agent.Agent) []string {
	if a.Usage == nil {
		return nil
	}
	name := a.Identity.Name
	var findings []string
	for _, tt := range a.Usage.TokenTypes {
		if !session.IsTokenType(tt) {
			findings = append(findings, fmt.Sprintf("adapter %q declares unknown token type %q", name, tt))
		}
	}
	if model.CapabilityState(column, "token_usage") == matrix.CapabilityAbsent && len(a.Usage.TokenTypes) > 0 {
		findings = append(findings, fmt.Sprintf(
			"adapter %q declares token types %v, but column %q says token_usage is absent",
			name, a.Usage.TokenTypes, column))
	}
	return findings
}

// TestUsageCoverage_AgreesWithTheCapabilityModel runs the check against the
// shipped adapters.json and the registered adapters.
//
// No column says token_usage is "absent" today (aider, deepseek-harness and
// kiro-cli are "untraced"), so on the shipped file the absent rule has no
// subject. The synthetic test below proves the rule itself; this one asserts
// it read the real file and resolved every column.
func TestUsageCoverage_AgreesWithTheCapabilityModel(t *testing.T) {
	model, err := matrix.LoadCapabilities(repoRootFromTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if !model.Loaded() {
		t.Fatal("replaydata/agents/adapters.json was not found; the check would have nothing to check")
	}
	findings, checked := usageCoverageFindings(model, coreagents.All())
	for _, f := range findings {
		t.Error(f)
	}
	if checked != len(model.AdapterNames()) || checked < 10 {
		t.Errorf("checked %d of %d columns; every column must resolve to a registered adapter",
			checked, len(model.AdapterNames()))
	}
	if countDeclaringUsage(coreagents.All()) == 0 {
		t.Error("no registered adapter declares usage coverage; the check compared nothing")
	}
}

// countDeclaringUsage counts the adapters that declare usage coverage.
func countDeclaringUsage(all []agent.Agent) int {
	declared := 0
	for _, a := range all {
		if a.Usage != nil {
			declared++
		}
	}
	return declared
}

// TestUsageCoverage_AbsentColumnMustDeclareNothing proves the rule on a
// synthetic model, since the shipped file gives it no subject.
func TestUsageCoverage_AbsentColumnMustDeclareNothing(t *testing.T) {
	model := &matrix.CapabilityModel{Adapters: map[string]matrix.AdapterModel{
		"claudecode": {Capabilities: map[string]string{"token_usage": matrix.CapabilityAbsent}},
		"pi":         {Capabilities: map[string]string{"token_usage": matrix.CapabilityUntraced}},
		"ghost":      {},
	}}
	all := []agent.Agent{
		{Identity: agent.Identity{Name: "claude-code"}, Usage: &session.UsageCoverage{TokenTypes: []session.TokenType{session.TokenInput}}},
		{Identity: agent.Identity{Name: "pi"}, Usage: &session.UsageCoverage{TokenTypes: []session.TokenType{"reasoning"}}},
	}
	findings, checked := usageCoverageFindings(model, all)
	want := []string{
		`adapter "claude-code" declares token types [input], but column "claudecode" says token_usage is absent`,
		`column "ghost" maps to adapter "ghost", which is not registered`,
		`adapter "pi" declares unknown token type "reasoning"`,
	}
	checkFindingsEqual(t, findings, want)
	if checked != 2 {
		t.Errorf("checked = %d, want 2", checked)
	}

	// The same absent column with no declaration is fine.
	all[0].Usage = nil
	findings, _ = usageCoverageFindings(model, all)
	for _, f := range findingsNaming(findings, "claude-code") {
		t.Errorf("an absent column with no declaration must pass, got %q", f)
	}
	if len(findings) != 2 {
		t.Errorf("findings = %q, want only the ghost column and pi's unknown type", findings)
	}
}

// checkFindingsEqual requires findings to be want, in order.
func checkFindingsEqual(t *testing.T, findings, want []string) {
	if len(findings) != len(want) {
		t.Fatalf("findings = %q, want %q", findings, want)
	}
	for i := range want {
		if findings[i] != want[i] {
			t.Errorf("finding %d = %q, want %q", i, findings[i], want[i])
		}
	}
}

// findingsNaming returns the findings that mention s.
func findingsNaming(findings []string, s string) []string {
	var out []string
	for _, f := range findings {
		if strings.Contains(f, s) {
			out = append(out, f)
		}
	}
	return out
}
