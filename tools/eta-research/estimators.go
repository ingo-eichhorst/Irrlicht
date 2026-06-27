package etaresearch

import (
	"sort"
	"time"

	"irrlicht/core/domain/session"
)

// Estimator is one candidate ETA model. Predict returns the projected
// completion time given the episode's turns up to and including index i, or nil
// when it cannot project yet. The episode is passed in full so stateful
// candidates (EWMA) can see the marker history; stateless ones (baseline,
// prior) use only turn i — the same inputs the production seam gets.
type Estimator struct {
	Name    string
	Predict func(ep Episode, i int) *time.Time
}

// observedRoundRate replicates the PRODUCTION rate measurement verbatim:
// within-task marker delta preferred (immune to previous tasks + idle gaps),
// whole-session elapsed as the single-marker fallback. Returns (seconds-per-
// round, observationCount); count 0 means no rate could be measured yet.
func observedRoundRate(est, base *session.TaskEstimate, elapsed int64, now time.Time) (float64, int) {
	if est == nil {
		return 0, 0
	}
	switch {
	case base != nil && est.CompletedRounds > base.CompletedRounds && est.UpdatedAt > base.UpdatedAt:
		n := est.CompletedRounds - base.CompletedRounds
		return float64(est.UpdatedAt-base.UpdatedAt) / float64(n), n
	case elapsed > 0 && est.CompletedRounds > 0:
		elapsedAtMarker := elapsed
		if est.UpdatedAt > 0 {
			if gap := now.Unix() - est.UpdatedAt; gap > 0 && gap < elapsed {
				elapsedAtMarker = elapsed - gap
			}
		}
		return float64(elapsedAtMarker) / float64(est.CompletedRounds), est.CompletedRounds
	}
	return 0, 0
}

// project anchors the ETA at the marker (UpdatedAt) so it is stable between
// markers, mirroring production. nil when there is nothing to project.
func project(est *session.TaskEstimate, perRound float64, now time.Time) *time.Time {
	if est == nil || perRound <= 0 {
		return nil
	}
	remaining := max(est.TotalRounds-est.CompletedRounds, 0)
	anchor := now
	if est.UpdatedAt > 0 {
		anchor = time.Unix(est.UpdatedAt, 0)
	}
	eta := anchor.Add(time.Duration(perRound * float64(remaining) * float64(time.Second)))
	return &eta
}

// Baseline is the current production model (pre-#753): pure observed round-rate,
// nil until at least one round has completed. The control.
func Baseline() Estimator {
	return Estimator{Name: "baseline", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		obs, n := observedRoundRate(t.Est, t.Base, t.ElapsedSeconds, unix(t.VirtualUnix))
		if n <= 0 || obs <= 0 {
			return nil
		}
		return project(t.Est, obs, unix(t.VirtualUnix))
	}}
}

// PriorBootstrap shows total_rounds × prior at zero rounds (the early-surfacing
// lever), then switches to the pure observed rate the moment a round completes —
// so it is byte-identical to Baseline for every measured turn and differs ONLY
// by surfacing a number sooner.
func PriorBootstrap(prior float64) Estimator {
	return Estimator{Name: "prior-bootstrap", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		obs, n := observedRoundRate(t.Est, t.Base, t.ElapsedSeconds, unix(t.VirtualUnix))
		per := obs
		if n <= 0 || obs <= 0 {
			per = prior
		}
		return project(t.Est, per, unix(t.VirtualUnix))
	}}
}

// PriorBlend shrinks the observed rate toward the prior at every turn:
// (w·prior + n·observed)/(w+n). Prior dominates at n=0, observed takes over
// within a couple of rounds; w is the prior's strength in pseudo-rounds.
func PriorBlend(prior, w float64) Estimator {
	return Estimator{Name: "prior-blend", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		obs, n := observedRoundRate(t.Est, t.Base, t.ElapsedSeconds, unix(t.VirtualUnix))
		per := prior
		if n > 0 && obs > 0 {
			per = (w*prior + float64(n)*obs) / (w + float64(n))
		}
		return project(t.Est, per, unix(t.VirtualUnix))
	}}
}

// EWMA smooths the per-round duration over consecutive markers (alpha = weight
// on the latest delta), seeded by the prior before any delta is seen. Stateful:
// reads the marker history turns[0..i]. Shippable only by widening the
// production seam to carry history — noted in the report.
func EWMA(prior, alpha float64) Estimator {
	return Estimator{Name: "ewma", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		rate, seen := prior, false
		for k := 1; k <= i; k++ {
			dr := ep.Turns[k].Est.CompletedRounds - ep.Turns[k-1].Est.CompletedRounds
			dt := ep.Turns[k].VirtualUnix - ep.Turns[k-1].VirtualUnix
			if dr <= 0 || dt <= 0 {
				continue
			}
			inst := float64(dt) / float64(dr)
			if !seen {
				rate, seen = inst, true
			} else {
				rate = alpha*inst + (1-alpha)*rate
			}
		}
		return project(t.Est, rate, unix(t.VirtualUnix))
	}}
}

// MedianPerRound is the corpus prior: the median PER-EPISODE average round
// duration — each episode's marker span divided by the rounds it advanced
// ((lastTime−firstTime)/(lastCompleted−firstCompleted)). Per episode, not per
// consecutive delta: markers are emitted in bursts (completed_rounds bumping a
// few seconds apart within one round), so a per-delta median collapses to the
// emission cadence (~4s) rather than a true round (~72s). Returns 0 for an
// empty corpus.
func MedianPerRound(eps []Episode) float64 {
	var durs []float64
	for _, ep := range eps {
		first, last := ep.Turns[0], ep.Turns[len(ep.Turns)-1]
		dr := last.Est.CompletedRounds - first.Est.CompletedRounds
		dt := last.VirtualUnix - first.VirtualUnix
		if dr > 0 && dt > 0 {
			durs = append(durs, float64(dt)/float64(dr))
		}
	}
	return median(durs)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
