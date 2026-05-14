package services

import (
	"encoding/json"
	"testing"

	"irrlicht/core/domain/session"
)

func mkClsRule(kind, state string, params any) ClassifierRule {
	b, _ := json.Marshal(params)
	return ClassifierRule{Kind: kind, TargetState: state, Params: b, RuleID: kind}
}

func TestClassifyStateWithRules_noRulesFallsBackToLegacy(t *testing.T) {
	m := &session.SessionMetrics{HasOpenToolCall: false}
	// Legacy default: any non-working state with non-nil metrics becomes working.
	st, _, ev := ClassifyStateWithRules(session.StateReady, m, nil)
	if st != session.StateWorking {
		t.Errorf("legacy fallback should produce working, got %s", st)
	}
	if ev.RuleID != "" {
		t.Errorf("evidence should be empty on legacy path, got %+v", ev)
	}
}

func TestClassifyStateWithRules_ruleFiresAndOverridesLegacy(t *testing.T) {
	rules := []ClassifierRule{
		mkClsRule("interrupt_marker", session.StateReady, map[string]string{}),
	}
	m := &session.SessionMetrics{LastWasUserInterrupt: true}
	st, reason, ev := ClassifyStateWithRules(session.StateWorking, m, rules)
	if st != session.StateReady {
		t.Errorf("rule should win → ready, got %s", st)
	}
	if ev.RuleID == "" {
		t.Error("evidence should be populated when a rule fires")
	}
	if reason == "" {
		t.Error("reason should describe the rule")
	}
}

func TestClassifyStateWithRules_ruleDoesNotFireUsesLegacy(t *testing.T) {
	rules := []ClassifierRule{
		mkClsRule("interrupt_marker", session.StateReady, nil),
	}
	m := &session.SessionMetrics{HasOpenToolCall: false} // no interrupt
	st, _, _ := ClassifyStateWithRules(session.StateReady, m, rules)
	// Legacy: non-working metrics with default fallback → working.
	if st != session.StateWorking {
		t.Errorf("expected legacy fallback to working, got %s", st)
	}
}

func TestEvaluateRules_priorityOrder(t *testing.T) {
	rules := []ClassifierRule{
		mkClsRule("hook_fired", session.StateWaiting, nil),
		mkClsRule("interrupt_marker", session.StateReady, nil),
	}
	m := &session.SessionMetrics{PermissionPending: true, LastWasUserInterrupt: true}
	st, ev, ok := EvaluateRules(rules, session.StateWorking, EvalContext{Metrics: m})
	if !ok {
		t.Fatal("expected a rule to fire")
	}
	if st != session.StateWaiting {
		t.Errorf("hook_fired (first) should win → waiting; got %s", st)
	}
	if ev.RuleID != "hook_fired" {
		t.Errorf("evidence rule_id should be hook_fired, got %s", ev.RuleID)
	}
}

func TestEvaluateRules_transcriptFieldValue_endTurn(t *testing.T) {
	rules := []ClassifierRule{
		mkClsRule("transcript_field_value", session.StateReady, map[string]string{
			"field": "stop_reason", "value": "end_turn",
		}),
	}
	// IsAgentDone is the runtime proxy for stop_reason==end_turn.
	m := &session.SessionMetrics{LastEventType: "turn_done"}
	st, _, ok := EvaluateRules(rules, session.StateWorking, EvalContext{Metrics: m})
	if !ok {
		t.Fatal("expected rule to fire")
	}
	if st != session.StateReady {
		t.Errorf("expected ready, got %s", st)
	}
}
