package filesystem

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// concurrencyProjectResolver is the narrow git-aware project-name resolution
// ConcurrencyTracker needs to fold worktree sessions (.claude/worktrees/<N>-
// <slug>/, created by the ir:exec skill per GitHub issue) into their real repo
// instead of keying them as their own separate project (issue #1046).
// *git.Adapter already satisfies this structurally. nil is valid (falls back
// to a bare basename, i.e. pre-#1046 behavior) so every recordings-dir-only
// test fixture can omit it.
type concurrencyProjectResolver interface {
	GetProjectName(dir string) string
}

// ConcurrencyTracker reconstructs concurrent-agent counts over time from the
// lifecycle recordings irrlichd writes under <dataDir>/recordings when started
// with --record (issue #751, History tab Phase 3). It is read-only — it never
// writes the recordings dir — and is the recordings analog of CostTracker's
// CostSeries over the cost dir.
type ConcurrencyTracker struct {
	dir string
	git concurrencyProjectResolver
}

// NewConcurrencyTrackerWithDir returns a reader rooted at the given recordings
// directory. The daemon passes the same dir the recorder writes to (see
// resolveRecordingsDir), so reads and writes can't drift. git resolves each
// event's raw CWD to its git repo's project name (issue #1046); pass nil to
// fall back to a bare filepath.Base(cwd).
func NewConcurrencyTrackerWithDir(dir string, git concurrencyProjectResolver) *ConcurrencyTracker {
	return &ConcurrencyTracker{dir: dir, git: git}
}

// Dir returns the recordings directory this reader scans.
func (t *ConcurrencyTracker) Dir() string { return t.dir }

// stateChange is one (timestamp, state) point in a session's reconstructed
// state timeline.
type stateChange struct {
	ts    int64
	state string // working|waiting|ready
}

// sessionTimeline accumulates one session's state transitions and project label
// while recordings are scanned. transitions are appended in scan order and
// sorted by ts before use; lastEventTS bounds a session that is still active at
// the end of the recording (no process_exited / transcript_removed) so it isn't
// counted as alive forever.
type sessionTimeline struct {
	project     string
	lastEventTS int64
	transitions []stateChange
}

// interval is a half-open [enter, exit) span during which a session is active
// (working or waiting). A session contributes +1 to concurrency over it.
type interval struct {
	enter int64
	exit  int64
}

// concurrencyActive reports whether a state counts toward concurrency: an agent
// is "concurrent" while working or waiting, and stops once it goes ready
// (process exited / cancelled / transcript removed) — the three-state model
// read literally (#751).
func concurrencyActive(state string) bool {
	return state == session.StateWorking || state == session.StateWaiting
}

// concurrencyProject derives a project label for an event from its working
// directory, preferring git-root resolution (folding a worktree session into
// its parent repo, issue #1046) when a resolver is available, falling back to
// a bare basename otherwise (nil resolver, or a directory GetProjectName
// itself can't resolve to a git repo). State and process events carry no CWD,
// so the label is sourced from the session's transcript events (which do) via
// "last non-empty wins" in loadTimelines. memo caches the resolution per raw
// CWD within one loadTimelines() scan, so a CWD shared across many events/
// sessions is only ever resolved (and, if git-backed, shelled out to `git`)
// once per request rather than once per event.
func concurrencyProject(ev lifecycle.Event, git concurrencyProjectResolver, memo map[string]string) string {
	if ev.CWD == "" {
		return ""
	}
	if p, ok := memo[ev.CWD]; ok {
		return p
	}
	p := filepath.Base(ev.CWD)
	if git != nil {
		if resolved := git.GetProjectName(ev.CWD); resolved != "" {
			p = resolved
		}
	}
	memo[ev.CWD] = p
	return p
}

// AgentsSeries reconstructs a per-project concurrent-agents time series over
// [Start, End) bucketed into BucketSeconds-wide buckets, plus the exact
// peak/average/current total concurrency. One pass over every recording file.
// A missing recordings dir (the common case — --record is opt-in) yields an
// empty result, never an error.
func (t *ConcurrencyTracker) AgentsSeries(q outbound.SeriesQuery) (*outbound.ConcurrencyResult, error) {
	start, end, bucketSeconds := q.Start, q.End, q.BucketSeconds
	if bucketSeconds <= 0 || end <= start {
		return emptyConcurrencyResult(start, end, bucketSeconds), nil
	}
	// Coarsen the bucket if the span would otherwise blow past the ceiling,
	// keeping the allocation and payload bounded — same rule as CostSeries.
	if span := end - start; span/bucketSeconds+1 > maxSeriesBuckets {
		bucketSeconds = (span + maxSeriesBuckets - 1) / maxSeriesBuckets
	}
	n := int((end - start + bucketSeconds - 1) / bucketSeconds)
	out := &outbound.ConcurrencyResult{
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		BucketStarts:  make([]int64, n),
		ByKey:         map[string][]float64{},
		PeakByKey:     map[string]float64{},
	}
	for i := range out.BucketStarts {
		out.BucketStarts[i] = start + int64(i)*bucketSeconds
	}

	timelines, err := t.loadTimelines()
	if err != nil {
		return nil, err
	}

	byProject, all, current := collectScopedIntervals(timelines, q, start, end)
	out.Current = current

	// Per-project per-bucket peak concurrency.
	for project, ivs := range byProject {
		dst, peak := bucketPeakSeries(ivs, start, bucketSeconds, n)
		out.ByKey[project] = dst
		out.PeakByKey[project] = peak
	}

	// Exact total peak (max simultaneous across all projects) and time-weighted
	// average over [start, end).
	out.Peak, out.Average = totalPeakAndAverage(all, start, end)
	return out, nil
}

// collectScopedIntervals gathers each in-scope session's active intervals,
// clamped to [start, end), grouped by project for the per-bucket peak series
// and flattened for the exact total peak/average. current counts sessions
// active strictly across end — their real, pre-clamp interval spans "now".
func collectScopedIntervals(timelines map[string]*sessionTimeline, q outbound.SeriesQuery, start, end int64) (byProject map[string][]interval, all []interval, current float64) {
	byProject = map[string][]interval{}
	for sid, tl := range timelines {
		if !concurrencyScopeMatches(q, sid, tl.project) {
			continue
		}
		// tl.project may be "" (no event ever carried a CWD) — left raw here,
		// same as CostSeriesResult, so handlers.go's share-based unknown-bucket
		// rule (resolveUnknownConcurrencyProject) is the sole place that
		// decides whether to surface or drop it (issue #1046).
		project := tl.project
		for _, iv := range tl.activeIntervals() {
			if iv.enter < end && iv.exit > end {
				current++ // spans the window end → still active "now"
			}
			cl, ok := clampInterval(iv, start, end)
			if !ok {
				continue
			}
			byProject[project] = append(byProject[project], cl)
			all = append(all, cl)
		}
	}
	return byProject, all, current
}

// StateSeries is AgentsSeries' per-state counterpart (issue #981): it keeps
// working/waiting separate instead of merging them into one "active" count,
// and adds ready as a per-bucket transition histogram. Same coarsening-by-span
// and empty-result conventions as AgentsSeries; one additional pass over the
// already-loaded timelines, no extra file I/O.
func (t *ConcurrencyTracker) StateSeries(q outbound.SeriesQuery) (*outbound.StateSeriesResult, error) {
	start, end, bucketSeconds := q.Start, q.End, q.BucketSeconds
	if bucketSeconds <= 0 || end <= start {
		return emptyStateSeriesResult(start, end, bucketSeconds), nil
	}
	if span := end - start; span/bucketSeconds+1 > maxSeriesBuckets {
		bucketSeconds = (span + maxSeriesBuckets - 1) / maxSeriesBuckets
	}
	n := int((end - start + bucketSeconds - 1) / bucketSeconds)
	out := &outbound.StateSeriesResult{
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		BucketStarts:  make([]int64, n),
		ByState: map[string]map[string][]float64{
			session.StateWorking: {},
			session.StateWaiting: {},
			session.StateReady:   {},
		},
	}
	for i := range out.BucketStarts {
		out.BucketStarts[i] = start + int64(i)*bucketSeconds
	}

	timelines, err := t.loadTimelines()
	if err != nil {
		return nil, err
	}

	byState, readyByProject, current := collectScopedStateIntervals(timelines, q, start, end)
	out.Current = current

	var allActive []interval
	for state, byProject := range byState {
		for project, ivs := range byProject {
			dst, _ := bucketPeakSeries(ivs, start, bucketSeconds, n)
			out.ByState[state][project] = dst
			allActive = append(allActive, ivs...)
		}
	}
	out.Peak, out.Average = totalPeakAndAverage(allActive, start, end)

	for project, events := range readyByProject {
		dst := make([]float64, n)
		for _, ts := range events {
			idx := int((ts - start) / bucketSeconds)
			if idx >= 0 && idx < n {
				dst[idx]++
			}
		}
		out.ByState[session.StateReady][project] = dst
	}

	return out, nil
}

// collectScopedStateIntervals is collectScopedIntervals' per-state
// counterpart: it gathers each in-scope session's working/waiting intervals,
// clamped to [start, end) and grouped by state then project, plus every ready
// transition timestamp landing in [start, end), grouped by project. current
// counts sessions active (in either state) strictly across end, matching
// collectScopedIntervals' "still active now" semantics exactly.
func collectScopedStateIntervals(timelines map[string]*sessionTimeline, q outbound.SeriesQuery, start, end int64) (byState map[string]map[string][]interval, readyByProject map[string][]int64, current float64) {
	byState = map[string]map[string][]interval{
		session.StateWorking: {},
		session.StateWaiting: {},
	}
	readyByProject = map[string][]int64{}
	for sid, tl := range timelines {
		if !concurrencyScopeMatches(q, sid, tl.project) {
			continue
		}
		// See the matching comment in collectScopedIntervals: "" is left raw
		// for handlers.go's resolveUnknownStateProject to decide (issue #1046).
		project := tl.project
		ivs, readyAt := tl.stateReconstruction()
		for _, iv := range ivs {
			if iv.enter < end && iv.exit > end {
				current++ // spans the window end → still active "now"
			}
			cl, ok := clampInterval(interval{iv.enter, iv.exit}, start, end)
			if !ok {
				continue
			}
			byState[iv.state][project] = append(byState[iv.state][project], cl)
		}
		for _, ts := range readyAt {
			if ts < start || ts >= end {
				continue
			}
			readyByProject[project] = append(readyByProject[project], ts)
		}
	}
	return byState, readyByProject, current
}

// emptyStateSeriesResult returns a valid zero-data result so the dashboard
// renders a clean empty state instead of erroring.
func emptyStateSeriesResult(start, end, bucketSeconds int64) *outbound.StateSeriesResult {
	return &outbound.StateSeriesResult{
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		BucketStarts:  []int64{},
		ByState: map[string]map[string][]float64{
			session.StateWorking: {},
			session.StateWaiting: {},
			session.StateReady:   {},
		},
	}
}

// clampInterval clips iv to [start, end), reporting ok=false when the
// clamped span is empty.
func clampInterval(iv interval, start, end int64) (interval, bool) {
	a, b := iv.enter, iv.exit
	if a < start {
		a = start
	}
	if b > end {
		b = end
	}
	if b <= a {
		return interval{}, false
	}
	return interval{a, b}, true
}

// bucketPeakSeries computes one project's per-bucket peak concurrency series
// (length n, starting at start and stepping by bucketSeconds) plus its exact
// peak across ivs.
func bucketPeakSeries(ivs []interval, start, bucketSeconds int64, n int) ([]float64, float64) {
	dst := make([]float64, n)
	peak := 0.0
	sweepIntervals(ivs, func(t0, t1 int64, v float64) {
		if v > peak {
			peak = v
		}
		lo := int((t0 - start) / bucketSeconds)
		hi := int((t1 - 1 - start) / bucketSeconds)
		if lo < 0 {
			lo = 0
		}
		if hi >= n {
			hi = n - 1
		}
		for i := lo; i <= hi; i++ {
			if v > dst[i] {
				dst[i] = v
			}
		}
	})
	return dst, peak
}

// totalPeakAndAverage computes the exact max-simultaneous count across all
// projects and the time-weighted average over [start, end).
func totalPeakAndAverage(all []interval, start, end int64) (peak, average float64) {
	integral := 0.0
	sweepIntervals(all, func(t0, t1 int64, v float64) {
		if v > peak {
			peak = v
		}
		integral += v * float64(t1-t0)
	})
	if end > start {
		average = integral / float64(end-start)
	}
	return peak, average
}

// emptyConcurrencyResult returns a valid zero-data result so the dashboard
// renders a clean empty state instead of erroring.
func emptyConcurrencyResult(start, end, bucketSeconds int64) *outbound.ConcurrencyResult {
	return &outbound.ConcurrencyResult{
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		BucketStarts:  []int64{},
		ByKey:         map[string][]float64{},
		PeakByKey:     map[string]float64{},
	}
}

// concurrencyScopeMatches applies a drilldown scope to a session. Recordings
// carry only project and session id, so a scope on project/session filters
// directly; a scope on an axis recordings don't carry (branch/provider/model)
// matches nothing rather than silently returning everything.
func concurrencyScopeMatches(q outbound.SeriesQuery, sessionID, project string) bool {
	switch q.ScopeField {
	case "":
		return true
	case "project":
		return project == q.ScopeValue
	case "session":
		return sessionID == q.ScopeValue
	default:
		return false
	}
}

// stateInterval is a half-open [enter, exit) span together with the specific
// working/waiting state active during it — the per-state generalization of
// interval, which only tracks "active" (working or waiting, merged).
type stateInterval struct {
	enter, exit int64
	state       string // session.StateWorking | session.StateWaiting
}

// stateReconstruction rebuilds a session's full timeline: working/waiting
// spans as durations (stateInterval, tagged by which of the two states was
// active) and every transition into ready as an instantaneous timestamp —
// ready is session.go's terminal state, with no duration to be concurrent in,
// so "how many agents went ready in bucket X" (issue #981) is a transition
// count, not a concurrency count. A session still working/waiting at its last
// recorded event (no terminator) is bounded at that last event — "last known
// alive" — so a daemon that died mid-session doesn't leave it counted as alive
// forever; same rule the merged reconstruction below always used. (lastEventTS
// >= last.ts always, since every transition is itself an event.) That bound is
// a synthetic sentinel, not a real transition — it must be read for
// interval-closing but excluded from readyAt, or every session still active
// "now" would spuriously count as having gone ready this instant.
//
// When lastEventTS == last.ts — the transition is itself the session's last
// recorded event, with no later heartbeat to push the bound forward — the
// synthetic bound is nudged one second past it instead of landing exactly on
// it. Without the nudge the closing interval is zero-width and the loop below
// (which requires next.ts > cur.ts) drops it entirely: a session whose only
// event is a single still-active transition would vanish from both
// AgentsSeries and StateSeries despite clearly having been active at that
// instant (issue #983). A one-second floor is the same granularity every
// timestamp here already uses (ev.Timestamp.Unix()).
func (tl *sessionTimeline) stateReconstruction() (ivs []stateInterval, readyAt []int64) {
	if len(tl.transitions) == 0 {
		return nil, nil
	}
	tr := append([]stateChange(nil), tl.transitions...)
	sort.SliceStable(tr, func(i, j int) bool { return tr[i].ts < tr[j].ts })

	for _, c := range tr {
		if c.state == session.StateReady {
			readyAt = append(readyAt, c.ts)
		}
	}

	if last := tr[len(tr)-1]; concurrencyActive(last.state) {
		tr = append(tr, stateChange{max(tl.lastEventTS, last.ts+1), session.StateReady})
	}
	for i := 0; i < len(tr)-1; i++ {
		cur, next := tr[i], tr[i+1]
		if !concurrencyActive(cur.state) {
			continue
		}
		if next.ts > cur.ts {
			ivs = append(ivs, stateInterval{cur.ts, next.ts, cur.state})
		}
	}
	return ivs, readyAt
}

// activeIntervals reports the merged working-or-waiting spans a session was
// concurrently "active" for — the view AgentsSeries needs, which doesn't
// distinguish which of the two states was active. Built on
// stateReconstruction so the merged and per-state views can never drift
// apart: sweepIntervals already treats abutting intervals (one state's exit
// landing exactly on the next state's enter) as continuous rather than a gap
// (see its doc comment), so splitting the merged span into per-state
// sub-intervals here doesn't change AgentsSeries' peak/average/bucket output.
func (tl *sessionTimeline) activeIntervals() []interval {
	ivs, _ := tl.stateReconstruction()
	out := make([]interval, 0, len(ivs))
	for _, iv := range ivs {
		out = append(out, interval{iv.enter, iv.exit})
	}
	return out
}

// sweepIntervals walks a set of half-open intervals in time order and calls fn
// for each maximal [t0, t1) segment with the overlap count active during it.
// Zero-overlap gaps are skipped. At a shared timestamp, exits (-1) are applied
// before entries (+1) so abutting intervals ([a,T) then [T,b)) don't read as
// overlapping.
func sweepIntervals(ivs []interval, fn func(t0, t1 int64, value float64)) {
	type ev struct {
		ts int64
		d  int
	}
	evs := make([]ev, 0, len(ivs)*2)
	for _, iv := range ivs {
		if iv.exit <= iv.enter {
			continue
		}
		evs = append(evs, ev{iv.enter, +1}, ev{iv.exit, -1})
	}
	if len(evs) == 0 {
		return
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].ts != evs[j].ts {
			return evs[i].ts < evs[j].ts
		}
		return evs[i].d < evs[j].d // -1 before +1 at equal ts
	})
	cur := 0
	prev := evs[0].ts
	for _, e := range evs {
		if e.ts > prev {
			if cur > 0 {
				fn(prev, e.ts, float64(cur))
			}
			prev = e.ts
		}
		cur += e.d
	}
}

// loadTimelines scans every recording file once and groups events by session
// into reconstructed state timelines. The project label is sourced from
// whichever of a session's events carry a CWD ("last non-empty wins").
func (t *ConcurrencyTracker) loadTimelines() (map[string]*sessionTimeline, error) {
	timelines := map[string]*sessionTimeline{}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return timelines, nil
		}
		return nil, err
	}
	// memo caches each raw CWD's resolved project name for this one scan (see
	// concurrencyProject) so a git-backed resolution never re-shells `git` for
	// a CWD it's already resolved.
	memo := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(t.dir, e.Name())
		if err := scanRecordingFile(path, func(ev lifecycle.Event) {
			recordTimelineEvent(timelines, ev, t.git, memo)
		}); err != nil {
			return nil, err
		}
	}
	return timelines, nil
}

// recordTimelineEvent folds one recording event into the session timeline it
// belongs to (creating the timeline on first sight), updating lastEventTS,
// the project label ("last non-empty wins"), and appending a state
// transition when the event is a canonical state change or a terminator
// (process exit / transcript removal, both treated as going ready — a
// terminated session is ready even if no state_transition to ready was
// recorded for it).
func recordTimelineEvent(timelines map[string]*sessionTimeline, ev lifecycle.Event, git concurrencyProjectResolver, memo map[string]string) {
	tl := timelines[ev.SessionID]
	if tl == nil {
		tl = &sessionTimeline{}
		timelines[ev.SessionID] = tl
	}
	ts := ev.Timestamp.Unix()
	if ts > tl.lastEventTS {
		tl.lastEventTS = ts
	}
	if p := concurrencyProject(ev, git, memo); p != "" {
		tl.project = p
	}
	switch ev.Kind {
	case lifecycle.KindStateTransition:
		if session.IsCanonicalState(ev.NewState) {
			tl.transitions = append(tl.transitions, stateChange{ts, ev.NewState})
		}
	case lifecycle.KindProcessExited, lifecycle.KindTranscriptRemoved:
		tl.transitions = append(tl.transitions, stateChange{ts, session.StateReady})
	}
}

// scanRecordingFile streams one recording file, invoking perEvent for each
// parsed lifecycle event. A missing file and malformed lines are skipped — a
// partial file shouldn't fail the whole query. Same skip policy as scanCostFile,
// but with an 8 MB max line like the replay reader (loadAllLifecycleEvents):
// recordings interleave every session, so lines and files run larger than the
// per-project cost files.
func scanRecordingFile(path string, perEvent func(lifecycle.Event)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev lifecycle.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		perEvent(ev)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}
