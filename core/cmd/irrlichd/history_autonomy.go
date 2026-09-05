package main

import (
	"net/http"
	"sort"
	"time"

	"irrlicht/core/ports/outbound"
)

// Autonomy (#1905) — the History view's own top-level section: FIVE PER-PROJECT
// PANELS, one per project, stacked, each carrying the same two things.
//
//	A LINE — the longest run in each time bucket. Only `working` matters here;
//	how a run ended is recorded but no longer drawn.
//	A HISTOGRAM under it — how many runs were working AT THE SAME TIME in that
//	bucket, derived from the span log by overlap.
//
// WHAT THIS REPLACED, and why the replacement is smaller rather than richer:
//
//   - The p5–p95 band and the p50 line are gone. The section reports the
//     LONGEST run now, so a percentile envelope has nothing left to describe.
//   - The per-project run strip, its end-reason colours and its legend are
//     gone with them: the strip's whole subject was how a run ENDED.
//   - The sample floor and the thin-bucket marking went with the percentiles.
//     They existed because a p95 over four samples is not a percentile — it is
//     that bucket's maximum wearing a percentile's name. A MAXIMUM over one run
//     is simply that run, so there is nothing left for a floor to protect, and
//     carrying it over as decoration would mark buckets whose figure is exact.
//
// `Reason` stays on the wire and in the store. Nothing renders it today; it
// costs one string per row and it is the one field that cannot be recovered
// after the fact, so it keeps being recorded.
//
// Everything is served from the always-on span log (outbound.AutonomySpanStore),
// never from the opt-in lifecycle recordings that back chart=agents/state — the
// concurrency figure included. That is not a preference: on a machine with no
// `recordings` directory the lifecycle-derived Agents chart is blank while the
// span log is complete, so a concurrency number taken from recordings would be
// missing exactly where this one is not.
const chartAutonomyProjects = "autonomy_projects"

// autonomyPanelCount is how many project panels the section draws.
//
// FIVE, and the number is the design rather than a cap bolted onto an unbounded
// list: the section is a per-project view of the five most important projects,
// so the daemon computes five panels and says how many projects it left out.
// Both clients draw what they are sent, which is why they cannot disagree about
// how many panels a stack has (the run strip they replaced shipped twelve rows
// on the web against six on macOS).
//
// "MOST IMPORTANT" IS GREATEST TOTAL AUTONOMOUS TIME IN THE WINDOW, never
// longest single run: one lucky overnight run would otherwise promote a project
// nobody has touched in a month over the one that has been working all week.
// The rank is stated on the wire (historyAutonomyPanel.TotalSeconds) so it can
// be checked rather than trusted.
const autonomyPanelCount = 5

// autonomyWindowSpec pairs one Range's bucket width with its bucket count.
//
// SEPARATE FROM historyGranularitySpecs ON PURPOSE, and the separation is
// load-bearing (#1905). The keys look alike and mean opposite things: there, a
// key names a BUCKET WIDTH which is then multiplied by a count, so "24h"
// resolves to a THIRTY-DAY window. Here a key names the WINDOW ITSELF. Merging
// the two tables in a later refactor would silently redefine what a user's
// "24h" means; TestAutonomyWindows_AreNotHistoryGranularities is the tripwire.
type autonomyWindowSpec struct {
	bucketSeconds int64
	buckets       int64
}

// autonomyWindowSpecs is the section's Range vocabulary. Two ranges — the
// section's only control now that the run strip's Span has gone with the strip.
var autonomyWindowSpecs = map[string]autonomyWindowSpec{
	"30d": {86400, 30},     // 30 daily buckets
	"1y":  {7 * 86400, 52}, // 52 weekly buckets
}

// isAutonomyChart reports whether a ?chart= value is the Autonomy section's,
// which resolves its window from ?window= instead of the usual ?range=/?bucket=
// pair.
func isAutonomyChart(chart string) bool { return chart == chartAutonomyProjects }

// autonomyDefaultWindow is the ?window= value assumed when the client sends
// none.
func autonomyDefaultWindow() string { return "30d" }

// resolveAutonomyWindow resolves ?window= into a trailing [start, end) window
// plus its bucket width. ok is false for a window the section does not offer.
func resolveAutonomyWindow(window string) (bucketSeconds, start, end int64, ok bool) {
	spec, known := autonomyWindowSpecs[window]
	if !known {
		return 0, 0, 0, false
	}
	end = time.Now().Unix()
	return spec.bucketSeconds, end - spec.bucketSeconds*spec.buckets, end, true
}

// autonomyWindowError is the 400 body for an unrecognized ?window=.
func autonomyWindowError() string {
	return "invalid window for chart=autonomy_projects: use 30d|1y"
}

// historyAutonomyPanelBucket is one time bucket of one project's panel.
//
// A BUCKET WITH NOTHING IN IT IS OMITTED, never emitted with zeros — a day with
// no runs is a gap, not a run of length zero next to nobody working. The line
// breaks there and the histogram draws nothing, because a zero bar sitting on
// the axis looks measured and a zero point pulls the line down to it. So a
// bucket that is PRESENT has Longest > 0 or Peak > 0.
type historyAutonomyPanelBucket struct {
	TS int64 `json:"ts"`

	// Longest is the length in seconds of the longest run that ENDED in this
	// bucket, and 0 when no run ended here.
	//
	// BY END, matching the store's own window semantics (a span is selected by
	// where it ended, since that is the only timestamp every row is guaranteed
	// to have). The consequence is worth stating: a run that merely passed
	// THROUGH a bucket raises that bucket's concurrency without putting a point
	// on its line, so a bucket can carry a bar and no line point. That is the
	// honest pair of facts — something was working, nothing finished — and the
	// alternative, crediting a run to every bucket it touched, would draw one
	// 11-hour run as two 11-hour days.
	Longest float64 `json:"longest"`

	// Running marks a bucket whose longest run HAS NOT ENDED: its length is how
	// long it has lasted so far, a floor rather than a measurement. It is still
	// the longest — "already lasted 3h" is a true statement about the longest
	// run — and it is marked so nobody reads a floor as final.
	Running bool `json:"running,omitempty"`

	// Peak is the greatest number of runs working AT THE SAME INSTANT anywhere
	// in this bucket. PEAK, not average: peak answers "how wide did I go", where
	// an average over a day is dominated by the hours nothing ran.
	Peak int `json:"peak"`

	// PeakTop and PeakSub split Peak at the instant it was reached. Meaningful
	// only when PeakSplit is true.
	PeakTop int `json:"peak_top"`
	PeakSub int `json:"peak_sub"`

	// PeakSplit reports that every run alive at the peak instant said which kind
	// it was, so PeakTop + PeakSub is a derivation rather than a guess.
	//
	// False is the normal case for old data: rows written before #1916, and rows
	// the back-fill rebuilt from a source with no parent information, carry
	// session.AutonomyKindUnknown. A client shows the total alone there — never
	// a split with an invented denominator.
	PeakSplit bool `json:"peak_split,omitempty"`
}

// historyAutonomyPanel is one project's panel: its two window-wide figures and
// its per-bucket series.
type historyAutonomyPanel struct {
	Project string `json:"project"`

	// Longest is the longest run in the whole window, in seconds, and
	// LongestRunning marks it as one that has not ended.
	//
	// A STILL-RUNNING RUN COUNTS TOWARDS IT, unlike its old effect on a
	// percentile. "The longest run already lasted 3h" is true and useful; a p95
	// that folded the same floor in would have claimed the top 5% of runs were
	// shorter than they were.
	Longest        float64 `json:"longest"`
	LongestRunning bool    `json:"longest_running,omitempty"`

	// TotalSeconds is the sum of every run's length — the figure the panels are
	// RANKED by. On the wire so the ranking can be checked against the panels
	// rather than taken on trust.
	TotalSeconds float64 `json:"total_seconds"`

	// Runs is how many runs the window holds for this project, of every kind.
	Runs int `json:"runs"`

	// Peak is the most runs this project had working at once anywhere in the
	// window, split the same way a bucket's is when the split is derivable.
	Peak      int  `json:"peak"`
	PeakTop   int  `json:"peak_top"`
	PeakSub   int  `json:"peak_sub"`
	PeakSplit bool `json:"peak_split,omitempty"`

	Buckets []historyAutonomyPanelBucket `json:"buckets"`
}

// historyAutonomySummary is the window-wide figure row: the section's two
// headline numbers across EVERY project, not just the five drawn.
type historyAutonomySummary struct {
	// Longest is the longest run anywhere in the window, and LongestProject
	// names where it happened.
	Longest        float64 `json:"longest"`
	LongestRunning bool    `json:"longest_running,omitempty"`
	LongestProject string  `json:"longest_project,omitempty"`

	// Peak is the highest per-project concurrency in the window — the widest any
	// ONE project went, never a sum across projects, which would be a different
	// and much larger number.
	Peak        int    `json:"peak"`
	PeakProject string `json:"peak_project,omitempty"`

	// Runs and Projects count the window.
	Runs     int `json:"runs"`
	Projects int `json:"projects"`
}

// historyAutonomyProjectsResponse is the chart=autonomy_projects payload.
type historyAutonomyProjectsResponse struct {
	Window        string  `json:"window"`
	Chart         string  `json:"chart"`
	Start         int64   `json:"start"`
	End           int64   `json:"end"`
	BucketSeconds int64   `json:"bucket_seconds"`
	BucketStarts  []int64 `json:"bucket_starts"`

	// Panels are the five most important projects, most autonomous time first.
	Panels []historyAutonomyPanel `json:"panels"`
	// PanelLimit is how many panels the daemon draws — on the wire so a client
	// renders what it was sent rather than re-deciding the number itself.
	PanelLimit int `json:"panel_limit"`
	// MoreProjects is how many projects the window holds beyond the panels, all
	// of them with less autonomous time than every panel above.
	MoreProjects int `json:"more_projects"`

	Summary historyAutonomySummary `json:"summary"`

	// EarliestSpan is the earliest span on record across the WHOLE log, not this
	// window — 0 when nothing has ever been recorded. It is what lets both
	// clients say "collecting since <date>" instead of leaving an empty section
	// to be read as "you did nothing" (#1905).
	EarliestSpan  int64 `json:"earliest_span"`
	TotalRecorded int   `json:"total_recorded"`

	// Provenance marks how much of THIS window was reconstructed rather than
	// measured (#1905 back-fill), and carries the source boundaries the panels
	// draw a rule at. Always present, zero-valued when the whole window was
	// measured live.
	Provenance historyAutonomyProvenance `json:"provenance"`
	// Kinds says what this payload is made of, by run kind (#1905 subagents).
	Kinds historyAutonomyKinds `json:"kinds"`
	// Measurement says how many of the runs in view have a duration that is a
	// floor rather than a measurement (#1905 recording).
	Measurement historyAutonomyMeasurement `json:"measurement"`
}

// historyAutonomyProvenance is the wire shape of
// outbound.AutonomySpanProvenance.
//
// It ships even though the back-fill tool never does: the tool is a one-off the
// maintainer runs by hand on one machine, but the rows it writes are read by
// every daemon and both clients afterwards, and a reconstructed number rendered
// as a measured one is precisely the "wrong figure with nothing on screen saying
// so" this feature was built to avoid.
type historyAutonomyProvenance struct {
	// Reconstructed is how many of the runs in this window were rebuilt from a
	// log rather than measured as they happened.
	Reconstructed int `json:"reconstructed"`
	// CostDerived is the subset of those whose end reason is unknown and cannot
	// be recovered — the source records activity, not outcome.
	CostDerived int `json:"cost_derived"`
	// LiveSince is the earliest MEASURED span across the whole log: the instant
	// before which everything on record is reconstructed. 0 when nothing has
	// ever been measured live.
	LiveSince int64 `json:"live_since"`
	// Boundaries are the instants where the PROVENANCE of the data changes,
	// oldest first. Empty when everything on record came from one source.
	Boundaries []historyAutonomyBoundary `json:"boundaries"`
}

// autonomyEraLive is the wire name for the measured era, whose rows carry no
// `source` at all. It exists only on the wire and in a label — nothing writes it
// to a row, and it is deliberately absent from session.AutonomySources() so it
// can never be mistaken for one.
const autonomyEraLive = "live"

// historyAutonomyBoundary is one instant where the data's provenance changes —
// a run drawn to the left of it came from `from`, one to the right from `to`.
//
// It exists because the provenance PARAGRAPH cannot fix what the eye reads off
// the LINE (#1905 back-fill, QA-2). The cost log cannot see a run shorter than
// its 60 s write interval; the event log records one-second runs. So the line
// steps at the source boundary and a reader takes a change of instrument for a
// change of behaviour. The marker puts the explanation where the artefact is.
//
// It survives the redesign for the same reason it was built, and the redesign
// makes it MORE necessary rather than less: five stacked panels sharing one x
// axis all step at the same instant, which looks like five findings and is one
// instrument change. Both clients draw the rule through every panel and caption
// it once (see the caption rules in each client).
//
// TS is the earliest start of the NEWER era. The rules that build the rows
// guarantee the older source stops there — the cost era ends at the event log's
// first transition, and the reconstruction refuses to write into the era the
// daemon has already measured — so one instant separates them rather than an
// overlap that would have no single boundary to draw.
type historyAutonomyBoundary struct {
	TS   int64  `json:"ts"`
	From string `json:"from"`
	To   string `json:"to"`
}

// autonomyProvenanceFrom converts the store's provenance block to the wire
// shape.
func autonomyProvenanceFrom(p outbound.AutonomySpanProvenance) historyAutonomyProvenance {
	return historyAutonomyProvenance{
		Reconstructed: p.Reconstructed,
		CostDerived:   p.CostDerived,
		LiveSince:     p.LiveSince(),
		Boundaries:    autonomyBoundariesFrom(p.EraStarts),
	}
}

// autonomyBoundariesFrom turns the log's per-source era starts into the instants
// where one era hands over to the next.
//
// ONE MECHANISM, not a case per pair. Today there are two handovers on a
// back-filled machine — cost→log and log→live — but nothing here names either:
// the eras are sorted by when they start and a boundary is emitted between each
// adjacent pair. A third source, or a machine that only ever had two of them,
// falls out without another branch.
//
// A single era yields no boundary, which is the normal install: nothing to mark,
// so nothing is drawn.
func autonomyBoundariesFrom(eraStarts map[string]int64) []historyAutonomyBoundary {
	type era struct {
		source string
		start  int64
	}
	eras := make([]era, 0, len(eraStarts))
	for source, start := range eraStarts {
		if start <= 0 {
			continue
		}
		eras = append(eras, era{source: source, start: start})
	}
	// Sorted by start; the source name breaks a tie, so two eras that begin in
	// the same second still order the same way on every request rather than
	// following map iteration order.
	sort.Slice(eras, func(i, j int) bool {
		if eras[i].start != eras[j].start {
			return eras[i].start < eras[j].start
		}
		return eras[i].source < eras[j].source
	})

	out := make([]historyAutonomyBoundary, 0, max(0, len(eras)-1))
	for i := 1; i < len(eras); i++ {
		out = append(out, historyAutonomyBoundary{
			TS:   eras[i].start,
			From: autonomyEraName(eras[i-1].source),
			To:   autonomyEraName(eras[i].source),
		})
	}
	return out
}

// autonomyEraName is a row's `source` as the wire spells it: "" — the absence
// that means measured — becomes "live".
func autonomyEraName(source string) string {
	if source == "" {
		return autonomyEraLive
	}
	return source
}

// historyAutonomyKinds is the wire shape of outbound.AutonomySpanKinds: what the
// window is made of, by run kind (#1905 subagents).
//
// THERE IS NO MODE ANY MORE (#1905 recording). Subagent runs are always counted
// — they are runs Irrlicht itself recorded — so "42 runs" means one thing and a
// client has nothing to remember it asked for. The three counts stay because
// they still say something a reader wants: how much of a window was subagent
// work, and how much of it predates the classification. The concurrency figure
// leans on the same field, and the Unknown count is what tells a reader why a
// panel's `at once` figure sometimes carries no split.
type historyAutonomyKinds struct {
	// TopLevel, Subagent and Unknown count the window, and — nothing being
	// dropped for its kind — the rows returned with them.
	TopLevel int `json:"top_level"`
	Subagent int `json:"subagent"`
	Unknown  int `json:"unknown"`
}

// autonomyKindsFrom converts the store's kind census to the wire shape.
func autonomyKindsFrom(k outbound.AutonomySpanKinds) historyAutonomyKinds {
	return historyAutonomyKinds{
		TopLevel: k.TopLevel,
		Subagent: k.Subagent,
		Unknown:  k.Unknown,
	}
}

// historyAutonomyMeasurement is the wire shape of
// outbound.AutonomySpanMeasurement: how many of the runs in view have a duration
// that is a LOWER BOUND rather than a measurement (#1905 recording).
//
// It ships beside Provenance and answers a different question. Provenance asks
// where a number came from; this asks whether the number is finished. A run
// still going, and a run Irrlicht met already in progress, are both real runs
// with real durations — floors, not measurements. Both are counted, returned and
// named on screen rather than quietly dropped, because dropping them is what
// left 5 of a day's 35 runs on the record.
type historyAutonomyMeasurement struct {
	// Running is how many of the runs in view have not ended.
	//
	// They ARE counted towards the longest run now — see historyAutonomyPanel.
	// What they must never do is pass as finished, which is why they are counted
	// separately and marked wherever one is the figure being shown.
	Running int `json:"running"`

	// LowerBoundStart is how many began before Irrlicht was watching, so their
	// recorded start is when it started watching and not when the run began.
	LowerBoundStart int `json:"start_lower_bound"`
}

// autonomyMeasurementFrom converts the store's measurement census to the wire
// shape.
func autonomyMeasurementFrom(m outbound.AutonomySpanMeasurement) historyAutonomyMeasurement {
	return historyAutonomyMeasurement{
		Running:         m.Running,
		LowerBoundStart: m.LowerBoundStart,
	}
}

// serveHistoryAutonomyProjectsChart serves chart=autonomy_projects. A nil store
// yields an empty-but-valid payload rather than an error, mirroring
// serveHistoryAgentsChart.
func serveHistoryAutonomyProjectsChart(w http.ResponseWriter, store outbound.AutonomySpanStore, window string, bucketSeconds, start, end int64) {
	res, ok := readAutonomySpans(w, store, outbound.AutonomySpanQuery{Start: start, End: end})
	if !ok {
		return
	}
	writeHistoryJSON(w, buildAutonomyProjectsResponse(window, bucketSeconds, start, end, res))
}

// readAutonomySpans performs the store read, substituting an empty result for a
// nil store and writing a 500 on error. ok is false once it has written a
// response.
//
// DELIBERATELY UNLIMITED. The run strip capped its read at 20 000 rows because
// it drew one column per run and could not draw more than its own pixel width;
// the panels reduce every run to two per-bucket figures, and a concurrency peak
// computed from a clipped span list is simply wrong — silently, and always
// downwards. A cap here would be a number nobody could check.
func readAutonomySpans(w http.ResponseWriter, store outbound.AutonomySpanStore, q outbound.AutonomySpanQuery) (*outbound.AutonomySpanResult, bool) {
	if store == nil {
		return &outbound.AutonomySpanResult{Spans: []outbound.AutonomySpan{}}, true
	}
	res, err := store.SpansInWindow(q)
	if err != nil {
		http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
		return nil, false
	}
	if res == nil {
		res = &outbound.AutonomySpanResult{Spans: []outbound.AutonomySpan{}}
	}
	return res, true
}

// autonomyProjectTotals is one project's window-wide roll-up, accumulated in the
// single pass that groups the window's spans.
type autonomyProjectTotals struct {
	spans          []outbound.AutonomySpan
	totalSeconds   float64
	longest        float64
	longestRunning bool
	runs           int
}

// buildAutonomyProjectsResponse groups the window's spans by project, ranks the
// projects by total autonomous time, and reduces the top autonomyPanelCount of
// them to a panel each.
func buildAutonomyProjectsResponse(window string, bucketSeconds, start, end int64, res *outbound.AutonomySpanResult) historyAutonomyProjectsResponse {
	n := 0
	if bucketSeconds > 0 && end > start {
		n = int((end - start + bucketSeconds - 1) / bucketSeconds)
	}
	bucketStarts := make([]int64, n)
	for i := range bucketStarts {
		bucketStarts[i] = start + int64(i)*bucketSeconds
	}

	byProject := groupAutonomySpans(res.Spans)
	ranked := rankAutonomyProjects(byProject)

	panels := make([]historyAutonomyPanel, 0, min(autonomyPanelCount, len(ranked)))
	for _, project := range ranked[:min(autonomyPanelCount, len(ranked))] {
		panels = append(panels, buildAutonomyPanel(project, byProject[project], start, bucketSeconds, n))
	}

	return historyAutonomyProjectsResponse{
		Window:        window,
		Chart:         chartAutonomyProjects,
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		BucketStarts:  bucketStarts,
		Panels:        panels,
		PanelLimit:    autonomyPanelCount,
		MoreProjects:  max(0, len(ranked)-autonomyPanelCount),
		Summary:       autonomySummaryFrom(ranked, byProject, start, end),
		EarliestSpan:  res.EarliestStart,
		TotalRecorded: res.TotalRecorded,
		Provenance:    autonomyProvenanceFrom(res.Provenance),
		Kinds:         autonomyKindsFrom(res.Kinds),
		Measurement:   autonomyMeasurementFrom(res.Measurement),
	}
}

// groupAutonomySpans buckets the window's spans by project and accumulates each
// project's window-wide roll-up in the same pass.
//
// A RUNNING RUN'S SECONDS SO FAR COUNT towards its project's total, and towards
// its longest. The rank is "which projects had the most autonomous time", and
// three hours still going is three hours of it; leaving it out would rank the
// busiest project below one that merely finished first.
func groupAutonomySpans(spans []outbound.AutonomySpan) map[string]*autonomyProjectTotals {
	out := map[string]*autonomyProjectTotals{}
	for _, s := range spans {
		t := out[s.Project]
		if t == nil {
			t = &autonomyProjectTotals{}
			out[s.Project] = t
		}
		t.spans = append(t.spans, s)
		t.runs++
		d := float64(s.Duration())
		t.totalSeconds += d
		if d > t.longest {
			t.longest = d
			t.longestRunning = s.Running
		}
	}
	return out
}

// rankAutonomyProjects orders projects by TOTAL AUTONOMOUS SECONDS, most first,
// with the project name breaking a tie so the same window ranks the same way on
// every request rather than following map iteration order.
//
// Not by longest run, and the difference is the whole point of ranking at all:
// a project with one lucky overnight run and nothing else would outrank a
// project that worked every day of the window.
func rankAutonomyProjects(byProject map[string]*autonomyProjectTotals) []string {
	out := make([]string, 0, len(byProject))
	for project := range byProject {
		out = append(out, project)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := byProject[out[i]], byProject[out[j]]
		if a.totalSeconds != b.totalSeconds {
			return a.totalSeconds > b.totalSeconds
		}
		return out[i] < out[j]
	})
	return out
}

// buildAutonomyPanel reduces one project's spans to its panel.
func buildAutonomyPanel(project string, t *autonomyProjectTotals, start, bucketSeconds int64, n int) historyAutonomyPanel {
	longest, running := autonomyBucketLongest(t.spans, start, bucketSeconds, n)
	peaks := autonomyPeaks(t.spans, start, bucketSeconds, n)
	windowPeak := autonomyWindowPeak(t.spans, start, start+bucketSeconds*int64(n))

	buckets := make([]historyAutonomyPanelBucket, 0, n)
	for i := 0; i < n; i++ {
		// The gap rule: nothing finished here AND nothing was working here, so
		// the bucket is omitted rather than sent as a pair of measured zeros.
		if longest[i] <= 0 && peaks[i].peak <= 0 {
			continue
		}
		buckets = append(buckets, historyAutonomyPanelBucket{
			TS:        start + int64(i)*bucketSeconds,
			Longest:   longest[i],
			Running:   running[i],
			Peak:      peaks[i].peak,
			PeakTop:   peaks[i].topLevel,
			PeakSub:   peaks[i].subagent,
			PeakSplit: peaks[i].splitKnown,
		})
	}

	return historyAutonomyPanel{
		Project:        project,
		Longest:        t.longest,
		LongestRunning: t.longestRunning,
		TotalSeconds:   t.totalSeconds,
		Runs:           t.runs,
		Peak:           windowPeak.peak,
		PeakTop:        windowPeak.topLevel,
		PeakSub:        windowPeak.subagent,
		PeakSplit:      windowPeak.splitKnown,
		Buckets:        buckets,
	}
}

// autonomyBucketLongest returns, per bucket, the length of the longest run that
// ENDED in it and whether that run was still going.
//
// A run's bucket is the one its END falls in — the store selects spans by their
// end for the same reason (it is the only timestamp every row is guaranteed to
// have). A run whose end lands exactly on the window's closing instant is
// credited to the last bucket rather than dropped: the buckets tile [start, end)
// so `end` itself is out of range by construction, and a run that finished this
// very second is not a run that did not happen.
func autonomyBucketLongest(spans []outbound.AutonomySpan, start, bucketSeconds int64, n int) (longest []float64, running []bool) {
	longest = make([]float64, n)
	running = make([]bool, n)
	if n == 0 || bucketSeconds <= 0 {
		return longest, running
	}
	for _, s := range spans {
		d := float64(s.Duration())
		if d <= 0 {
			continue
		}
		idx := int((s.End - start) / bucketSeconds)
		if idx < 0 {
			continue
		}
		if idx >= n {
			idx = n - 1
		}
		if d > longest[idx] {
			longest[idx] = d
			running[idx] = s.Running
		}
	}
	return longest, running
}

// autonomySummaryFrom reduces every project in the window — not just the five
// drawn — to the section's two headline figures.
func autonomySummaryFrom(ranked []string, byProject map[string]*autonomyProjectTotals, start, end int64) historyAutonomySummary {
	out := historyAutonomySummary{Projects: len(ranked)}
	for _, project := range ranked {
		t := byProject[project]
		out.Runs += t.runs
		if t.longest > out.Longest {
			out.Longest = t.longest
			out.LongestRunning = t.longestRunning
			out.LongestProject = project
		}
		// One sweep per project, over the whole window as a single bucket: the
		// figure is "the widest any ONE project went", never a sum across
		// projects, which would be a different and much larger number.
		if peak := autonomyWindowPeak(t.spans, start, end); peak.peak > out.Peak {
			out.Peak = peak.peak
			out.PeakProject = project
		}
	}
	return out
}
