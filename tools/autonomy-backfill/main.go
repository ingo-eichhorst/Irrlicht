// autonomy-backfill reconstructs Autonomy spans (#1905) for the era before
// the feature shipped, from logs that were already on disk, and MARKS every
// row it writes as reconstructed rather than measured.
//
// LOCAL, MANUAL, ONE-OFF. It is not wired into daemon startup, migrations or
// first-run logic, and it never will be — TestBackfillIsNotWiredIntoTheDaemon
// fails the build if it ever is. Every other machine's Autonomy view correctly
// starts empty on the day it upgrades, and the shipped empty state already
// says so in words. This exists for one maintainer's machine, which happens to
// carry two logs that reach back months further than the feature does.
//
// Two sources, in descending order of what they can honestly claim:
//
//	logs/events.log*  — the daemon's own `session-detector` entries carry the
//	                    REAL transition, so a span from here has a real end
//	                    reason. Marked source=log.
//	cost/*.jsonl      — records WHEN a session was consuming tokens and never
//	                    why a run stopped. Marked source=cost, and its end
//	                    reason is `unknown`, always.
//
// Usage:
//
//	go run ./tools/autonomy-backfill <data-dir> [--apply] [--gap-seconds N]
//	                                            [--since YYYY-MM-DD] [--force]
//
// It DRY RUNS by default: without --apply it reads, reports and writes
// nothing. <data-dir> is the daemon's data directory — $IRRLICHT_HOME for an
// isolated daemon, or ~/Library/Application Support/Irrlicht for the packaged
// app. Spans land in <data-dir>/autonomy/<project>.jsonl, appended alongside
// whatever the daemon has measured live.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/stats"
	"irrlicht/core/ports/outbound"
)

// autonomyDirName must match the daemon's own subdirectory (see
// initAutonomySpanStore in core/cmd/irrlichd/startup.go). Stated here rather
// than imported because it is unexported there — the same note
// tools/seed-autonomy-spans carries, and the same reason it is safe: writing
// to the wrong directory produces a section that stays empty, not a section
// that lies.
const autonomyDirName = "autonomy"

// malformedLimit is the share of unreadable lines above which --apply is
// refused.
//
// AGENTS.md: a validator that cannot parse its input checks MORE, never less.
// A source this tool has stopped being able to read produces FEWER spans, and
// fewer spans is indistinguishable from a quieter month — so past this point
// it stops and says so instead of quietly reconstructing a fraction of the
// history. --allow-malformed is the deliberate override, and it prints what it
// is overriding.
const malformedLimit = 0.01

type options struct {
	dataDir        string
	apply          bool
	gapSeconds     int64
	since          int64
	force          bool
	allowMalformed bool
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "autonomy-backfill:", err)
		os.Exit(2)
	}
	if err := run(os.Stdout, opts); err != nil {
		fmt.Fprintln(os.Stderr, "autonomy-backfill:", err)
		os.Exit(1)
	}
}

func parseFlags(argv []string) (options, error) {
	fs := flag.NewFlagSet("autonomy-backfill", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the reconstructed spans (default: dry run, writes nothing)")
	gap := fs.Int64("gap-seconds", costGapSeconds, "cost-log quiet period that ends an active stretch")
	since := fs.String("since", "", "drop reconstructed spans starting before this date (YYYY-MM-DD)")
	force := fs.Bool("force", false, "write even though the span log already holds reconstructed rows")
	allowMalformed := fs.Bool("allow-malformed", false,
		fmt.Sprintf("write even though a source's unreadable share exceeds %.1f%%", malformedLimit*100))
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "autonomy-backfill <data-dir> [--apply] [--gap-seconds N] [--since YYYY-MM-DD] [--force]")
		fmt.Fprintln(fs.Output(), "  Reconstruct pre-#1905 Autonomy spans from this machine's own logs, MARKED as reconstructed.")
		fmt.Fprintln(fs.Output(), "  Dry runs by default; --apply is what writes.")
		fs.PrintDefaults()
	}
	positionals, err := parseInterleaved(fs, argv)
	if err != nil {
		return options{}, err
	}
	if len(positionals) != 1 {
		fs.Usage()
		return options{}, fmt.Errorf("expected exactly one <data-dir>, got %d", len(positionals))
	}
	if *gap <= 0 {
		return options{}, fmt.Errorf("--gap-seconds must be positive, got %d", *gap)
	}
	opts := options{
		dataDir:        positionals[0],
		apply:          *apply,
		gapSeconds:     *gap,
		force:          *force,
		allowMalformed: *allowMalformed,
	}
	if *since != "" {
		t, err := time.ParseInLocation("2006-01-02", *since, time.Local)
		if err != nil {
			return options{}, fmt.Errorf("--since must be YYYY-MM-DD: %w", err)
		}
		opts.since = t.Unix()
	}
	return opts, nil
}

// parseInterleaved parses argv, allowing flags to appear AFTER the positional
// argument, and returns the positionals it lifted out.
//
// Go's flag package stops at the first non-flag argument, so plain
// `fs.Parse(argv)` reads `<data-dir> --apply` as TWO positionals and silently
// ignores --apply — a dry run where the caller asked for a write, reported as
// a usage error at best and as "nothing happened" at worst. The documented
// usage puts the directory first because that is how anyone would type it, so
// the parser has to accept it rather than the docs bending to the parser.
//
// Re-Parse on the same FlagSet is safe: flags already set keep their values.
func parseInterleaved(fs *flag.FlagSet, argv []string) ([]string, error) {
	var positionals []string
	rest := argv
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		args := fs.Args()
		if len(args) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, args[0])
		rest = args[1:]
	}
}

// result is one complete reconstruction, ready to report or write.
type result struct {
	LogSpans  []outbound.AutonomySpan
	CostSpans []outbound.AutonomySpan
	LogLoss   lossReport
	CostLoss  lossReport
	EventLog  *eventLog
	CostLog   *costLog
	Boundary  int64
	// LiveFloor is the earliest span the daemon has ALREADY measured (0 = it
	// has measured none). Nothing is reconstructed at or after it.
	LiveFloor int64
}

// All returns every reconstructed span, ordered.
func (r result) All() []outbound.AutonomySpan {
	out := make([]outbound.AutonomySpan, 0, len(r.LogSpans)+len(r.CostSpans))
	out = append(out, r.LogSpans...)
	out = append(out, r.CostSpans...)
	sortSpans(out)
	return out
}

func run(out *os.File, opts options) error {
	evt, err := readEventLog(opts.dataDir)
	if err != nil {
		return err
	}
	cost, err := readCostLog(opts.dataDir)
	if err != nil {
		return err
	}
	// Read the live floor BEFORE reconstructing, so the dry run reports exactly
	// what --apply would write. A dry run whose numbers differ from the write
	// is not a preview of anything.
	liveFloor, err := readLiveFloor(opts.dataDir)
	if err != nil {
		return err
	}
	res := reconstruct(evt, cost, opts, liveFloor, time.Now().Unix())

	report(out, res, opts)
	printSensitivity(out, cost, res.Boundary, opts.since)

	if !opts.apply {
		fmt.Fprintf(out, "\nDRY RUN — nothing was written. Re-run with --apply to write %d spans into %s.\n",
			len(res.All()), filepath.Join(opts.dataDir, autonomyDirName))
		return nil
	}
	if err := guardMalformed(res, opts); err != nil {
		return err
	}
	return apply(out, res, opts)
}

// readLiveFloor returns the earliest span the daemon has already MEASURED in
// this data dir, or 0 when it has measured none.
//
// It reads through the daemon's own store, so "measured" means exactly what
// the daemon and both clients mean by it — a row with no source.
func readLiveFloor(dataDir string) (int64, error) {
	store := filesystem.NewAutonomySpanTrackerWithDir(filepath.Join(dataDir, autonomyDirName))
	res, err := store.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		return 0, fmt.Errorf("read the existing span log: %w", err)
	}
	if res == nil {
		return 0, nil
	}
	return res.Provenance.LiveSince, nil
}

// reconstruct runs both sources against one set of options.
func reconstruct(evt *eventLog, cost *costLog, opts options, liveFloor, now int64) result {
	boundary := earliestTransition(evt)
	logSpans, logLoss := reconstructEventSpans(evt, sessionProjects(cost), now)
	if opts.since > 0 {
		logSpans = dropBefore(logSpans, opts.since)
	}
	costSpans, costLoss := reconstructCostSpans(cost, opts.gapSeconds, boundary, opts.since)
	// The event log keeps being written after the feature ships, so its era
	// overlaps the live span log's. Whatever the daemon measured wins.
	logSpans, logOverlap := dropOverlappingLive(logSpans, liveFloor)
	costSpans, costOverlap := dropOverlappingLive(costSpans, liveFloor)
	logLoss.OverlapsLive = logOverlap
	costLoss.OverlapsLive = costOverlap
	return result{
		LogSpans:  logSpans,
		CostSpans: costSpans,
		LogLoss:   logLoss,
		CostLoss:  costLoss,
		EventLog:  evt,
		CostLog:   cost,
		Boundary:  boundary,
		LiveFloor: liveFloor,
	}
}

// dropBefore applies an explicit --since floor.
func dropBefore(spans []outbound.AutonomySpan, since int64) []outbound.AutonomySpan {
	out := spans[:0:0]
	for _, s := range spans {
		if s.Start >= since {
			out = append(out, s)
		}
	}
	return out
}

// guardMalformed refuses to write when a source has become unreadable.
func guardMalformed(res result, opts options) error {
	for name, st := range map[string]parseStats{"event log": res.EventLog.Stats, "cost log": res.CostLog.Stats} {
		if st.MalformedShare() <= malformedLimit {
			continue
		}
		if opts.allowMalformed {
			fmt.Fprintf(os.Stderr,
				"autonomy-backfill: WARNING — the %s is %.2f%% unreadable and --allow-malformed was passed; "+
					"the reconstruction below covers only what could be parsed.\n", name, st.MalformedShare()*100)
			continue
		}
		return fmt.Errorf("refusing to write: the %s is %.2f%% unreadable (limit %.1f%%) — %s. "+
			"Fewer spans and an unreadable source look identical in the result, so this stops instead. "+
			"Pass --allow-malformed to write anyway",
			name, st.MalformedShare()*100, malformedLimit*100, st)
	}
	return nil
}

// apply writes the reconstruction through the daemon's own span tracker, so
// the rows cannot drift from the format the daemon reads.
func apply(out *os.File, res result, opts options) error {
	spanDir := filepath.Join(opts.dataDir, autonomyDirName)
	tracker := filesystem.NewAutonomySpanTrackerWithDir(spanDir)
	if err := guardAlreadyBackfilled(tracker, opts.force); err != nil {
		return err
	}
	written := 0
	for _, s := range res.All() {
		if err := tracker.RecordSpan(s); err != nil {
			return fmt.Errorf("write span: %w", err)
		}
		written++
	}
	fmt.Fprintf(out, "\nWROTE %d reconstructed spans into %s.\n", written, spanDir)
	fmt.Fprintln(out, "Every row carries a `source`; the daemon's own rows do not, and none of them were touched.")
	fmt.Fprintln(out, "Reload the dashboard (or restart the app) and open History → Autonomy.")
	return nil
}

// guardAlreadyBackfilled refuses a second --apply into a span log that already
// holds reconstructed rows.
//
// There is no undo. Appending the same reconstruction twice doubles every
// figure in the section and is indistinguishable afterwards from a machine
// that genuinely ran twice as much — the same trap tools/seed-autonomy-spans
// guards, for the same reason.
func guardAlreadyBackfilled(store outbound.AutonomySpanStore, force bool) error {
	res, err := store.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		return fmt.Errorf("read the existing span log: %w", err)
	}
	if res == nil || res.Provenance.Reconstructed == 0 {
		return nil
	}
	if force {
		fmt.Fprintf(os.Stderr, "autonomy-backfill: WARNING — appending to a log that already holds %d "+
			"reconstructed span(s); --force was passed.\n", res.Provenance.Reconstructed)
		return nil
	}
	return fmt.Errorf("refusing to write: the span log already holds %d reconstructed span(s), "+
		"and appending a second reconstruction would double every figure in the section with no way "+
		"to tell afterwards. Delete the reconstructed rows first, or pass --force",
		res.Provenance.Reconstructed)
}

// report prints the reconstruction census: what each source produced, over
// what period, and everything that was deliberately dropped.
func report(out *os.File, res result, opts options) {
	fmt.Fprintln(out, "SOURCES")
	fmt.Fprintf(out, "  event log : %s\n", res.EventLog.Stats)
	fmt.Fprintf(out, "  cost log  : %s\n", res.CostLog.Stats)
	fmt.Fprintf(out, "  boundary  : %s (the event log's first transition; the cost era stops there)\n",
		stampOrNever(res.Boundary))
	fmt.Fprintf(out, "  live from : %s (the earliest span the daemon MEASURED; nothing is reconstructed at or after it)\n",
		stampOrNever(res.LiveFloor))
	fmt.Fprintf(out, "  gap       : %ds (cost-log quiet period that ends a stretch), grace %ds\n\n",
		opts.gapSeconds, graceSeconds)

	fmt.Fprintln(out, "RECONSTRUCTED")
	reportSource(out, session.AutonomySourceLog, res.LogSpans, "real end reason, read back from the logged transition")
	reportSource(out, session.AutonomySourceCost, res.CostSpans,
		"end reason `"+session.AutonomyReasonUnknown+"` — the source records activity, never outcome")
	fmt.Fprintf(out, "  %-6s %6d spans\n\n", "TOTAL", len(res.All()))

	fmt.Fprintln(out, "DELIBERATELY DROPPED (an undercount, and the safe direction)")
	fmt.Fprintf(out, "  %6d  daemon restarts in the event log (clustered from its `startup` entries)\n", res.LogLoss.Restarts)
	fmt.Fprintf(out, "  %6d  log spans straddling a restart — several runs merged, dropped not split\n", res.LogLoss.RestartStraddlers)
	fmt.Fprintf(out, "  %6d  closes with no matching open (the open half predates the retained log)\n", res.LogLoss.OrphanCloses)
	fmt.Fprintf(out, "  %6d  `ready` with no open span (provisional; emits nothing either way)\n", res.LogLoss.OrphanReady)
	fmt.Fprintf(out, "  %6d  `→ working` while already working (the live detector replaces the start too)\n", res.LogLoss.ReopenedWhileOpen)
	fmt.Fprintf(out, "  %6d  log spans still open when the log ends (no measured end; none invented)\n", res.LogLoss.UnclosedAtEnd)
	fmt.Fprintf(out, "  %6d  log spans with no project to file them under\n", res.LogLoss.NoProject)
	fmt.Fprintf(out, "  %6d  spans reaching into the era the daemon already MEASURED (never counted twice)\n",
		res.LogLoss.OverlapsLive+res.CostLoss.OverlapsLive)
	fmt.Fprintf(out, "  %6d  cost stretches at or after the boundary (the event log speaks for that era)\n", res.CostLoss.BoundaryStraddlers)
	fmt.Fprintf(out, "  %6d  cost stretches of a single row — an instant, with no duration to measure\n", res.CostLoss.NonPositive)
	fmt.Fprintf(out, "  %6d  log spans whose end did not follow their start\n", res.LogLoss.NonPositive)
}

// reportSource prints one source's line: count, period, and reason mix.
func reportSource(out *os.File, source string, spans []outbound.AutonomySpan, note string) {
	if len(spans) == 0 {
		fmt.Fprintf(out, "  %-6s %6d spans — %s\n", source, 0, note)
		return
	}
	last := spans[0].End
	for _, s := range spans {
		if s.End > last {
			last = s.End
		}
	}
	fmt.Fprintf(out, "  %-6s %6d spans  %s → %s  [%s]\n", source, len(spans),
		stampOrNever(spans[0].Start), stampOrNever(last), reasonMix(spans))
	fmt.Fprintf(out, "         %s\n", note)
}

// reasonMix renders a source's end-reason breakdown, ordered for stability.
func reasonMix(spans []outbound.AutonomySpan) string {
	counts := map[string]int{}
	for _, s := range spans {
		counts[s.Reason]++
	}
	reasons := make([]string, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	line := ""
	for i, r := range reasons {
		if i > 0 {
			line += " "
		}
		line += fmt.Sprintf("%s %d", r, counts[r])
	}
	return line
}

// printSensitivity prints what the gap threshold costs: span count and the
// p50/p95 it yields, at 120 s, the value in force, and 300 s.
//
// It exists because the threshold is the one number in this tool that is a
// judgement call rather than a measurement, and a judgement call whose effect
// is not shown is indistinguishable from an arbitrary one.
func printSensitivity(out *os.File, cost *costLog, boundary, since int64) {
	fmt.Fprintln(out, "\nCOST-LOG GAP SENSITIVITY (only source=cost spans are affected)")
	fmt.Fprintf(out, "  %-10s %8s %10s %10s\n", "gap", "spans", "p50", "p95")
	for _, gap := range sensitivityThresholds {
		spans, _ := reconstructCostSpans(cost, gap, boundary, since)
		d := make([]float64, 0, len(spans))
		for _, s := range spans {
			d = append(d, float64(s.Duration()))
		}
		sort.Float64s(d)
		mark := ""
		if gap == costGapSeconds {
			mark = "  ← in force"
		}
		fmt.Fprintf(out, "  %-10s %8d %10s %10s%s\n", fmt.Sprintf("%ds", gap), len(spans),
			humanSeconds(stats.PercentileSorted(d, 0.50)), humanSeconds(stats.PercentileSorted(d, 0.95)), mark)
	}
}

// humanSeconds renders a duration the way both clients' AutonomyFormat does,
// so a figure printed here can be compared with one on screen.
func humanSeconds(seconds float64) string {
	s := int(math.Round(seconds))
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	default:
		return fmt.Sprintf("%dd%02dh", s/86400, (s%86400)/3600)
	}
}

// stampOrNever formats a unix second, or says plainly that there isn't one.
func stampOrNever(ts int64) string {
	if ts <= 0 {
		return "«none»"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}
