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
		eta.PriorBlend(prior, 1.0),
		eta.EWMA(prior, 0.5),
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

	in := eta.ReportInput{
		Transcripts:    len(transcripts),
		Episodes:       len(eps),
		CleanEpisodes:  clean,
		PriorSeconds:   prior,
		CorpusNote:     corpusNote,
		Scores:         scores,
		BlendSweep:     blendSweep,
		EWMASweep:      ewmaSweep,
		Recommendation: recommend(scores, blendSweep),
	}
	if err := os.WriteFile(*out, []byte(eta.RenderReport(in)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s — %d transcripts, %d episodes (%d clean), prior=%.0fs\n",
		*out, len(transcripts), len(eps), clean, prior)
}

// recommend states the shipped choice with the live numbers plugged in.
// prior-bootstrap is the call: it delivers the issue's primary goal (surface
// sooner) with a provable no-regression guarantee — for every measured turn it
// runs baseline's exact code path — and the minimal blast radius (only the
// zero-round path changes). prior-blend's better mean-MAE is reported as a
// documented, more-invasive follow-up rather than the ship.
func recommend(scores []eta.Score, blendSweep []eta.SweepPoint) string {
	by := map[string]eta.Score{}
	for _, s := range scores {
		by[s.Estimator] = s
	}
	base, boot := by["baseline"], by["prior-bootstrap"]

	bestBlend := by["prior-blend"]
	for _, p := range blendSweep {
		if p.Score.MAESeconds < bestBlend.MAESeconds {
			bestBlend = p.Score
		}
	}

	return fmt.Sprintf(
		"Ship **prior-bootstrap**.\n\n"+
			"- **Surfaces sooner (the issue's primary goal):** a number appears in %s vs "+
			"baseline's %s, at %.0f%% episode coverage vs %.0f%%.\n"+
			"- **Provably no accuracy regression:** after the first round, prior-bootstrap runs "+
			"baseline's exact code path, so its prediction is identical — the `baseline` row *is* "+
			"prior-bootstrap on measured turns (MAE %s, median %s). Its own MAE %s / median %s "+
			"additionally fold in the new zero-round turns it predicts where baseline showed nothing.\n"+
			"- **prior-blend — measured-better-MAE, not shipped:** shrinking the rate toward the "+
			"prior at every turn cuts MAE to %s (best swept) but raises the median to %s and "+
			"changes the number on *every* session, not just the zero-round gap — a larger blast "+
			"radius for a mean-only win. Left as a documented follow-up.\n"+
			"- The zero-round number is shown with a deliberately wide (≈2×) range to signal a "+
			"population prior, not a measured rate.\n",
		fmtSecs(boot.MeanSecsToFirst), fmtSecs(base.MeanSecsToFirst),
		boot.FirstCoverage*100, base.FirstCoverage*100,
		fmtSecs(base.MAESeconds), fmtSecs(base.MedianAbsSeconds),
		fmtSecs(boot.MAESeconds), fmtSecs(boot.MedianAbsSeconds),
		fmtSecs(bestBlend.MAESeconds), fmtSecs(bestBlend.MedianAbsSeconds),
	)
}

func fmtSecs(s float64) string {
	if s < 120 {
		return fmt.Sprintf("%.0fs", s)
	}
	return fmt.Sprintf("%.1fm", s/60)
}
