package etaresearch

import "math"

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

		var prevEnd *int64
		firstFound := false
		scoredHere := false
		for i := range ep.Turns {
			pred := est.Predict(ep, i)
			if pred == nil {
				continue
			}
			predEnd := pred.Unix()
			if !firstFound {
				firstFound = true
				secsToFirst = append(secsToFirst, float64(ep.Turns[i].VirtualUnix-ep.FirstUnix()))
				firstAbs = append(firstAbs, math.Abs(float64(predEnd-ep.ActualEndUnix)))
			}
			if prevEnd != nil {
				jitter = append(jitter, math.Abs(float64(predEnd-*prevEnd)))
			}
			pe := predEnd
			prevEnd = &pe

			actualRemaining := ep.ActualEndUnix - ep.Turns[i].VirtualUnix
			if actualRemaining <= 0 {
				continue // at/after the end — not a forward prediction
			}
			err := float64(predEnd - ep.ActualEndUnix) // predicted-remaining − actual-remaining
			abs = append(abs, math.Abs(err))
			signed = append(signed, err)
			rel = append(rel, math.Abs(err)/float64(actualRemaining))
			scoredHere = true
		}
		if firstFound {
			episodesWithEstimate++
		}
		if scoredHere {
			sc.Episodes++
		}
	}

	sc.Turns = len(abs)
	sc.MAESeconds = mean(abs)
	sc.MedianAbsSeconds = median(abs)
	sc.MedianRelErr = median(rel)
	sc.BiasSeconds = mean(signed)
	if totalEpisodes > 0 {
		sc.FirstCoverage = float64(episodesWithEstimate) / float64(totalEpisodes)
	}
	sc.MeanSecsToFirst = mean(secsToFirst)
	sc.MeanFirstAbsErr = mean(firstAbs)
	sc.MeanJitter = mean(jitter)
	return sc
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
