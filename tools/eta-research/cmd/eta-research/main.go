// Command eta-research replays recorded sessions, scores candidate ETA
// estimators, and writes the Markdown comparison report (issue #753).
//
// Usage:
//
//	go run ./cmd/eta-research \
//	    -fixtures ../../replaydata/agents/claudecode \
//	    -local "$HOME/.claude/projects" \
//	    -out ../../tools/eta-research/REPORT.md
//
// The committed corpus is the claudecode replay fixtures (always); pass -local
// (or set IRRLICHT_ETA_CORPUS) at a directory of real transcripts for the
// trustworthy accuracy numbers — those transcripts are never committed.
package main

import (
	"flag"
	"fmt"
	"os"

	"irrlicht/core/domain/session"
	eta "irrlicht/tools/eta-research"
)

func main() {
	fixtures := flag.String("fixtures", "replaydata/agents/claudecode", "committed claudecode fixtures root")
	local := flag.String("local", os.Getenv("IRRLICHT_ETA_CORPUS"), "optional real-transcripts root (default $IRRLICHT_ETA_CORPUS)")
	out := flag.String("out", "tools/eta-research/REPORT.md", "report output path")
	flag.Parse()

	transcripts := eta.DiscoverTranscripts(*fixtures)
	corpusNote := fmt.Sprintf("Fixtures only (`%s`). Pass `-local` for real-session accuracy numbers.", *fixtures)
	if *local != "" {
		localT := eta.DiscoverTranscripts(*local)
		transcripts = append(transcripts, localT...)
		// Note the count, not the absolute path — the local corpus is private.
		corpusNote = fmt.Sprintf("Fixtures (`%s`) + %d local transcripts (from `$IRRLICHT_ETA_CORPUS`, not committed).", *fixtures, len(localT))
	}
	if len(transcripts) == 0 {
		fmt.Fprintln(os.Stderr, "no marker-bearing transcripts found; check -fixtures / -local")
		os.Exit(1)
	}

	eps := eta.LoadEpisodes(transcripts)
	prior := eta.MedianPerRound(eps)
	if prior <= 0 {
		fmt.Fprintln(os.Stderr, "could not derive a per-round prior from the corpus")
		os.Exit(1)
	}

	clean := 0
	for _, ep := range eps {
		if ep.Reached && len(ep.Turns) >= 2 && ep.ActualEndUnix > ep.FirstUnix() {
			clean++
		}
	}

	candidates := []eta.Estimator{
		eta.Baseline(),
		eta.PriorBootstrap(prior),
		eta.PriorBlend(prior, session.TaskRoundPriorWeight),
		eta.EWMA(prior, 0.5),
		eta.Production(),
	}
	var scores []eta.Score
	for _, c := range candidates {
		scores = append(scores, eta.ScoreEstimator(c, eps, true))
	}

	// Bounded, deterministic sweeps (no randomness, fixed grids).
	var blendSweep []eta.SweepPoint
	for _, w := range []float64{0.5, 1.0, 2.0, 4.0} {
		blendSweep = append(blendSweep, eta.SweepPoint{
			Label: fmt.Sprintf("%.1f", w),
			Score: eta.ScoreEstimator(eta.PriorBlend(prior, w), eps, true),
		})
	}
	var ewmaSweep []eta.SweepPoint
	for _, a := range []float64{0.2, 0.5, 0.8} {
		ewmaSweep = append(ewmaSweep, eta.SweepPoint{
			Label: fmt.Sprintf("%.1f", a),
			Score: eta.ScoreEstimator(eta.EWMA(prior, a), eps, true),
		})
	}

	// Sweep the "still working?" cutoff: 3min is the UI's own stale boundary,
	// 10min and 1h are progressively more generous readings that fold in
	// longer pauses. If the tail only appears at the generous end, it is a
	// human's absence being measured, not an agent's work.
	var lastMileSweep []eta.LastMileStats
	for _, cutoff := range []int64{180, 600, 3600} {
		lastMileSweep = append(lastMileSweep, eta.LastMile(eps, eta.CandidateWrapUpRounds, cutoff))
	}

	in := eta.ReportInput{
		Transcripts:       len(transcripts),
		Episodes:          len(eps),
		CleanEpisodes:     clean,
		PriorSeconds:      prior,
		CorpusNote:        corpusNote,
		Scores:            scores,
		BlendSweep:        blendSweep,
		EWMASweep:         ewmaSweep,
		LastMileSweep:     lastMileSweep,
		ShippedPrior:      session.TaskRoundPriorSeconds,
		ShippedWeight:     session.TaskRoundPriorWeight,
		CandidateWrapUp:   eta.CandidateWrapUpRounds,
		ShippedPriorDrift: prior - session.TaskRoundPriorSeconds,
		Recommendation:    recommend(scores, blendSweep, lastMileSweep),
	}
	if err := os.WriteFile(*out, []byte(eta.RenderReport(in)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s — %d transcripts, %d episodes (%d clean), prior=%.0fs\n",
		*out, len(transcripts), len(eps), clean, prior)
}

// recommend states the shipped choice with the live numbers plugged in. #753
// shipped prior-bootstrap for early surfacing; #977 keeps that property and
// adds convergence — shrinkage toward the prior, so one unrepresentative round
// can no longer anchor the whole forecast. It also records what #977 proposed
// and this harness rejected: padding the forecast past completed==total.
func recommend(scores []eta.Score, blendSweep []eta.SweepPoint, lastMile []eta.LastMileStats) string {
	by := map[string]eta.Score{}
	for _, s := range scores {
		by[s.Estimator] = s
	}
	boot, prod := by["prior-bootstrap"], by["production"]

	bestBlend := by["prior-blend"]
	for _, p := range blendSweep {
		if p.Score.MAESeconds < bestBlend.MAESeconds {
			bestBlend = p.Score
		}
	}

	lm := eta.LastMileStats{}
	if len(lastMile) > 0 {
		lm = lastMile[0]
	}
	tailPct := 0.0
	if lm.Episodes > 0 {
		tailPct = float64(lm.WithTail) / float64(lm.Episodes) * 100
	}

	return fmt.Sprintf(
		"Ship **production** — prior-blend at `w=%.1f` over a %s prior (#977).\n\n"+
			"- **Converges instead of anchoring (#977 failure mode #2):** shrinking the observed "+
			"rate toward the prior in proportion to how few rounds back it cuts MAE from %s "+
			"(prior-bootstrap) to %s, median |err| from %s to %s, and median relative error from "+
			"%.0f%% to %.0f%%. A single unrepresentative first round now nudges the forecast "+
			"rather than setting it — which is the issue's \"increasingly accurate\" requirement.\n"+
			"- **Still surfaces immediately (#753's property, preserved):** a number appears in %s "+
			"at %.0f%% episode coverage — the zero-round path is the bare prior exactly as before.\n"+
			"- **Steadier:** jitter %s → %s between consecutive turns.\n"+
			"- **Rejected — the wrap-up floor (#977 failure mode #1).** Only %.0f%% of completed "+
			"episodes kept working past their final marker (median %s), and predicting zero there "+
			"is wrong by %s at the median versus %s for a %.2f-round pad. See the last-mile "+
			"section: the tail is bimodal, so no constant fits it. Fixed in the UIs instead, by "+
			"labelling that moment \"wrapping up\" rather than `<1m left`.\n"+
			"- **Watch the bias.** Blending trades a worse mean signed error (%s → %s) for the "+
			"better MAE/median above; the corpus is dominated by a few very long episodes, so the "+
			"median columns are the more robust read.\n"+
			"- Lowest-MAE swept blend was %s (median %s); the shipped weight trades a little mean "+
			"for the better median rather than chasing MAE alone.\n"+
			"- The zero-round number is still shown with a deliberately wide (≈2×) range to signal "+
			"a population prior, not a measured rate.\n",
		session.TaskRoundPriorWeight, fmtSecs(session.TaskRoundPriorSeconds),
		fmtSecs(boot.MAESeconds), fmtSecs(prod.MAESeconds),
		fmtSecs(boot.MedianAbsSeconds), fmtSecs(prod.MedianAbsSeconds),
		boot.MedianRelErr*100, prod.MedianRelErr*100,
		fmtSecs(prod.MeanSecsToFirst), prod.FirstCoverage*100,
		fmtSecs(boot.MeanJitter), fmtSecs(prod.MeanJitter),
		tailPct, fmtSecs(lm.MedianSeconds),
		fmtSecs(lm.ErrZeroSeconds), fmtSecs(lm.ErrPaddedSeconds), eta.CandidateWrapUpRounds,
		fmtSecs(boot.BiasSeconds), fmtSecs(prod.BiasSeconds),
		fmtSecs(bestBlend.MAESeconds), fmtSecs(bestBlend.MedianAbsSeconds),
	)
}

func fmtSecs(s float64) string {
	if s < 120 {
		return fmt.Sprintf("%.0fs", s)
	}
	return fmt.Sprintf("%.1fm", s/60)
}
