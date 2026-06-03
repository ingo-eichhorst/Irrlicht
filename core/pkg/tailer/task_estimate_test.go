package tailer

import (
	"testing"

	"irrlicht/core/pkg/capacity"
)

// taskEstimateTestParser is a minimal parser that lifts a synthetic "est"
// field off the line into ParsedEvent.TaskEstimate, so these tests exercise
// only the tailer plumbing (persist across passes, latest wins) — marker
// parsing itself is covered in the claudecode adapter package.
type taskEstimateTestParser struct{}

func (p *taskEstimateTestParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{Timestamp: ParseTimestamp(raw), EventType: "assistant_message"}
	if v, ok := raw["est"].(map[string]interface{}); ok {
		total, _ := v["total"].(float64)
		done, _ := v["done"].(float64)
		ev.TaskEstimate = &TaskEstimate{
			TotalRounds:     int(total),
			CompletedRounds: int(done),
			ObservedAt:      ev.Timestamp.Unix(),
		}
	}
	return ev
}

func newTaskEstimateTestTailer(path string) *TranscriptTailer {
	tl := NewTranscriptTailer(path, &taskEstimateTestParser{}, "claude-code")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

func TestTailer_TaskEstimate_SurfacedOnMetrics(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0)},
		{"timestamp": ts(1), "est": map[string]interface{}{"total": float64(10), "done": float64(2)}},
	})
	m, err := newTaskEstimateTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate on metrics")
	}
	if m.TaskEstimate.TotalRounds != 10 || m.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/10", m.TaskEstimate.CompletedRounds, m.TaskEstimate.TotalRounds)
	}
}

func TestTailer_TaskEstimate_PersistsAcrossMarkerlessPasses(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "est": map[string]interface{}{"total": float64(10), "done": float64(3)}},
	})
	tl := newTaskEstimateTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}

	// A later pass with new markerless content must not drop the estimate.
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1)})
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.CompletedRounds != 3 {
		t.Fatalf("TaskEstimate = %+v, want persisted CompletedRounds=3", m.TaskEstimate)
	}
}

func TestTailer_TaskEstimate_LatestMarkerWins(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "est": map[string]interface{}{"total": float64(10), "done": float64(1)}},
	})
	tl := newTaskEstimateTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1), "est": map[string]interface{}{"total": float64(12), "done": float64(7)}})
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.CompletedRounds != 7 || m.TaskEstimate.TotalRounds != 12 {
		t.Fatalf("TaskEstimate = %+v, want latest (7/12)", m.TaskEstimate)
	}
}
