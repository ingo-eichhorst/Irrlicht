package antigravity

import "testing"

// Antigravity recordings repeat a planner step under the same step_index
// (1-2_session-end lines 3 and 8), so the sample is keyed by step and the
// tailer observes it once (#737 review).
func TestParser_Hedge_KeyedByStepIndex(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 3, "I'm not sure the command exists.", nil))
	if ev.Hedge == nil || ev.Hedge.Weight == 0 {
		t.Fatalf("Hedge = %+v, want a weighted sample", ev.Hedge)
	}
	if ev.HedgeKey != "step-3" {
		t.Errorf("HedgeKey = %q, want step-3", ev.HedgeKey)
	}
}
