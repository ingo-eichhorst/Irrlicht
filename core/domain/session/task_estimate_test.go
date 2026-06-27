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

func TestForecastTaskCompletion_NoEstimateYieldsNil(t *testing.T) {
	// The only no-projection case left after #753: no estimate at all.
	if eta := ForecastTaskCompletion(nil, nil, 240, time.Now()); eta != nil {
		t.Error("nil estimate should yield nil eta")
	}
}

func TestForecastTaskCompletion_PriorBootstrapAtZeroRounds(t *testing.T) {
	// #753: with zero completed rounds there is no measured rate, but a real
	// eta still appears at the very first marker — total_rounds × the corpus
	// prior — instead of "estimating…". This is the time-to-first-estimate win.
	marker := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 4, CompletedRounds: 0, UpdatedAt: marker.Unix()}
	eta := ForecastTaskCompletion(est, est, 0, marker)
	if eta == nil {
		t.Fatal("zero completed rounds should now surface a prior-based eta (#753), got nil")
	}
	want := marker.Add(time.Duration(4*taskRoundPriorSeconds) * time.Second)
	if !eta.Equal(want) {
		t.Errorf("eta = %v, want %v (total_rounds × prior)", eta, want)
	}
}

func TestForecastTaskCompletion_PriorFallbackWhenRateUnmeasurable(t *testing.T) {
	// Progress reported but no measurable rate (no base, zero elapsed): fall
	// back to the prior rather than nil, so the chip keeps showing a number.
	marker := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	est := &TaskEstimate{TotalRounds: 10, CompletedRounds: 2, UpdatedAt: marker.Unix()}
	eta := ForecastTaskCompletion(est, nil, 0, marker)
	if eta == nil {
		t.Fatal("unmeasurable rate should fall back to the prior, got nil")
	}
	want := marker.Add(time.Duration(8*taskRoundPriorSeconds) * time.Second)
	if !eta.Equal(want) {
		t.Errorf("eta = %v, want %v (remaining × prior)", eta, want)
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

func TestFresherTaskEstimate(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	fresh := &TaskEstimate{Source: "marker", UpdatedAt: now.Add(-60 * time.Second).Unix()}
	newest := &TaskEstimate{Source: "tasks", UpdatedAt: now.Add(-10 * time.Second).Unix()}
	stale := &TaskEstimate{Source: "marker", UpdatedAt: now.Add(-10 * time.Minute).Unix()}
	newerThanStale := &TaskEstimate{Source: "tasks", UpdatedAt: now.Add(-5 * time.Minute).Unix()}
	olderThanStale := &TaskEstimate{Source: "tasks", UpdatedAt: now.Add(-20 * time.Minute).Unix()}

	for name, tc := range map[string]struct {
		preferred, challenger, want *TaskEstimate
	}{
		"nil preferred yields challenger":      {nil, newest, newest},
		"nil challenger yields preferred":      {stale, nil, stale},
		"fresh preferred holds off newer":      {fresh, newest, fresh},
		"stale preferred loses to newer":       {stale, newerThanStale, newerThanStale},
		"stale preferred wins over even older": {stale, olderThanStale, stale},
		"both nil":                             {nil, nil, nil},
	} {
		if got := FresherTaskEstimate(tc.preferred, tc.challenger, now); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.want)
		}
	}
}

func TestTaskEstimateFromTasks_NoCompletions(t *testing.T) {
	if est, base := TaskEstimateFromTasks(nil); est != nil || base != nil {
		t.Errorf("no tasks: got (%+v, %+v), want (nil, nil)", est, base)
	}
	pending := []Task{{ID: "1", Status: "pending"}, {ID: "2", Status: "in_progress"}}
	if est, base := TaskEstimateFromTasks(pending); est != nil || base != nil {
		t.Errorf("no completed tasks: got (%+v, %+v), want (nil, nil)", est, base)
	}
}

func TestTaskEstimateFromTasks_DeltaRateThroughForecast(t *testing.T) {
	// 4 tasks, 2 completed 90s apart → est anchors at the latest completion,
	// base reconstructs the first → ForecastTaskCompletion's delta rate:
	// perRound = 90s, remaining 2 → eta = latest + 180s.
	t1 := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(90 * time.Second)
	tasks := []Task{
		{ID: "1", Status: "completed", CompletedAt: t1.Unix()},
		{ID: "2", Status: "completed", CompletedAt: t2.Unix()},
		{ID: "3", Status: "in_progress"},
		{ID: "4", Status: "pending"},
	}
	est, base := TaskEstimateFromTasks(tasks)
	if est == nil || est.TotalRounds != 4 || est.CompletedRounds != 2 || est.UpdatedAt != t2.Unix() {
		t.Fatalf("est = %+v, want 2/4 anchored at latest completion", est)
	}
	if est.Source != "tasks" {
		t.Errorf("est.Source = %q, want \"tasks\"", est.Source)
	}
	if base == nil || base.CompletedRounds != 1 || base.UpdatedAt != t1.Unix() {
		t.Fatalf("base = %+v, want 1/4 at first completion", base)
	}
	eta := ForecastTaskCompletion(est, base, 9999 /* poisoned session elapsed */, t2.Add(10*time.Second))
	want := t2.Add(180 * time.Second)
	if eta == nil || !eta.Equal(want) {
		t.Errorf("eta = %v, want %v (delta rate from completion stamps)", eta, want)
	}
}

func TestTaskEstimateFromTasks_SingleCompletionFallsBack(t *testing.T) {
	// One stamped completion: no delta to measure → nil base, and the
	// forecast uses the marker-anchored session-elapsed rate.
	t1 := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	tasks := []Task{
		{ID: "1", Status: "completed", CompletedAt: t1.Unix()},
		{ID: "2", Status: "pending"},
	}
	est, base := TaskEstimateFromTasks(tasks)
	if est == nil || est.CompletedRounds != 1 || base != nil {
		t.Fatalf("got (%+v, %+v), want (1/2 est, nil base)", est, base)
	}
	// 1 round in 60s elapsed → remaining 1 → eta = completion + 60s.
	eta := ForecastTaskCompletion(est, base, 60, t1)
	want := t1.Add(60 * time.Second)
	if eta == nil || !eta.Equal(want) {
		t.Errorf("eta = %v, want %v (elapsed fallback)", eta, want)
	}
}

func TestTaskEstimateFromTasks_UnstampedCompletionsCountAsPreFirst(t *testing.T) {
	// A completion restored from an older ledger has no stamp: it still
	// counts toward progress, and the base treats it as done before the
	// first stamp — rate spans only the stamped interval.
	t1 := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(40 * time.Second)
	tasks := []Task{
		{ID: "1", Status: "completed"}, // unstamped
		{ID: "2", Status: "completed", CompletedAt: t1.Unix()},
		{ID: "3", Status: "completed", CompletedAt: t2.Unix()},
		{ID: "4", Status: "pending"},
	}
	est, base := TaskEstimateFromTasks(tasks)
	if est == nil || est.CompletedRounds != 3 || est.UpdatedAt != t2.Unix() {
		t.Fatalf("est = %+v, want 3/4 anchored at latest stamp", est)
	}
	if base == nil || base.CompletedRounds != 2 || base.UpdatedAt != t1.Unix() {
		t.Fatalf("base = %+v, want 2/4 at first stamp", base)
	}
	// perRound = (t2−t1)/(3−2) = 40s, remaining 1 → eta = t2 + 40s.
	eta := ForecastTaskCompletion(est, base, 9999, t2)
	want := t2.Add(40 * time.Second)
	if eta == nil || !eta.Equal(want) {
		t.Errorf("eta = %v, want %v", eta, want)
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
