package session

import (
	"testing"
	"time"
)

func TestForecastTaskCompletion_MeasuredRate(t *testing.T) {
	// 2 of 10 rounds in 240s → perRound = 120s, remaining 8 → eta = now + 960s.
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 10, CompletedRounds: 2}
	eta := ForecastTaskCompletion(est, 240, now)
	if eta == nil {
		t.Fatal("expected eta, got nil")
	}
	want := now.Add(960 * time.Second)
	if !eta.Equal(want) {
		t.Errorf("eta = %v, want %v", eta, want)
	}
}

func TestForecastTaskCompletion_NoProjectionPossible(t *testing.T) {
	now := time.Now()
	if eta := ForecastTaskCompletion(nil, 240, now); eta != nil {
		t.Error("nil estimate should yield nil eta")
	}
	if eta := ForecastTaskCompletion(&TaskEstimate{TotalRounds: 10, CompletedRounds: 0}, 240, now); eta != nil {
		t.Error("zero completed rounds should yield nil eta (no measured rate)")
	}
	if eta := ForecastTaskCompletion(&TaskEstimate{TotalRounds: 10, CompletedRounds: 2}, 0, now); eta != nil {
		t.Error("zero elapsed should yield nil eta")
	}
}

func TestForecastTaskCompletion_AllRoundsDone(t *testing.T) {
	// completed == total → remaining 0 → eta = now ("about to finish").
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 5, CompletedRounds: 5}
	eta := ForecastTaskCompletion(est, 600, now)
	if eta == nil || !eta.Equal(now) {
		t.Fatalf("eta = %v, want now (%v)", eta, now)
	}
}

func TestMergeMetrics_TaskEstimateResetPropagates(t *testing.T) {
	// No nil carry-over for the estimate: the tailer persists the last-seen
	// marker itself, so a nil in fresh metrics is a real reset (a new user
	// message started a new task) and must NOT be resurrected (issue #558).
	etaUnix := int64(1769000000)
	oldM := &SessionMetrics{
		TaskEstimate:      &TaskEstimate{TotalRounds: 10, CompletedRounds: 10, UpdatedAt: 1768999000},
		TaskCompletionEta: &etaUnix,
	}
	newM := &SessionMetrics{ModelName: "claude-sonnet-4-6"}
	got := MergeMetrics(newM, oldM)
	if got.TaskEstimate != nil {
		t.Errorf("TaskEstimate = %+v, want nil (reset must propagate)", got.TaskEstimate)
	}
	if got.TaskCompletionEta != nil {
		t.Errorf("TaskCompletionEta = %v, want nil (reset must propagate)", got.TaskCompletionEta)
	}

	// A fresh estimate copies through verbatim.
	freshEta := int64(1769000500)
	newM2 := &SessionMetrics{
		TaskEstimate:      &TaskEstimate{TotalRounds: 10, CompletedRounds: 7, UpdatedAt: 1769000400},
		TaskCompletionEta: &freshEta,
	}
	got2 := MergeMetrics(newM2, oldM)
	if got2.TaskEstimate == nil || got2.TaskEstimate.CompletedRounds != 7 {
		t.Errorf("fresh TaskEstimate should win, got %+v", got2.TaskEstimate)
	}
	if got2.TaskCompletionEta == nil || *got2.TaskCompletionEta != freshEta {
		t.Errorf("fresh TaskCompletionEta should win, got %v", got2.TaskCompletionEta)
	}
}
