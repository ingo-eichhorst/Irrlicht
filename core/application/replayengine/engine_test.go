package replayengine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/session"
)

// repoRoot resolves the repository root from this test file's location so the
// test reads the committed fixtures regardless of the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// core/application/replayengine/engine_test.go → repo root is three up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// newestTranscript returns the committed transcript.jsonl of the NEWEST
// recording for a scenarios/ cell. Every recording lives under
// recordings/<name>/; the newest is the lexicographically-greatest name.
func newestTranscript(t *testing.T, cellFolder string) string {
	t.Helper()
	recsDir := filepath.Join(repoRoot(t), "replaydata", "agents", "claudecode", "scenarios", cellFolder, "recordings")
	entries, err := os.ReadDir(recsDir)
	if err != nil {
		t.Fatalf("read recordings dir %s: %v", recsDir, err)
	}
	newest := ""
	for _, e := range entries {
		if e.IsDir() && e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		t.Fatalf("no recordings under %s", recsDir)
	}
	return filepath.Join(recsDir, newest, "transcript.jsonl")
}

// TestReplayTranscript_producesWaitingFromQuestion is the regression guard
// for issue #461 finding #1: a transcript whose turn ends on a question must
// route through `waiting`. This is exactly the semantics the agent-onboarding
// viewer's old fabricated ready↔working arc could not express; now that the
// viewer drives this engine, the engine owning the behaviour keeps them in
// lockstep.
func TestReplayTranscript_producesWaitingFromQuestion(t *testing.T) {
	src := newestTranscript(t, "2-17_user-blocking-question")

	res, err := replayengine.ReplayTranscript(src, replayengine.Options{
		Adapter:                    claudecode.AdapterName,
		Parser:                     &claudecode.Parser{},
		DisableModelConfigFallback: true,
	})
	if err != nil {
		t.Fatalf("ReplayTranscript: %v", err)
	}
	if res == nil || len(res.Transitions) == 0 {
		t.Fatal("expected transitions, got none")
	}

	// First transition is always the synthetic initial ready state.
	if got := res.Transitions[0]; got.Cause != replayengine.CauseInit || got.NewState != session.StateReady {
		t.Errorf("first transition = %+v; want init→ready", got)
	}

	var sawWaiting, sawWorking bool
	var prevTime = res.Transitions[0].VirtualTime
	for i, tr := range res.Transitions {
		switch tr.NewState {
		case session.StateWaiting:
			sawWaiting = true
		case session.StateWorking:
			sawWorking = true
		}
		// Monotonic non-decreasing virtual time, so the viewer's playback
		// scheduler never sees a negative inter-event delta.
		if tr.VirtualTime.Before(prevTime) {
			t.Errorf("transition %d goes back in time: %v < %v", i, tr.VirtualTime, prevTime)
		}
		prevTime = tr.VirtualTime
	}
	if !sawWorking {
		t.Error("expected a working transition")
	}
	if !sawWaiting {
		t.Error("expected a waiting transition (turn ended on a question) — the classifier semantics the naive arc lacked")
	}
}

// TestReplayTranscript_emptyTranscript returns (nil, nil) for an empty file
// so callers can treat "no usable transcript" uniformly.
func TestReplayTranscript_emptyTranscript(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := replayengine.ReplayTranscript(empty, replayengine.Options{
		Adapter: claudecode.AdapterName,
		Parser:  &claudecode.Parser{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result for empty transcript, got %+v", res)
	}
}

// TestReplayTranscript_metricsTimelineClimbs guards the viewer-playback fix:
// EmitMetricsTimeline must yield cumulative, monotonically non-decreasing
// snapshots (ascending time, non-shrinking tokens + cost) so the recording
// viewer can animate cost/tokens turn-by-turn instead of showing the final
// total at frame 1. Uses the real 3-turn token-accounting fixture whose cache
// tokens accumulate across turns. Token accumulation is asserted directly
// (pricing-independent); cost is asserted only as non-decreasing because the
// LiteLLM pricing cache may be absent in a bare `go test`.
func TestReplayTranscript_metricsTimelineClimbs(t *testing.T) {
	src := newestTranscript(t, "5-1_token-accounting")

	res, err := replayengine.ReplayTranscript(src, replayengine.Options{
		Adapter:                    claudecode.AdapterName,
		Parser:                     &claudecode.Parser{},
		DisableModelConfigFallback: true,
		EmitMetricsTimeline:        true,
	})
	if err != nil {
		t.Fatalf("ReplayTranscript: %v", err)
	}
	if res == nil || len(res.MetricsTimeline) < 2 {
		t.Fatalf("expected a multi-point metrics timeline, got %d points", len(res.MetricsTimeline))
	}

	var prevTime = res.MetricsTimeline[0].VirtualTime
	var prevTokens int64
	var prevCost float64
	for i, p := range res.MetricsTimeline {
		if p.Metrics == nil {
			t.Fatalf("timeline point %d has nil metrics", i)
		}
		if p.VirtualTime.Before(prevTime) {
			t.Errorf("point %d goes back in time: %v < %v", i, p.VirtualTime, prevTime)
		}
		prevTime = p.VirtualTime
		if p.Metrics.TotalTokens < prevTokens {
			t.Errorf("point %d TotalTokens shrank: %d < %d", i, p.Metrics.TotalTokens, prevTokens)
		}
		prevTokens = p.Metrics.TotalTokens
		if p.Metrics.EstimatedCostUSD < prevCost {
			t.Errorf("point %d cost shrank: %v < %v", i, p.Metrics.EstimatedCostUSD, prevCost)
		}
		prevCost = p.Metrics.EstimatedCostUSD
	}

	// The whole point: the final snapshot carries strictly more tokens than the
	// first — i.e. metrics genuinely accumulate, not a flat final blob.
	first, last := res.MetricsTimeline[0].Metrics, res.MetricsTimeline[len(res.MetricsTimeline)-1].Metrics
	if last.TotalTokens <= first.TotalTokens {
		t.Errorf("expected tokens to climb across the timeline: first=%d last=%d", first.TotalTokens, last.TotalTokens)
	}

	// Tail parity: the timeline's final cost must equal a single whole-transcript
	// ComputeMetrics — the timeline is the same accumulation, just sampled.
	if lm := res.LastMetrics; lm != nil && last.EstimatedCostUSD != lm.EstimatedCostUSD {
		t.Errorf("final timeline cost %v != LastMetrics cost %v", last.EstimatedCostUSD, lm.EstimatedCostUSD)
	}
}
