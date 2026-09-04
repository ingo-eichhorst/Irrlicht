// seed-autonomy-spans writes a synthetic autonomy span log (#1905) into a
// given Irrlicht data dir, so the History view's Autonomy section can be
// screenshotted with realistic data on a machine that has none.
//
// A fresh install has no spans — the log starts collecting the day the feature
// ships — so every QA pass of this section would otherwise look at the empty
// state and nothing else.
//
// It writes through the daemon's own AutonomySpanTracker rather than
// hand-rolling JSONL, so the seeded rows cannot drift from the format the
// daemon reads. That also means it obeys the same "one file per project"
// layout and the same 400-day retention.
//
// Usage:
//
//	go run ./tools/seed-autonomy-spans <data-dir> [--overwrite] [--seed N] [--days N]
//
// where <data-dir> is the daemon's data directory: $IRRLICHT_HOME for an
// isolated daemon, or ~/Library/Application Support/Irrlicht for the packaged
// app. Spans land in <data-dir>/autonomy/<project>.jsonl.
package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// autonomyDirName must match the daemon's own subdirectory (see
// initAutonomySpanStore in core/cmd/irrlichd/startup.go). Stated here rather
// than imported because it is unexported there; seedMarker below is what makes
// a mismatch loud instead of silent — a wrong directory produces a marker in a
// place the daemon never reads, and the section stays empty.
const autonomyDirName = "autonomy"

// seedMarker records that THIS tool created the span directory. Its absence in
// an existing directory means the directory is someone else's — a real
// daemon's, most likely — and seeding into it would silently mix synthetic
// runs into a user's real history.
const seedMarker = ".seeded-by-seed-autonomy-spans"

// The synthetic corpus' shape. Tuned so that every window both elements offer
// renders populated:
//
//   - the strip's 8h window needs runs in the last eight hours (recentBurst);
//   - the duration chart's 30d range needs DAILY buckets on both sides of the
//     20-span floor, so QA sees the thin rendering AND the normal one;
//   - its 1y range needs WEEKLY buckets that also straddle that floor, which
//     is why the older days are sparse-but-not-empty rather than empty: at
//     1-6 runs a day a week lands around 25, on both sides of 20.
const (
	recentDays        = 30
	recentSpansMin    = 6
	recentSpansMax    = 34
	olderSpansMin     = 1
	olderSpansMax     = 6
	recentBurst       = 6 // runs forced into the last eight hours
	defaultTotalDays  = 400
	medianDurationSec = 480.0 // p50 ≈ 8 minutes
	durationSigma     = 1.25  // log-normal spread: a long tail into hours
	maxDurationSec    = 6 * 3600
)

// seedProject is one synthetic project's identity. Four of them, because the
// strip draws one row per project and a single-row strip proves nothing about
// row ordering or the busiest-first rule.
type seedProject struct {
	name    string
	adapter string
	model   string
	weight  int // relative share of runs
}

var seedProjects = []seedProject{
	{"irrlicht", "claude-code", "claude-opus-4", 5},
	{"articles", "codex", "gpt-5", 3},
	{"irrlicht-1719", "claude-code", "claude-sonnet-4", 2},
	{"dotfiles", "opencode", "qwen3-coder", 1},
}

// reasonWeights is the end-reason mix. Deliberately not uniform: most runs
// finish their turn, a good share stop to ask, and errors are the minority —
// a strip where a third of the columns are red would misrepresent what the
// colors mean.
var reasonWeights = []struct {
	reason string
	weight int
}{
	{session.StateReady, 55},
	{session.StateWaiting, 35},
	{session.StateError, 10},
}

func main() {
	overwrite := flag.Bool("overwrite", false, "replace an existing span log (refused by default)")
	seed := flag.Int64("seed", 1905, "PRNG seed; the same seed produces the same corpus")
	days := flag.Int("days", defaultTotalDays, "how far back to spread runs, in days")
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	dataDir := flag.Arg(0)
	spanDir := filepath.Join(dataDir, autonomyDirName)

	if err := guardSpanDir(spanDir, *overwrite); err != nil {
		fmt.Fprintln(os.Stderr, "refusing to seed:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(spanDir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create", spanDir+":", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(spanDir, seedMarker),
		[]byte(time.Now().Format(time.RFC3339)+"\n"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write the seed marker:", err)
		os.Exit(1)
	}

	tracker := filesystem.NewAutonomySpanTrackerWithDir(spanDir)
	spans := generateSpans(rand.New(rand.NewSource(*seed)), time.Now(), *days)
	for _, s := range spans {
		if err := tracker.RecordSpan(s); err != nil {
			fmt.Fprintln(os.Stderr, "write failed:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("seeded %d autonomy spans across %d projects into %s\n",
		len(spans), len(seedProjects), spanDir)
	fmt.Println("restart the daemon (or just reload the dashboard) and open History → Autonomy")
}

func usage() {
	fmt.Fprintln(os.Stderr,
		"seed-autonomy-spans <data-dir> [--overwrite] [--seed N] [--days N] — fill <data-dir>/autonomy "+
			"with a few hundred synthetic runs so History → Autonomy renders populated in every range and span.")
	flag.PrintDefaults()
}

// guardSpanDir refuses to write into a span directory this tool did not create
// or that already holds spans, unless the caller asked for it explicitly.
//
// The point is not tidiness: <data-dir> is normally a live daemon's, and
// synthetic runs mixed into a real span log are indistinguishable from real
// ones afterwards — there is no undo, and the charts would be quietly wrong
// from then on.
func guardSpanDir(spanDir string, overwrite bool) error {
	entries, err := os.ReadDir(spanDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // we will create it
		}
		return err
	}
	var spanFiles []string
	seeded := false
	for _, e := range entries {
		if e.Name() == seedMarker {
			seeded = true
			continue
		}
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			spanFiles = append(spanFiles, e.Name())
		}
	}
	if overwrite {
		for _, name := range spanFiles {
			if err := os.Remove(filepath.Join(spanDir, name)); err != nil {
				return err
			}
		}
		return nil
	}
	if !seeded {
		return fmt.Errorf("%s exists and was not created by this tool — it looks like a real span log; "+
			"pass --overwrite only if you are sure you want to replace it", spanDir)
	}
	if len(spanFiles) > 0 {
		return fmt.Errorf("%s already holds %d span file(s); pass --overwrite to replace them",
			spanDir, len(spanFiles))
	}
	return nil
}

// generateSpans builds the synthetic corpus, newest day first, using rng so a
// given --seed always produces the same picture (a screenshot that changes
// every run is not evidence of anything).
func generateSpans(rng *rand.Rand, now time.Time, days int) []outbound.AutonomySpan {
	var out []outbound.AutonomySpan
	for day := 0; day < days; day++ {
		n := spansForDay(rng, day)
		dayEnd := now.Add(-time.Duration(day) * 24 * time.Hour)
		for i := 0; i < n; i++ {
			// Spread across the day, biased towards working hours only in the
			// crude sense that runs cluster rather than tile evenly.
			offset := time.Duration(rng.Float64()*24*3600) * time.Second
			out = append(out, spanEndingAt(rng, dayEnd.Add(-offset)))
		}
	}
	// The strip's shortest window is 8 hours; without runs inside it, the
	// default view of element 2 is the empty state, which is precisely the
	// thing a seeded screenshot is supposed to get past.
	for i := 0; i < recentBurst; i++ {
		end := now.Add(-time.Duration(rng.Intn(7*3600+1)) * time.Second)
		out = append(out, spanEndingAt(rng, end))
	}
	return out
}

// spansForDay picks how many runs a given day back from now holds: dense over
// the last month (straddling the 20-span sample floor, so both the thin and
// the normal bucket rendering appear), sparse before it.
func spansForDay(rng *rand.Rand, dayBack int) int {
	if dayBack < recentDays {
		return recentSpansMin + rng.Intn(recentSpansMax-recentSpansMin+1)
	}
	return olderSpansMin + rng.Intn(olderSpansMax-olderSpansMin+1)
}

// spanEndingAt builds one run ending at end, with a log-normal duration — the
// long-tailed shape real runs have, where the median is minutes and the top
// few per cent are hours. A uniform duration would make p95, p50 and p5 sit
// evenly apart and hide exactly what the three lines exist to show.
func spanEndingAt(rng *rand.Rand, end time.Time) outbound.AutonomySpan {
	d := math.Exp(math.Log(medianDurationSec) + rng.NormFloat64()*durationSigma)
	if d < 5 {
		d = 5
	}
	if d > maxDurationSec {
		d = maxDurationSec
	}
	seconds := int64(d)
	p := pickProject(rng)
	return outbound.AutonomySpan{
		Start:   end.Unix() - seconds,
		End:     end.Unix(),
		Project: p.name,
		Session: fmt.Sprintf("seed-%d", rng.Int63n(1_000_000)),
		Adapter: p.adapter,
		Model:   p.model,
		Reason:  pickReason(rng),
	}
}

func pickProject(rng *rand.Rand) seedProject {
	total := 0
	for _, p := range seedProjects {
		total += p.weight
	}
	n := rng.Intn(total)
	for _, p := range seedProjects {
		if n < p.weight {
			return p
		}
		n -= p.weight
	}
	return seedProjects[0]
}

func pickReason(rng *rand.Rand) string {
	total := 0
	for _, r := range reasonWeights {
		total += r.weight
	}
	n := rng.Intn(total)
	for _, r := range reasonWeights {
		if n < r.weight {
			return r.reason
		}
		n -= r.weight
	}
	return session.StateReady
}
