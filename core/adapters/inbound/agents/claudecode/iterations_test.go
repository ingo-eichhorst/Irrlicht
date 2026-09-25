package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// loadAdvisorIterationsLine returns the one assistant line in
// testdata/advisor-iterations.jsonl: a real Claude Code 2.1.278 transcript
// entry (content redacted, usage verbatim) for a turn that called the
// server-side advisor tool. Its usage.iterations is
// [message, advisor_message(claude-fable-5-1), message] — issue #2052.
func loadAdvisorIterationsLine(t *testing.T) map[string]interface{} {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "advisor-iterations.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return raw
}

// TestParser_Iterations_ContextFromLastMessageIteration: the top-level usage
// of a multi-iteration message sums both `message` iterations, so reading it
// as the context size doubles the context bar (#2052). The context snapshot
// must come from the LAST iteration of type "message".
func TestParser_Iterations_ContextFromLastMessageIteration(t *testing.T) {
	ev := (&Parser{}).ParseLine(loadAdvisorIterationsLine(t))
	if ev == nil || ev.Tokens == nil {
		t.Fatal("expected a token snapshot")
	}
	// Last message iteration: input 2, output 577, cache_read 62504, cache_creation 1528.
	want := tailer.TokenSnapshot{Input: 2, Output: 577, CacheRead: 62504, CacheCreation: 1528, Total: 2 + 577 + 62504 + 1528}
	if *ev.Tokens != want {
		t.Errorf("Tokens = %+v, want %+v (last message iteration, not the top-level sum)", *ev.Tokens, want)
	}
}

// TestParser_Iterations_ContributionSumsMessageIterations: the top-level
// usage.cache_creation 5m/1h split reflects only the FIRST iteration (checked
// over 49 local multi-iteration messages: its sum is always below the flat
// cache_creation_input_tokens, which equals the message iterations' sum). The
// cost breakdown for the main model must be the sum of the message iterations.
func TestParser_Iterations_ContributionSumsMessageIterations(t *testing.T) {
	p := &Parser{}
	_ = p.ParseLine(loadAdvisorIterationsLine(t))
	c := p.PendingContribution()
	if c == nil {
		t.Fatal("expected a pending contribution")
	}
	want := tailer.UsageBreakdown{Input: 4, Output: 669, CacheRead: 123455, CacheCreation5m: 1528, CacheCreation1h: 1553}
	if c.Model != "claude-opus-5" || c.Usage != want {
		t.Errorf("contribution = %s %+v, want claude-opus-5 %+v", c.Model, c.Usage, want)
	}
}

// TestParser_Iterations_AdvisorContribution: the advisor iteration is billed
// at its own model and appears nowhere in the top-level usage, so it must be
// emitted as an extra contribution keyed by that model (#2052).
func TestParser_Iterations_AdvisorContribution(t *testing.T) {
	p := &Parser{}
	_ = p.ParseLine(loadAdvisorIterationsLine(t))
	c := p.PendingContribution()
	if c == nil {
		t.Fatal("expected a pending contribution")
	}
	if len(c.Extra) != 1 {
		t.Fatalf("Extra = %+v, want exactly one advisor contribution", c.Extra)
	}
	want := tailer.UsageBreakdown{Input: 63892, Output: 4130}
	if c.Extra[0].Model != "claude-fable-5-1" || c.Extra[0].Usage != want {
		t.Errorf("advisor = %s %+v, want claude-fable-5-1 %+v", c.Extra[0].Model, c.Extra[0].Usage, want)
	}
}

// TestParser_Iterations_AbsentKeepsTopLevel is a LOCK: a message without
// usage.iterations keeps today's top-level snapshot and breakdown.
func TestParser_Iterations_AbsentKeepsTopLevel(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "assistant", "requestId": "req-1",
		"message": map[string]interface{}{"model": "claude-sonnet-4-6", "usage": map[string]interface{}{
			"input_tokens": float64(10), "output_tokens": float64(20),
			"cache_read_input_tokens": float64(300), "cache_creation_input_tokens": float64(40),
		}},
	})
	if ev.Tokens == nil || ev.Tokens.Total != 370 {
		t.Fatalf("Tokens = %+v, want Total 370", ev.Tokens)
	}
	c := p.PendingContribution()
	want := tailer.UsageBreakdown{Input: 10, Output: 20, CacheRead: 300, CacheCreation5m: 40}
	if c == nil || c.Usage != want || len(c.Extra) != 0 {
		t.Errorf("contribution = %+v, want %+v and no extras", c, want)
	}
}

// TestParser_Iterations_StreamedTurnCountsAdvisorOnce is a LOCK on the
// request-ID dedup: several streamed lines of the same advisor turn flush as
// ONE contribution carrying ONE advisor extra.
func TestParser_Iterations_StreamedTurnCountsAdvisorOnce(t *testing.T) {
	p := &Parser{}
	line := loadAdvisorIterationsLine(t)
	_ = p.ParseLine(line)
	_ = p.ParseLine(loadAdvisorIterationsLine(t))
	ev := p.ParseLine(map[string]interface{}{
		"type": "assistant", "requestId": "req-next",
		"message": map[string]interface{}{"model": "claude-opus-5", "usage": map[string]interface{}{"input_tokens": float64(1)}},
	})
	if ev.Contribution == nil || len(ev.Contribution.Extra) != 1 {
		t.Fatalf("flushed contribution = %+v, want one advisor extra", ev.Contribution)
	}
}

// TestTailer_Iterations_TotalTokensIsLastMessageIteration drives the real
// tailer over the fixture: metrics.TotalTokens — what /api/v1/sessions reports
// and the context bar and pressure level read — must be the last message
// iteration, not the top-level sum (#2052).
func TestTailer_Iterations_TotalTokensIsLastMessageIteration(t *testing.T) {
	tl := tailer.NewTranscriptTailer(filepath.Join("testdata", "advisor-iterations.jsonl"), &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalTokens != 2+577+62504+1528 {
		t.Errorf("TotalTokens = %d, want %d (last message iteration)", m.TotalTokens, 2+577+62504+1528)
	}
}
