package tailer

import (
	"testing"

	"irrlicht/core/pkg/capacity"
)

// extraParser emits one scripted contribution per transcript line and reports
// a scripted pending contribution, so the tailer's handling of
// PerTurnContribution.Extra can be driven without a real adapter (#2052).
type extraParser struct {
	contribs []*PerTurnContribution
	pending  *PerTurnContribution
	i        int
}

func (p *extraParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{EventType: "assistant", Timestamp: ParseTimestamp(raw)}
	if p.i < len(p.contribs) {
		ev.Contribution = p.contribs[p.i]
	}
	p.i++
	return ev
}

func (p *extraParser) PendingContribution() *PerTurnContribution { return p.pending }

func newExtraTailer(t *testing.T, p *extraParser, lines int) *TranscriptTailer {
	t.Helper()
	rows := make([]map[string]interface{}, lines)
	for i := range rows {
		rows[i] = map[string]interface{}{"type": "assistant", "timestamp": ts(i)}
	}
	tl := NewTranscriptTailer(writeTranscriptLines(t, rows), p, "claude-code")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

// TestCost_ApplyContribution_ExtraAccumulatesUnderOwnModel: a committed
// contribution's Extra entries land in cumByModel under their own model, so
// the advisor iteration is counted and priced at the advisor's model (#2052).
func TestCost_ApplyContribution_ExtraAccumulatesUnderOwnModel(t *testing.T) {
	p := &extraParser{contribs: []*PerTurnContribution{{
		Model: "claude-sonnet-4-5",
		Usage: UsageBreakdown{Input: 100, Output: 10},
		Extra: []PerTurnContribution{{Model: "claude-opus-4-6", Usage: UsageBreakdown{Input: 5000, Output: 700}}},
	}}}
	tl := newExtraTailer(t, p, 1)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	adv := tl.cumByModel["claude-opus-4-6"]
	if adv == nil || adv.Input != 5000 || adv.Output != 700 {
		t.Fatalf("cumByModel[claude-opus-4-6] = %+v, want Input 5000 Output 700", adv)
	}
	if main := tl.cumByModel["claude-sonnet-4-5"]; main == nil || main.Input != 100 {
		t.Fatalf("cumByModel[claude-sonnet-4-5] = %+v, want Input 100", main)
	}
	if m.CumInputTokens != 5100 || m.CumOutputTokens != 710 {
		t.Errorf("Cum in/out = %d/%d, want 5100/710", m.CumInputTokens, m.CumOutputTokens)
	}
}

// TestCost_PendingContribution_ExtraIncludedInLiveCost: the in-progress
// turn's Extra entries count toward the live totals and cost before the turn
// is flushed (#2052).
func TestCost_PendingContribution_ExtraIncludedInLiveCost(t *testing.T) {
	p := &extraParser{
		// A committed turn makes the tailer take the per-model priced path.
		contribs: []*PerTurnContribution{{Model: "claude-sonnet-4-5", Usage: UsageBreakdown{Input: 1}}},
		pending: &PerTurnContribution{
			Model: "claude-sonnet-4-5",
			Usage: UsageBreakdown{Input: 100},
			Extra: []PerTurnContribution{{Model: "claude-opus-4-6", Usage: UsageBreakdown{Input: 1_000_000}}},
		},
	}
	tl := newExtraTailer(t, p, 1)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.CumInputTokens != 1_000_101 {
		t.Errorf("CumInputTokens = %d, want 1000101 (pending extra included)", m.CumInputTokens)
	}
	// claude-opus-4-6 input in the fixture is $15/Mtok, so the extra alone is $15.
	if m.EstimatedCostUSD < 15 {
		t.Errorf("EstimatedCostUSD = %f, want >= 15 (pending extra priced at its own model)", m.EstimatedCostUSD)
	}
}

// TestCost_PendingContribution_FirstTurnUsesPricedPath: before any turn is
// committed, cumByModel is empty. The live cost must still come from the
// pending contribution, Extra included, rather than the legacy ev.Tokens
// path, which for Claude Code now holds only the last message iteration
// (#2052 review).
func TestCost_PendingContribution_FirstTurnUsesPricedPath(t *testing.T) {
	p := &extraParser{pending: &PerTurnContribution{
		Model: "claude-sonnet-4-5",
		Usage: UsageBreakdown{Input: 100},
		Extra: []PerTurnContribution{{Model: "claude-opus-4-6", Usage: UsageBreakdown{Input: 1_000_000}}},
	}}
	tl := newExtraTailer(t, p, 1)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.CumInputTokens != 1_000_100 {
		t.Errorf("CumInputTokens = %d, want 1000100 (pending turn and its extra)", m.CumInputTokens)
	}
	if m.EstimatedCostUSD < 15 {
		t.Errorf("EstimatedCostUSD = %f, want >= 15", m.EstimatedCostUSD)
	}
}
