package etaresearch

import (
	"math"
	"testing"

	"irrlicht/core/domain/session"
)

// reachedEpisode builds a finished task: rounds 0..total at 60s spacing, base
// pinned to the first marker (the production rate anchor). Ground truth is the
// last marker (rounds==total).
func reachedEpisode(total int) Episode { return reachedEpisodeWithTail(total, nil) }

// reachedEpisodeWithTail additionally appends markerless activity after the
// final marker, at the given offsets (seconds past that marker), so the
// last-mile walk has something to find.
func reachedEpisodeWithTail(total int, tailOffsets []int64) Episode {
	base := &session.TaskEstimate{TotalRounds: total, CompletedRounds: 0, UpdatedAt: 1000, Source: session.MarkerEstimateSource}
	var turns []Turn
	var activity []int64
	for c := 0; c <= total; c++ {
		ts := int64(1000 + c*60)
		activity = append(activity, ts)
		turns = append(turns, Turn{
			VirtualUnix:    ts,
			Est:            &session.TaskEstimate{TotalRounds: total, CompletedRounds: c, UpdatedAt: ts, Source: session.MarkerEstimateSource},
			Base:           base,
			ElapsedSeconds: ts - 990,
		})
	}
	last := int64(1000 + total*60)
	for _, off := range tailOffsets {
		activity = append(activity, last+off)
	}
	ep := Episode{Source: "synthetic", Turns: turns}
	finalizeEpisode(&ep, activity, math.MaxInt64)
	return ep
}

func TestMedianPerRound(t *testing.T) {
	if got := MedianPerRound([]Episode{reachedEpisode(4)}); math.Abs(got-60) > 0.001 {
		t.Fatalf("prior = %v, want 60", got)
	}
}

func TestBaselineNilAtZeroRounds_BootstrapSurfaces(t *testing.T) {
	ep := reachedEpisode(4)
	if got := Baseline().Predict(ep, 0); got != nil {
		t.Fatalf("baseline at 0 rounds = %v, want nil (no measured rate)", got)
	}
	boot := PriorBootstrap(60).Predict(ep, 0)
	if boot == nil {
		t.Fatal("prior-bootstrap at 0 rounds = nil, want a prior-based eta")
	}
	// anchor(1000) + remaining(4) × prior(60) = 1240.
	if boot.Unix() != 1240 {
		t.Fatalf("bootstrap eta = %d, want 1240", boot.Unix())
	}
}

func TestBootstrapEqualsBaselineOnMeasuredTurns(t *testing.T) {
	ep := reachedEpisode(4)
	for i := 1; i < len(ep.Turns)-1; i++ { // measured turns, before the end
		b := Baseline().Predict(ep, i)
		p := PriorBootstrap(60).Predict(ep, i)
		if b == nil || p == nil || b.Unix() != p.Unix() {
			t.Fatalf("turn %d: baseline=%v bootstrap=%v, want identical non-nil", i, b, p)
		}
	}
}

// The core invariant the shipped change must hold: prior-bootstrap surfaces a
// number at least as soon as baseline and never in fewer episodes.
func TestBootstrapSurfacesSooner(t *testing.T) {
	eps := []Episode{reachedEpisode(3), reachedEpisode(5), reachedEpisode(4)}
	base := ScoreEstimator(Baseline(), eps, true)
	boot := ScoreEstimator(PriorBootstrap(60), eps, true)
	if boot.MeanSecsToFirst > base.MeanSecsToFirst {
		t.Fatalf("bootstrap secs-to-first %v > baseline %v", boot.MeanSecsToFirst, base.MeanSecsToFirst)
	}
	if boot.FirstCoverage < base.FirstCoverage {
		t.Fatalf("bootstrap coverage %v < baseline %v", boot.FirstCoverage, base.FirstCoverage)
	}
	if boot.MeanSecsToFirst != 0 {
		t.Fatalf("bootstrap secs-to-first = %v, want 0 (surfaces at the first marker)", boot.MeanSecsToFirst)
	}
}

// The `production` row must BE production, not a lookalike — that is the whole
// reason it exists, so the report can never describe code nobody runs.
func TestProductionEstimatorIsTheShippedSeam(t *testing.T) {
	ep := reachedEpisode(4)
	for i := range ep.Turns {
		tn := ep.Turns[i]
		want := session.ForecastTaskCompletion(tn.Est, tn.Base, tn.ElapsedSeconds, unix(tn.VirtualUnix))
		got := Production().Predict(ep, i)
		if (got == nil) != (want == nil) {
			t.Fatalf("turn %d: production=%v, seam=%v", i, got, want)
		}
		if got != nil && !got.Equal(*want) {
			t.Fatalf("turn %d: production=%v, seam=%v", i, got, want)
		}
	}
}

// The last mile is the measurement the accuracy table structurally cannot make:
// its ground truth IS the final marker, so work after it scores as nothing.
func TestLastMileMeasuresPostCompletionTail(t *testing.T) {
	// 60s/round; 120s of markerless work after the final marker = 2.0 rounds.
	ep := reachedEpisodeWithTail(4, []int64{60, 120})
	if got := ep.TailSecondsAt(DefaultIdleCutoffSeconds); got != 120 {
		t.Fatalf("tail = %ds, want 120", got)
	}
	lm := LastMile([]Episode{ep}, 0.4, DefaultIdleCutoffSeconds)
	if lm.Episodes != 1 || lm.WithTail != 1 {
		t.Fatalf("lastmile = %d episodes / %d with tail, want 1/1", lm.Episodes, lm.WithTail)
	}
	if math.Abs(lm.MedianRounds-2.0) > 0.001 {
		t.Errorf("median tail = %v rounds, want 2.0", lm.MedianRounds)
	}
	// Predicting zero is wrong by the whole tail, which is what MedianSeconds
	// already reports; the 0.4-round floor (0.4 × 60s = 24s) leaves 96s.
	if lm.MedianSeconds != 120 || math.Abs(lm.ErrPaddedSeconds-96) > 0.001 {
		t.Errorf("err zero/padded = %v/%v, want 120/96", lm.MedianSeconds, lm.ErrPaddedSeconds)
	}
}

// An absent user must not be scored as a working agent: the walk stops at the
// first gap wider than the idle cutoff.
func TestLastMileStopsAtIdleGap(t *testing.T) {
	ep := reachedEpisodeWithTail(4, []int64{60, 60 + DefaultIdleCutoffSeconds + 1, 6 * 3600})
	if got := ep.TailSecondsAt(DefaultIdleCutoffSeconds); got != 60 {
		t.Fatalf("tail = %ds, want 60 (walk stops before the idle gap)", got)
	}
}

// A task that never reported all its rounds has no last mile to measure.
func TestLastMileSkipsUnreachedEpisodes(t *testing.T) {
	ep := reachedEpisodeWithTail(4, []int64{60})
	ep.Turns = ep.Turns[:2] // 0/4 and 1/4 only
	finalizeEpisode(&ep, []int64{1000, 1060}, math.MaxInt64)
	if lm := LastMile([]Episode{ep}, 0.4, DefaultIdleCutoffSeconds); lm.Episodes != 0 {
		t.Errorf("unreached episode counted: %d", lm.Episodes)
	}
}

// Corpus plumbing: the committed claudecode fixtures load into episodes without
// error (real accuracy numbers need a local corpus; see README).
func TestCorpusLoadsFixtures(t *testing.T) {
	ts := DiscoverTranscripts("../../replaydata/agents/claudecode")
	if len(ts) == 0 {
		t.Fatal("no fixture transcripts discovered")
	}
	if eps := LoadEpisodes(ts); len(eps) == 0 {
		t.Fatal("fixtures yielded no episodes")
	}
}
