package junie

import "testing"

// Junie re-emits each block as a stepId-keyed update stream (IN_PROGRESS,
// COMPLETED, then a replay at task end), so thought and result blocks carry
// their stepId as HedgeKey and the tailer observes each block once (#737
// review).
func TestParser_Hedge_KeyedByStepID(t *testing.T) {
	cases := map[string]string{
		"thought": `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"AgentThoughtBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"step-a","text":"I'm not sure which document holds the key ideas."}},"taskId":"t1","timestampMs":1787586223111}`,
		"result":  `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"step-b","cancelled":false,"result":"The spec is ambiguous, so I picked the list view.","title":"Done","changes":[],"errorCode":"Submit"}},"taskId":"t1","timestampMs":1787585375354}`,
	}
	want := map[string]string{"thought": "step-a", "result": "step-b"}
	for name, raw := range cases {
		ev := (&Parser{}).ParseLine(line(t, raw))
		if ev.Hedge == nil || ev.Hedge.Weight == 0 {
			t.Fatalf("%s: Hedge = %+v, want a weighted sample", name, ev.Hedge)
		}
		if ev.HedgeKey != want[name] {
			t.Errorf("%s: HedgeKey = %q, want %q", name, ev.HedgeKey, want[name])
		}
	}
}
