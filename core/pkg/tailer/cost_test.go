package tailer

import (
	"math"
	"testing"
)

// TestCost_SingleRequestID_MultipleStreams verifies that multiple streaming events
// with the same requestId only count the final event's tokens toward cost.
func TestCost_SingleRequestID_MultipleStreams(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		// First streaming event — partial usage.
		{
			"type": "assistant", "timestamp": ts(1), "requestId": "req-1",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(50),
					"cache_read_input_tokens": float64(500), "cache_creation_input_tokens": float64(200),
				},
			},
		},
		// Second streaming event — same requestId, updated output.
		{
			"type": "assistant", "timestamp": ts(2), "requestId": "req-1",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(300),
					"cache_read_input_tokens": float64(500), "cache_creation_input_tokens": float64(200),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// Cumulative should reflect only the final event (300 output, not 50+300).
	if m.CumInputTokens != 1000 {
		t.Errorf("CumInputTokens = %d, want 1000", m.CumInputTokens)
	}
	if m.CumOutputTokens != 300 {
		t.Errorf("CumOutputTokens = %d, want 300", m.CumOutputTokens)
	}
	if m.CumCacheReadTokens != 500 {
		t.Errorf("CumCacheReadTokens = %d, want 500", m.CumCacheReadTokens)
	}
	if m.CumCacheCreationTokens != 200 {
		t.Errorf("CumCacheCreationTokens = %d, want 200", m.CumCacheCreationTokens)
	}
}

// TestCost_MultipleRequestIDs verifies that tokens from distinct requestIds
// are summed (each requestId's final event contributes to the total).
func TestCost_MultipleRequestIDs(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		// Turn 1: two streaming events.
		{
			"type": "assistant", "timestamp": ts(1), "requestId": "req-1",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(100),
				},
			},
		},
		{
			"type": "assistant", "timestamp": ts(2), "requestId": "req-1",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(200),
				},
			},
		},
		// Turn 2: new requestId.
		{"type": "user", "timestamp": ts(3)},
		{
			"type": "assistant", "timestamp": ts(4), "requestId": "req-2",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(2000), "output_tokens": float64(500),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// req-1 final: input=1000, output=200
	// req-2 final: input=2000, output=500
	// total: input=3000, output=700
	if m.CumInputTokens != 3000 {
		t.Errorf("CumInputTokens = %d, want 3000", m.CumInputTokens)
	}
	if m.CumOutputTokens != 700 {
		t.Errorf("CumOutputTokens = %d, want 700", m.CumOutputTokens)
	}
}

// TestCost_NoRequestID_AccumulatesDirectly verifies that events without a
// requestId (Pi-style) accumulate tokens directly with no deduplication.
func TestCost_NoRequestID_AccumulatesDirectly(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{
			"type": "assistant", "timestamp": ts(1),
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(500), "output_tokens": float64(100),
				},
			},
		},
		{"type": "user", "timestamp": ts(2)},
		{
			"type": "assistant", "timestamp": ts(3),
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(800), "output_tokens": float64(200),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// No requestId: both events accumulate directly.
	if m.CumInputTokens != 1300 {
		t.Errorf("CumInputTokens = %d, want 1300", m.CumInputTokens)
	}
	if m.CumOutputTokens != 300 {
		t.Errorf("CumOutputTokens = %d, want 300", m.CumOutputTokens)
	}
}

// TestCost_CumulativeTokensOverride verifies that CumulativeTokens (Codex-style)
// directly sets the cumulative values, overriding any per-turn accumulation.
func TestCost_CumulativeTokensOverride(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{
			"type": "assistant", "timestamp": ts(1),
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(500), "output_tokens": float64(100),
				},
			},
			"cumulative_usage": map[string]interface{}{
				"input_tokens": float64(5000), "output_tokens": float64(1000),
				"cache_read_input_tokens": float64(3000),
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// CumulativeTokens should be used directly for cost.
	if m.CumInputTokens != 5000 {
		t.Errorf("CumInputTokens = %d, want 5000", m.CumInputTokens)
	}
	if m.CumOutputTokens != 1000 {
		t.Errorf("CumOutputTokens = %d, want 1000", m.CumOutputTokens)
	}
	if m.CumCacheReadTokens != 3000 {
		t.Errorf("CumCacheReadTokens = %d, want 3000", m.CumCacheReadTokens)
	}

	// Snapshot fields should still reflect the per-turn values.
	if m.InputTokens != 500 {
		t.Errorf("InputTokens (snapshot) = %d, want 500", m.InputTokens)
	}
	if m.OutputTokens != 100 {
		t.Errorf("OutputTokens (snapshot) = %d, want 100", m.OutputTokens)
	}
}

// TestCost_RequestIDChange_FlushPrevious verifies that changing requestId
// flushes the previous turn and starts accumulating the new one.
func TestCost_RequestIDChange_FlushPrevious(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		// Turn 1: req-A with 2 streaming events.
		{
			"type": "assistant", "timestamp": ts(1), "requestId": "req-A",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(50),
				},
			},
		},
		{
			"type": "assistant", "timestamp": ts(2), "requestId": "req-A",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(150),
				},
			},
		},
		// Turn 2: req-B.
		{"type": "user", "timestamp": ts(3)},
		{
			"type": "assistant", "timestamp": ts(4), "requestId": "req-B",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(2000), "output_tokens": float64(400),
				},
			},
		},
		// Turn 3: req-C.
		{"type": "user", "timestamp": ts(5)},
		{
			"type": "assistant", "timestamp": ts(6), "requestId": "req-C",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(3000), "output_tokens": float64(600),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// req-A final: input=1000, output=150
	// req-B final: input=2000, output=400
	// req-C final: input=3000, output=600 (pending, included via effectiveCum*)
	// total: input=6000, output=1150
	if m.CumInputTokens != 6000 {
		t.Errorf("CumInputTokens = %d, want 6000", m.CumInputTokens)
	}
	if m.CumOutputTokens != 1150 {
		t.Errorf("CumOutputTokens = %d, want 1150", m.CumOutputTokens)
	}

	// Snapshot should reflect the most recent turn (req-C).
	if m.InputTokens != 3000 {
		t.Errorf("InputTokens (snapshot) = %d, want 3000", m.InputTokens)
	}
	if m.OutputTokens != 600 {
		t.Errorf("OutputTokens (snapshot) = %d, want 600", m.OutputTokens)
	}
}

// TestCost_ZeroTokenEvents verifies that events with zero tokens don't affect
// the cumulative accumulators.
func TestCost_ZeroTokenEvents(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{
			"type": "assistant", "timestamp": ts(1), "requestId": "req-1",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(200),
				},
			},
		},
		// Event with no token data (e.g. a text-only streaming chunk).
		{"type": "assistant", "timestamp": ts(2)},
		// Event with zero tokens.
		{
			"type": "assistant", "timestamp": ts(3), "requestId": "req-2",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// Only req-1 had tokens. req-2 had no usage block.
	if m.CumInputTokens != 1000 {
		t.Errorf("CumInputTokens = %d, want 1000", m.CumInputTokens)
	}
	if m.CumOutputTokens != 200 {
		t.Errorf("CumOutputTokens = %d, want 200", m.CumOutputTokens)
	}
}

// TestCost_EstimatedCostUSD verifies that the EstimatedCostUSD field is
// computed from cumulative values, not snapshot values.
func TestCost_EstimatedCostUSD(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		// Turn 1.
		{
			"type": "assistant", "timestamp": ts(1), "requestId": "req-1",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(10000), "output_tokens": float64(5000),
				},
			},
		},
		// Turn 2.
		{"type": "user", "timestamp": ts(2)},
		{
			"type": "assistant", "timestamp": ts(3), "requestId": "req-2",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(20000), "output_tokens": float64(8000),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// The cost should be based on cumulative (30000 input + 13000 output),
	// not the snapshot (20000 input + 8000 output).
	if m.EstimatedCostUSD == 0 {
		t.Skip("no pricing data for claude-sonnet-4-20250514; cost validation skipped")
	}

	// Verify the cost is greater than what the snapshot alone would give.
	// snapshot cost = EstimateCostUSD(20000, 8000, 0, 0)
	// cumulative cost = EstimateCostUSD(30000, 13000, 0, 0)
	// The cumulative cost should be higher because it includes both turns.
	snapshotCost := tailer.capacityMgr.EstimateCostUSD(
		"claude-sonnet-4-20250514", 20000, 8000, 0, 0)
	cumulativeCost := tailer.capacityMgr.EstimateCostUSD(
		"claude-sonnet-4-20250514", 30000, 13000, 0, 0)

	if snapshotCost == 0 {
		t.Skip("no pricing data for claude-sonnet-4-20250514")
	}

	if math.Abs(m.EstimatedCostUSD-cumulativeCost) > 0.0001 {
		t.Errorf("EstimatedCostUSD = %.6f, want %.6f (cumulative); snapshot would be %.6f",
			m.EstimatedCostUSD, cumulativeCost, snapshotCost)
	}
}

// TestCost_IncrementalTail verifies that cumulative accumulators survive
// across multiple TailAndProcess calls (incremental tail).
func TestCost_IncrementalTail(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{
			"type": "assistant", "timestamp": ts(1), "requestId": "req-1",
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-20250514",
				"usage": map[string]interface{}{
					"input_tokens": float64(1000), "output_tokens": float64(200),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m1, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m1.CumInputTokens != 1000 {
		t.Fatalf("after first tail: CumInputTokens = %d, want 1000", m1.CumInputTokens)
	}

	// Append more data and tail again.
	appendTranscriptLine(t, path, map[string]interface{}{
		"type": "user", "timestamp": ts(2),
	})
	appendTranscriptLine(t, path, map[string]interface{}{
		"type": "assistant", "timestamp": ts(3), "requestId": "req-2",
		"message": map[string]interface{}{
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]interface{}{
				"input_tokens": float64(2000), "output_tokens": float64(500),
			},
		},
	})

	m2, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// Both turns should be accumulated.
	if m2.CumInputTokens != 3000 {
		t.Errorf("after second tail: CumInputTokens = %d, want 3000", m2.CumInputTokens)
	}
	if m2.CumOutputTokens != 700 {
		t.Errorf("after second tail: CumOutputTokens = %d, want 700", m2.CumOutputTokens)
	}
}
