package etaresearch

import (
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/domain/stats"
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

// Every estimator below measures the rate with session.ObservedRoundRate and
// projects with session.ProjectCompletion — production's own functions, called
// directly rather than reimplemented (#977). The harness used to keep verbatim
// copies of both, which meant each change to the shipped model had to be
// mirrored here by hand or the research numbers would quietly describe code
// nobody runs. What distinguishes the candidates is only how they turn an
// observed rate into the rate they project with.

// Baseline is the pre-#753 production model: pure observed round-rate, nil until
// at least one round has completed. The control.
func Baseline() Estimator {
	return Estimator{Name: "baseline", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		obs, n := session.ObservedRoundRate(t.Est, t.Base, t.ElapsedSeconds, unix(t.VirtualUnix))
		if n <= 0 {
			return nil
		}
		return session.ProjectCompletion(t.Est, obs, unix(t.VirtualUnix))
	}}
}

// PriorBootstrap shows total_rounds × prior at zero rounds (the early-surfacing
// lever), then switches to the pure observed rate the moment a round completes —
// so it is byte-identical to Baseline for every measured turn and differs ONLY
// by surfacing a number sooner. That is BlendRoundRate at zero prior weight:
// all prior with no observations, all measurement with any.
func PriorBootstrap(prior float64) Estimator {
	return Estimator{Name: "prior-bootstrap", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		obs, n := session.ObservedRoundRate(t.Est, t.Base, t.ElapsedSeconds, unix(t.VirtualUnix))
		per := session.BlendRoundRate(obs, n, prior, 0)
		return session.ProjectCompletion(t.Est, per, unix(t.VirtualUnix))
	}}
}

// PriorBlend shrinks the observed rate toward the prior at every turn:
// (w·prior + n·observed)/(w+n). Prior dominates at n=0, observed takes over
// within a couple of rounds; w is the prior's strength in pseudo-rounds.
// Parameterised on (prior, w) so the sweep can vary both; production pins them
// to session.TaskRoundPriorSeconds / TaskRoundPriorWeight.
func PriorBlend(prior, w float64) Estimator {
	return Estimator{Name: "prior-blend", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		obs, n := session.ObservedRoundRate(t.Est, t.Base, t.ElapsedSeconds, unix(t.VirtualUnix))
		per := session.BlendRoundRate(obs, n, prior, w)
		return session.ProjectCompletion(t.Est, per, unix(t.VirtualUnix))
	}}
}

// Production scores the SHIPPED seam itself — session.ForecastTaskCompletion
// with production's own constants — instead of a model that merely resembles
// it. It is the row that answers "what do users actually get?", and it cannot
// drift from the daemon because it is the daemon's code.
func Production() Estimator {
	return Estimator{Name: "production", Predict: func(ep Episode, i int) *time.Time {
		t := ep.Turns[i]
		return session.ForecastTaskCompletion(t.Est, t.Base, t.ElapsedSeconds, unix(t.VirtualUnix))
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
		return session.ProjectCompletion(t.Est, rate, unix(t.VirtualUnix))
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
		if r := ep.RoundRateSeconds(); r > 0 {
			durs = append(durs, r)
		}
	}
	m, _ := stats.Median(durs)
	return m
}
