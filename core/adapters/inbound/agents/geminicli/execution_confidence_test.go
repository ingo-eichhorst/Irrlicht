package geminicli

import "testing"

// Gemini rewrites a streaming assistant message in place under one id, so the
// same message can appear on several lines. Each carries the message id as
// its HedgeKey, so the tailer observes the message once (#737 review).
func TestParser_Hedge_KeyedByMessageID(t *testing.T) {
	p := &Parser{}
	raw := `{"id":"g9","type":"gemini","content":"I'm not sure which file holds the config.","model":"gemini-3-flash-preview"}`
	for i := 0; i < 2; i++ {
		ev := p.ParseLine(decode(t, raw))
		if ev.Hedge == nil || ev.Hedge.Weight == 0 {
			t.Fatalf("emission %d: Hedge = %+v, want a weighted sample", i, ev.Hedge)
		}
		if ev.HedgeKey != "g9" {
			t.Errorf("emission %d: HedgeKey = %q, want the message id g9", i, ev.HedgeKey)
		}
	}
}
