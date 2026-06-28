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

func sessTokens(state string, tokens int64) *session.SessionState {
	return &session.SessionState{
		SessionID: "s1",
		State:     state,
		Adapter:   "claude-code",
		Metrics:   &session.SessionMetrics{TotalTokens: tokens},
	}
}

// newEngine builds an engine with a controllable clock and toggle. *clock and
// *on are mutated by the test.
func newEngine(rules []backchannel.Rule, on *bool, clock *time.Time) *BackchannelEngine {
	e := NewBackchannelEngine(stubRules{rules}, nil, nil, nil, func() bool { return *on }, bcNopLog{})
	e.now = func() time.Time { return *clock }
	return e
}

// fakeForwarder records what runActions delegates, bypassing InputService's
// gates so the preset-translation logic is tested in isolation.
type fakeForwarder struct {
	sentInput   []byte
	sentCommand string
	commandSet  bool
	interrupted bool
}

func (f *fakeForwarder) SendInput(_ string, data []byte) error { f.sentInput = data; return nil }
func (f *fakeForwarder) SendCommand(_ string, cmd string) error {
	f.sentCommand, f.commandSet = cmd, true
	return nil
}
func (f *fakeForwarder) Interrupt(_ string) error { f.interrupted = true; return nil }

func claudeCompactPresets() map[string]map[string]string {
	return map[string]map[string]string{"claude-code": {backchannel.PresetCompact: "/compact"}}
}

func presetRule() backchannel.Rule {
	return backchannel.Rule{ID: "r", Enabled: true,
		Actions: []backchannel.Action{{Kind: backchannel.ActionInput, Preset: backchannel.PresetCompact}}}
}

func TestRunActions_PresetTranslatesPerAdapter(t *testing.T) {
	on := true
	fw := &fakeForwarder{}
	e := NewBackchannelEngine(stubRules{}, fw, claudeCompactPresets(), nil, func() bool { return on }, bcNopLog{})
	e.runActions(presetRule(), "s1", "claude-code")
	if fw.sentCommand != "/compact" {
		t.Errorf("SendCommand = %q, want %q", fw.sentCommand, "/compact")
	}
	if fw.sentInput != nil {
		t.Errorf("SendInput must not be used for a preset, got %q", fw.sentInput)
	}
}

func TestRunActions_UnsupportedPresetDoesNotFire(t *testing.T) {
	on := true
	fw := &fakeForwarder{}
	e := NewBackchannelEngine(stubRules{}, fw, claudeCompactPresets(), nil, func() bool { return on }, bcNopLog{})
	// codex declares no compact preset → the rule must not fire (no wrong command).
	e.runActions(presetRule(), "s1", "codex")
	if fw.commandSet || fw.sentInput != nil {
		t.Errorf("unsupported preset must not fire: command=%q input=%q", fw.sentCommand, fw.sentInput)
	}
}

func TestRunActions_CustomSendsRawVerbatim(t *testing.T) {
	on := true
	fw := &fakeForwarder{}
	e := NewBackchannelEngine(stubRules{}, fw, claudeCompactPresets(), nil, func() bool { return on }, bcNopLog{})
	r := backchannel.Rule{ID: "r", Enabled: true,
		Actions: []backchannel.Action{{Kind: backchannel.ActionInput, Data: "/foo\r"}}}
	e.runActions(r, "s1", "claude-code")
	if string(fw.sentInput) != "/foo\r" {
		t.Errorf("SendInput = %q, want %q", fw.sentInput, "/foo\r")
	}
	if fw.commandSet {
		t.Errorf("SendCommand must not be used for Custom, got %q", fw.sentCommand)
	}
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

func TestEvaluate_ContextTokensHysteresis(t *testing.T) {
	on := true
	clk := time.Unix(1000, 0)
	r := backchannel.Rule{ID: "pt", Enabled: true,
		Trigger:         backchannel.Trigger{Event: backchannel.EventContextTokens, Threshold: 150000},
		Actions:         []backchannel.Action{{Kind: backchannel.ActionInput, Data: "/compact\r"}},
		CooldownSeconds: 1}

	e := newEngine([]backchannel.Rule{r}, &on, &clk)
	e.evaluate(sessTokens("working", 50000)) // baseline below threshold
	clk = clk.Add(2 * time.Second)
	if len(e.evaluate(sessTokens("working", 160000))) != 1 {
		t.Fatal("rising across token threshold should fire")
	}
	clk = clk.Add(2 * time.Second)
	if got := e.evaluate(sessTokens("working", 170000)); got != nil {
		t.Fatalf("staying above token threshold must not re-fire, got %d", len(got))
	}
	clk = clk.Add(2 * time.Second)
	e.evaluate(sessTokens("working", 120000)) // drop below (e.g. after a /compact)
	clk = clk.Add(2 * time.Second)
	if len(e.evaluate(sessTokens("working", 155000))) != 1 {
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

func TestForget_EvictsSessionState(t *testing.T) {
	on := true
	clk := time.Unix(1000, 0)
	e := newEngine([]backchannel.Rule{waitingRule()}, &on, &clk)

	e.evaluate(sess("working", 0)) // baseline
	if len(e.evaluate(sess("waiting", 0))) != 1 {
		t.Fatal("first waiting edge should fire")
	}

	e.forget("s1")

	e.mu.Lock()
	leftovers := len(e.prevState) + len(e.prevUtil) + len(e.lastFired) + len(e.recent)
	e.mu.Unlock()
	if leftovers != 0 {
		t.Fatalf("forget left state behind: leftovers=%d", leftovers)
	}

	// After eviction the session is first-sight again, so an already-waiting
	// observation must NOT fire (no stale edge replay).
	if got := e.evaluate(sess("waiting", 0)); got != nil {
		t.Fatalf("post-forget first sight must not fire, got %d", len(got))
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
