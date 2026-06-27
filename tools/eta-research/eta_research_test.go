package etaresearch

import (
	"math"
	"testing"

	"irrlicht/core/domain/session"
)

// reachedEpisode builds a finished task: rounds 0..total at 60s spacing, base
// pinned to the first marker (the production rate anchor). Ground truth is the
// last marker (rounds==total).
func reachedEpisode(total int) Episode {
	base := &session.TaskEstimate{TotalRounds: total, CompletedRounds: 0, UpdatedAt: 1000, Source: session.MarkerEstimateSource}
	var turns []Turn
	for c := 0; c <= total; c++ {
		ts := int64(1000 + c*60)
		turns = append(turns, Turn{
			VirtualUnix:    ts,
			Est:            &session.TaskEstimate{TotalRounds: total, CompletedRounds: c, UpdatedAt: ts, Source: session.MarkerEstimateSource},
			Base:           base,
			ElapsedSeconds: ts - 990,
		})
	}
	ep := Episode{Source: "synthetic", Turns: turns}
	finalizeEpisode(&ep)
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
