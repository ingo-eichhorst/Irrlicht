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

func TestEvaluateRules_transcriptEventKind(t *testing.T) {
	rules := []ClassifierRule{
		mkClsRule("transcript_event_kind", session.StateReady,
			map[string]string{"event_kind": "task_complete"}),
	}
	m := &session.SessionMetrics{LastEventType: "task_complete"}
	st, ev, ok := EvaluateRules(rules, session.StateWorking, EvalContext{Metrics: m})
	if !ok || st != session.StateReady {
		t.Errorf("want fired→ready; got fired=%v st=%s", ok, st)
	}
	if ev.RuleID != "transcript_event_kind" {
		t.Errorf("evidence rule_id wrong: %+v", ev)
	}

	m2 := &session.SessionMetrics{LastEventType: "tool_use"}
	if _, _, fired := EvaluateRules(rules, session.StateWorking, EvalContext{Metrics: m2}); fired {
		t.Error("expected non-fire on different event_kind")
	}
}

func TestEvaluateRules_transcriptTailRegex(t *testing.T) {
	rules := []ClassifierRule{
		mkClsRule("transcript_tail_regex", session.StateWorking,
			map[string]string{"pattern": `"role":"user"`}),
	}
	m := &session.SessionMetrics{}
	ec := EvalContext{Metrics: m, LastEventTxt: `{"role":"user","content":"hi"}`}
	st, _, ok := EvaluateRules(rules, session.StateReady, ec)
	if !ok || st != session.StateWorking {
		t.Errorf("expected match→working; got ok=%v st=%s", ok, st)
	}
}

func TestRuntimeSupportedKinds_matchesEvaluator(t *testing.T) {
	// Every kind in the supported map must have a matching case in
	// matchRuntime. We can't introspect cases directly, so we exercise
	// each kind with a no-op params payload and verify it doesn't panic.
	for kind := range RuntimeSupportedKinds {
		t.Run(kind, func(t *testing.T) {
			r := mkClsRule(kind, session.StateReady, map[string]string{})
			_ = matchRuntime(r, EvalContext{Metrics: &session.SessionMetrics{}})
		})
	}
}

func TestIsRuntimeSupported(t *testing.T) {
	if !IsRuntimeSupported("transcript_field_value") {
		t.Error("transcript_field_value should be runtime-supported")
	}
	if IsRuntimeSupported("pane_substring_present") {
		t.Error("pane_substring_present is validator-only, not runtime")
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
