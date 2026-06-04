package session

import (
	"testing"
	"time"
)

func TestForecastTaskCompletion_MeasuredRate(t *testing.T) {
	// No marker timestamp → anchored at now: 2 of 10 rounds in 240s →
	// perRound = 120s, remaining 8 → eta = now + 960s.
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 10, CompletedRounds: 2}
	eta := ForecastTaskCompletion(est, nil, 240, now)
	if eta == nil {
		t.Fatal("expected eta, got nil")
	}
	want := now.Add(960 * time.Second)
	if !eta.Equal(want) {
		t.Errorf("eta = %v, want %v", eta, want)
	}
}

func TestForecastTaskCompletion_AnchoredAtMarker(t *testing.T) {
	// The projection anchors at the marker and freezes the rate there, so
	// markerless passes don't slide the eta forward — UIs count down against
	// a stable target (issue #558).
	marker := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 10, CompletedRounds: 2, UpdatedAt: marker.Unix()}

	// At the marker: elapsed 240s → perRound 120 → eta = marker + 960s.
	want := marker.Add(960 * time.Second)
	etaAtMarker := ForecastTaskCompletion(est, nil, 240, marker)
	if etaAtMarker == nil || !etaAtMarker.Equal(want) {
		t.Fatalf("eta at marker = %v, want %v", etaAtMarker, want)
	}

	// 60s later with no fresh marker (elapsed grew to 300): the gap is
	// subtracted, the anchor stays the marker → identical eta.
	etaLater := ForecastTaskCompletion(est, nil, 300, marker.Add(60*time.Second))
	if etaLater == nil || !etaLater.Equal(want) {
		t.Errorf("eta 60s later = %v, want unchanged %v", etaLater, want)
	}
}

func TestForecastTaskCompletion_DeltaRatePreferred(t *testing.T) {
	// With the task's first marker as base, the rate comes from marker
	// deltas — immune to previous tasks/idle time in the session elapsed:
	// 8 rounds in 320s since the 0/10 base → perRound 40s, remaining 2 →
	// eta = marker + 80s. The huge session elapsed must be ignored.
	taskStart := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	base := &TaskEstimate{TotalRounds: 10, CompletedRounds: 0, UpdatedAt: taskStart.Unix()}
	est := &TaskEstimate{TotalRounds: 10, CompletedRounds: 8, UpdatedAt: taskStart.Add(320 * time.Second).Unix()}
	now := taskStart.Add(330 * time.Second)

	eta := ForecastTaskCompletion(est, base, 9999 /* poisoned session elapsed */, now)
	if eta == nil {
		t.Fatal("expected eta, got nil")
	}
	want := taskStart.Add((320 + 80) * time.Second)
	if !eta.Equal(want) {
		t.Errorf("eta = %v, want %v (delta rate, not session elapsed)", eta, want)
	}
}

func TestForecastTaskCompletion_BaseWithoutProgressFallsBack(t *testing.T) {
	// base == est (single marker so far): no delta to measure → fall back
	// to the session-elapsed rate.
	marker := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 10, CompletedRounds: 2, UpdatedAt: marker.Unix()}
	eta := ForecastTaskCompletion(est, est, 240, marker)
	if eta == nil {
		t.Fatal("expected fallback eta, got nil")
	}
	want := marker.Add(960 * time.Second)
	if !eta.Equal(want) {
		t.Errorf("eta = %v, want %v (elapsed fallback)", eta, want)
	}
}

func TestForecastTaskCompletion_NoProjectionPossible(t *testing.T) {
	now := time.Now()
	if eta := ForecastTaskCompletion(nil, nil, 240, now); eta != nil {
		t.Error("nil estimate should yield nil eta")
	}
	if eta := ForecastTaskCompletion(&TaskEstimate{TotalRounds: 10, CompletedRounds: 0}, nil, 240, now); eta != nil {
		t.Error("zero completed rounds should yield nil eta (no measured rate)")
	}
	if eta := ForecastTaskCompletion(&TaskEstimate{TotalRounds: 10, CompletedRounds: 2}, nil, 0, now); eta != nil {
		t.Error("zero elapsed should yield nil eta")
	}
}

func TestForecastTaskCompletion_AllRoundsDone(t *testing.T) {
	// completed == total → remaining 0 → eta = now ("about to finish").
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 5, CompletedRounds: 5}
	eta := ForecastTaskCompletion(est, nil, 600, now)
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
