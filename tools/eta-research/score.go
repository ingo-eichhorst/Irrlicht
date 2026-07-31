package etaresearch

import (
	"math"
	"slices"

	"irrlicht/core/domain/stats"
)

// Score aggregates one estimator's performance over a corpus of episodes. All
// times are seconds. Accuracy is measured only on turns strictly before the
// ground-truth end (actual-remaining > 0); latency and stability are per
// episode.
type Score struct {
	Estimator string

	Episodes int // episodes with at least one scored turn
	Turns    int // turns scored for accuracy

	MAESeconds       float64 // mean |predicted-remaining − actual-remaining|
	MedianAbsSeconds float64 // median of the same
	MedianRelErr     float64 // median |error| / actual-remaining
	BiasSeconds      float64 // mean signed error (+ = over-estimate)

	FirstCoverage   float64 // fraction of episodes that EVER produce an estimate
	MeanSecsToFirst float64 // mean seconds from task start to the first estimate
	MeanFirstAbsErr float64 // mean |error| of that first estimate
	MeanJitter      float64 // mean |Δ predicted end-time| between consecutive turns
}

// episodeAccum holds one episode's raw samples contributed to the aggregate
// score, before they are folded into ScoreEstimator's running totals.
type episodeAccum struct {
	abs, rel, signed           []float64
	secsToFirst, firstAbs      []float64
	jitter                     []float64
	firstFound, scoredAnything bool
}

// scoreEpisode runs the estimator over every turn of one episode and collects
// its accuracy, latency, and stability samples. Split out of ScoreEstimator
// so the per-turn bookkeeping (first-estimate tracking, jitter, forward-only
// accuracy) doesn't nest inside the per-episode loop.
func scoreEpisode(est Estimator, ep Episode) episodeAccum {
	var acc episodeAccum
	var prevEnd *int64
	for i := range ep.Turns {
		pred := est.Predict(ep, i)
		if pred == nil {
			continue
		}
		predEnd := pred.Unix()
		if !acc.firstFound {
			acc.firstFound = true
			acc.secsToFirst = append(acc.secsToFirst, float64(ep.Turns[i].VirtualUnix-ep.FirstUnix()))
			acc.firstAbs = append(acc.firstAbs, math.Abs(float64(predEnd-ep.ActualEndUnix)))
		}
		if prevEnd != nil {
			acc.jitter = append(acc.jitter, math.Abs(float64(predEnd-*prevEnd)))
		}
		pe := predEnd
		prevEnd = &pe

		actualRemaining := ep.ActualEndUnix - ep.Turns[i].VirtualUnix
		if actualRemaining <= 0 {
			continue // at/after the end — not a forward prediction
		}
		err := float64(predEnd - ep.ActualEndUnix) // predicted-remaining − actual-remaining
		acc.abs = append(acc.abs, math.Abs(err))
		acc.signed = append(acc.signed, err)
		acc.rel = append(acc.rel, math.Abs(err)/float64(actualRemaining))
		acc.scoredAnything = true
	}
	return acc
}

// ScoreEstimator runs one estimator over the episodes and aggregates the
// metrics. reachedOnly restricts accuracy to episodes that hit completed==total
// (where the last marker IS the completion, the clean accuracy subset).
func ScoreEstimator(est Estimator, eps []Episode, reachedOnly bool) Score {
	sc := Score{Estimator: est.Name}
	var abs, rel, signed []float64
	var secsToFirst, firstAbs, jitter []float64
	episodesWithEstimate := 0
	totalEpisodes := 0

	for _, ep := range eps {
		if len(ep.Turns) < 2 || ep.ActualEndUnix <= ep.FirstUnix() {
			continue
		}
		if reachedOnly && !ep.Reached {
			continue
		}
		totalEpisodes++

		acc := scoreEpisode(est, ep)
		abs = append(abs, acc.abs...)
		rel = append(rel, acc.rel...)
		signed = append(signed, acc.signed...)
		secsToFirst = append(secsToFirst, acc.secsToFirst...)
		firstAbs = append(firstAbs, acc.firstAbs...)
		jitter = append(jitter, acc.jitter...)
		if acc.firstFound {
			episodesWithEstimate++
		}
		if acc.scoredAnything {
			sc.Episodes++
		}
	}

	sc.Turns = len(abs)
	sc.MAESeconds = mean(abs)
	sc.MedianAbsSeconds, _ = stats.Median(abs)
	sc.MedianRelErr, _ = stats.Median(rel)
	sc.BiasSeconds = mean(signed)
	if totalEpisodes > 0 {
		sc.FirstCoverage = float64(episodesWithEstimate) / float64(totalEpisodes)
	}
	sc.MeanSecsToFirst = mean(secsToFirst)
	sc.MeanFirstAbsErr = mean(firstAbs)
	sc.MeanJitter = mean(jitter)
	return sc
}

// LastMileStats measures how long the agent keeps working AFTER reporting
// completed==total — failure mode #1 of issue #977, and the one thing the
// accuracy table above is structurally blind to (its ground truth IS that final
// marker, so every such turn scores an actual-remaining of 0 and is skipped).
//
// Predicting "done now" there is wrong by exactly the tail, so MedianSeconds is
// also the median error of the shipped model. ErrPaddedSeconds is what a
// wrap-up floor of WrapUpRounds × the episode's own round rate would be wrong
// by instead; #977 proposed exactly that padding, and comparing the two is how
// it was rejected. Both are medians over episodes that reached completed==total.
type LastMileStats struct {
	CutoffSeconds int64   // how long a pause still counts as "still working"
	WrapUpRounds  float64 // the padding evaluated against the hard zero

	Episodes int // reached episodes with a measurable round rate
	WithTail int // ...of which kept working after the final marker

	MedianSeconds float64
	P90Seconds    float64
	MaxSeconds    float64

	// MedianRounds and P90Rounds are the tail divided by the episode's own
	// seconds-per-round — the unit a wrap-up floor would be expressed in, and
	// the clearest view of how far apart the two halves of the distribution sit.
	MedianRounds float64
	P90Rounds    float64

	ErrPaddedSeconds float64
}

// CandidateWrapUpRounds is the wrap-up floor #977 proposed and this harness
// rejected — kept as the value the last-mile table scores, so the report shows
// what the rejected option would have cost rather than merely asserting it lost.
const CandidateWrapUpRounds = 0.4

// LastMile computes the tail distribution over episodes that reached
// completed==total, evaluating wrapUpRounds as the replacement for the old
// hard-zero. cutoff is how long a pause still counts as the agent working;
// vary it to see how much of the answer that judgment call is carrying.
// Episodes whose round rate is unmeasurable are excluded: the tail cannot be
// expressed in rounds and the forecast would not have projected there either.
func LastMile(eps []Episode, wrapUpRounds float64, cutoff int64) LastMileStats {
	st := LastMileStats{CutoffSeconds: cutoff, WrapUpRounds: wrapUpRounds}
	var secs, rounds, errPadded []float64
	for _, ep := range eps {
		if !ep.Reached || len(ep.Turns) < 2 {
			continue
		}
		rate := ep.RoundRateSeconds()
		if rate <= 0 {
			continue
		}
		st.Episodes++
		tail := float64(ep.TailSecondsAt(cutoff))
		if tail > 0 {
			st.WithTail++
		}
		secs = append(secs, tail)
		rounds = append(rounds, tail/rate)
		errPadded = append(errPadded, math.Abs(wrapUpRounds*rate-tail))
	}
	st.MedianSeconds, _ = stats.Median(secs)
	st.P90Seconds, _ = stats.Percentile(secs, 0.90)
	if len(secs) > 0 {
		st.MaxSeconds = slices.Max(secs)
	}
	st.MedianRounds, _ = stats.Median(rounds)
	st.P90Rounds, _ = stats.Percentile(rounds, 0.90)
	st.ErrPaddedSeconds, _ = stats.Median(errPadded)
	return st
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
