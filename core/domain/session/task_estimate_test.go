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

func TestMergeMetrics_TaskEstimateCarryOver(t *testing.T) {
	// Markers are sporadic — a pass with no fresh marker must not drop the
	// last-seen estimate or the chip flickers (issue #558).
	etaUnix := int64(1769000000)
	oldM := &SessionMetrics{
		TaskEstimate:      &TaskEstimate{TotalRounds: 10, CompletedRounds: 3, UpdatedAt: 1768999000},
		TaskCompletionEta: &etaUnix,
	}
	newM := &SessionMetrics{ModelName: "claude-sonnet-4-6"}
	got := MergeMetrics(newM, oldM)
	if got.TaskEstimate == nil || got.TaskEstimate.CompletedRounds != 3 {
		t.Errorf("TaskEstimate = %+v, want carried-over 3/10", got.TaskEstimate)
	}
	if got.TaskCompletionEta == nil || *got.TaskCompletionEta != etaUnix {
		t.Errorf("TaskCompletionEta = %v, want carried-over %d", got.TaskCompletionEta, etaUnix)
	}

	// A fresh marker wins over the carried-over one.
	freshEta := int64(1769000500)
	newM2 := &SessionMetrics{
		TaskEstimate:      &TaskEstimate{TotalRounds: 10, CompletedRounds: 7, UpdatedAt: 1769000400},
		TaskCompletionEta: &freshEta,
	}
	got2 := MergeMetrics(newM2, oldM)
	if got2.TaskEstimate.CompletedRounds != 7 {
		t.Errorf("fresh TaskEstimate should win, got %+v", got2.TaskEstimate)
	}
	if *got2.TaskCompletionEta != freshEta {
		t.Errorf("fresh TaskCompletionEta should win, got %d", *got2.TaskCompletionEta)
	}
}
