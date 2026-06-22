package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/backchannel"
	"irrlicht/core/domain/session"
)

type stubRules struct{ rules []backchannel.Rule }

func (s stubRules) Rules() []backchannel.Rule { return s.rules }

type bcNopLog struct{}

func (bcNopLog) LogInfo(_, _, _ string)                                  {}
func (bcNopLog) LogError(_, _, _ string)                                 {}
func (bcNopLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (bcNopLog) Close() error                                            { return nil }

func sess(state string, util float64) *session.SessionState {
	return &session.SessionState{
		SessionID: "s1",
		State:     state,
		Adapter:   "claude-code",
		Metrics:   &session.SessionMetrics{ContextUtilization: util},
	}
}

// newEngine builds an engine with a controllable clock and toggle. *clock and
// *on are mutated by the test.
func newEngine(rules []backchannel.Rule, on *bool, clock *time.Time) *BackchannelEngine {
	e := NewBackchannelEngine(stubRules{rules}, nil, nil, func() bool { return *on }, bcNopLog{})
	e.now = func() time.Time { return *clock }
	return e
}

func waitingRule() backchannel.Rule {
	return backchannel.Rule{ID: "r1", Enabled: true,
		Trigger: backchannel.Trigger{Event: backchannel.EventWaiting},
		Actions: []backchannel.Action{{Kind: backchannel.ActionInput, Data: "hi\r"}}}
}

func TestEvaluate_EdgeFiresOncePerTransition(t *testing.T) {
	on := true
	clk := time.Unix(1000, 0)
	e := newEngine([]backchannel.Rule{waitingRule()}, &on, &clk)

	if got := e.evaluate(sess("working", 0)); got != nil {
		t.Fatalf("baseline (first sight) must not fire, got %d", len(got))
	}
	if got := e.evaluate(sess("waiting", 0)); len(got) != 1 {
		t.Fatalf("transition working→waiting should fire once, got %d", len(got))
	}
	if got := e.evaluate(sess("waiting", 0)); got != nil {
		t.Fatalf("staying waiting must not re-fire, got %d", len(got))
	}
}

func TestEvaluate_Cooldown(t *testing.T) {
	on := true
	clk := time.Unix(1000, 0)
	r := waitingRule()
	r.CooldownSeconds = 60
	e := newEngine([]backchannel.Rule{r}, &on, &clk)

	e.evaluate(sess("working", 0))                // baseline
	if len(e.evaluate(sess("waiting", 0))) != 1 { // fire
		t.Fatal("first waiting edge should fire")
	}
	e.evaluate(sess("working", 0)) // leave waiting
	if got := e.evaluate(sess("waiting", 0)); got != nil {
		t.Fatalf("re-fire within cooldown must be suppressed, got %d", len(got))
	}
	clk = clk.Add(61 * time.Second)
	e.evaluate(sess("working", 0))
	if len(e.evaluate(sess("waiting", 0))) != 1 {
		t.Fatal("after cooldown elapsed, the edge should fire again")
	}
}

func TestEvaluate_ContextPressureHysteresis(t *testing.T) {
	on := true
	clk := time.Unix(1000, 0)
	r := backchannel.Rule{ID: "p", Enabled: true,
		Trigger:         backchannel.Trigger{Event: backchannel.EventContextPressure, Threshold: 85},
		Actions:         []backchannel.Action{{Kind: backchannel.ActionInput, Data: "/compact\r"}},
		CooldownSeconds: 1}

	e := newEngine([]backchannel.Rule{r}, &on, &clk)
	e.evaluate(sess("working", 50)) // baseline below threshold
	clk = clk.Add(2 * time.Second)
	if len(e.evaluate(sess("working", 90))) != 1 {
		t.Fatal("rising across threshold should fire")
	}
	clk = clk.Add(2 * time.Second)
	if got := e.evaluate(sess("working", 92)); got != nil {
		t.Fatalf("staying above threshold must not re-fire, got %d", len(got))
	}
	clk = clk.Add(2 * time.Second)
	e.evaluate(sess("working", 70)) // drop below
	clk = clk.Add(2 * time.Second)
	if len(e.evaluate(sess("working", 88))) != 1 {
		t.Fatal("re-crossing upward should fire again")
	}
}

func TestEvaluate_InertWhenDisabledNoReplay(t *testing.T) {
	on := false
	clk := time.Unix(1000, 0)
	e := newEngine([]backchannel.Rule{waitingRule()}, &on, &clk)

	e.evaluate(sess("working", 0)) // baseline
	if got := e.evaluate(sess("waiting", 0)); got != nil {
		t.Fatalf("disabled: must not fire, got %d", len(got))
	}
	on = true // enable; the stale waiting edge must NOT replay
	if got := e.evaluate(sess("waiting", 0)); got != nil {
		t.Fatalf("enabling must not replay the already-passed edge, got %d", len(got))
	}
	e.evaluate(sess("working", 0))
	if len(e.evaluate(sess("waiting", 0))) != 1 {
		t.Fatal("a fresh edge after enabling should fire")
	}
}

func TestEvaluate_GlobalCap(t *testing.T) {
	on := true
	clk := time.Unix(1000, 0)
	r := backchannel.Rule{ID: "p", Enabled: true,
		Trigger:         backchannel.Trigger{Event: backchannel.EventContextPressure, Threshold: 85},
		Actions:         []backchannel.Action{{Kind: backchannel.ActionInterrupt}},
		CooldownSeconds: 1}
	e := newEngine([]backchannel.Rule{r}, &on, &clk)
	e.evaluate(sess("working", 0)) // baseline

	fires := 0
	for i := 0; i < 8; i++ {
		clk = clk.Add(2 * time.Second)
		fires += len(e.evaluate(sess("working", 90))) // cross up
		clk = clk.Add(2 * time.Second)
		e.evaluate(sess("working", 0)) // drop below (no fire)
	}
	if fires != maxActionsPerSessionPerMinute {
		t.Fatalf("expected the per-minute cap (%d) to bound fires, got %d", maxActionsPerSessionPerMinute, fires)
	}
}

func TestEvaluate_AdapterScope(t *testing.T) {
	on := true
	clk := time.Unix(1000, 0)
	r := waitingRule()
	r.Adapter = "codex" // scoped away from the claude-code session
	e := newEngine([]backchannel.Rule{r}, &on, &clk)
	e.evaluate(sess("working", 0))
	if got := e.evaluate(sess("waiting", 0)); got != nil {
		t.Fatalf("rule scoped to another adapter must not fire, got %d", len(got))
	}
}
