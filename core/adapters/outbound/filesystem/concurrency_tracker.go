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

// concurrencyUnknownProject labels sessions whose recordings carry no working
// directory (so no project can be derived). Matches the handler's "unknown"
// contributor label for cost.
const concurrencyUnknownProject = "unknown"

// ConcurrencyTracker reconstructs concurrent-agent counts over time from the
// lifecycle recordings irrlichd writes under <dataDir>/recordings when started
// with --record (issue #751, History tab Phase 3). It is read-only — it never
// writes the recordings dir — and is the recordings analog of CostTracker's
// CostSeries over the cost dir.
type ConcurrencyTracker struct {
	dir string
}

// NewConcurrencyTrackerWithDir returns a reader rooted at the given recordings
// directory. The daemon passes the same dir the recorder writes to (see
// resolveRecordingsDir), so reads and writes can't drift.
func NewConcurrencyTrackerWithDir(dir string) *ConcurrencyTracker {
	return &ConcurrencyTracker{dir: dir}
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
// directory (basename), matching how the cost chart keys projects. State and
// process events carry no CWD, so the label is sourced from the session's
// transcript events (which do) via "last non-empty wins" in loadTimelines.
func concurrencyProject(ev lifecycle.Event) string {
	if ev.CWD != "" {
		return filepath.Base(ev.CWD)
	}
	return ""
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

	// Collect each session's active intervals, clamped to the window. Group by
	// project for the per-bucket peak series, and keep a flat list for the exact
	// total peak/average. Current counts sessions active strictly across End
	// (their real, pre-clamp interval spans "now").
	byProject := map[string][]interval{}
	var all []interval
	for sid, tl := range timelines {
		if !concurrencyScopeMatches(q, sid, tl.project) {
			continue
		}
		project := tl.project
		if project == "" {
			project = concurrencyUnknownProject
		}
		for _, iv := range tl.activeIntervals() {
			if iv.enter < end && iv.exit > end {
				out.Current++ // spans the window end → still active "now"
			}
			a, b := iv.enter, iv.exit
			if a < start {
				a = start
			}
			if b > end {
				b = end
			}
			if b <= a {
				continue
			}
			cl := interval{a, b}
			byProject[project] = append(byProject[project], cl)
			all = append(all, cl)
		}
	}

	// Per-project per-bucket peak concurrency.
	for project, ivs := range byProject {
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
		out.ByKey[project] = dst
		out.PeakByKey[project] = peak
	}

	// Exact total peak (max simultaneous across all projects) and time-weighted
	// average over [start, end).
	integral := 0.0
	sweepIntervals(all, func(t0, t1 int64, v float64) {
		if v > out.Peak {
			out.Peak = v
		}
		integral += v * float64(t1-t0)
	})
	if end > start {
		out.Average = integral / float64(end-start)
	}
	return out, nil
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

// activeIntervals reconstructs a session's [enter, exit) active spans from its
// state timeline. A session is active while working or waiting and inactive
// while ready. A session still active at its last recorded event (no terminator)
// is bounded at that last event — "last known alive" — so a daemon that died
// mid-session doesn't leave a session counted as alive forever. The bound is
// needed for the common idle case too: a session waiting for input emits no
// further events, so without it that session's whole bounded span would drop.
// (lastEventTS >= last.ts always, since every transition is itself an event.)
func (tl *sessionTimeline) activeIntervals() []interval {
	if len(tl.transitions) == 0 {
		return nil
	}
	tr := append([]stateChange(nil), tl.transitions...)
	sort.SliceStable(tr, func(i, j int) bool { return tr[i].ts < tr[j].ts })
	if last := tr[len(tr)-1]; concurrencyActive(last.state) {
		tr = append(tr, stateChange{tl.lastEventTS, session.StateReady})
	}

	var ivs []interval
	activeStart := int64(-1)
	for _, c := range tr {
		if concurrencyActive(c.state) {
			if activeStart < 0 {
				activeStart = c.ts
			}
			continue
		}
		if activeStart >= 0 {
			if c.ts > activeStart {
				ivs = append(ivs, interval{activeStart, c.ts})
			}
			activeStart = -1
		}
	}
	return ivs
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
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if err := scanRecordingFile(filepath.Join(t.dir, e.Name()), func(ev lifecycle.Event) {
			tl := timelines[ev.SessionID]
			if tl == nil {
				tl = &sessionTimeline{}
				timelines[ev.SessionID] = tl
			}
			ts := ev.Timestamp.Unix()
			if ts > tl.lastEventTS {
				tl.lastEventTS = ts
			}
			if p := concurrencyProject(ev); p != "" {
				tl.project = p
			}
			switch ev.Kind {
			case lifecycle.KindStateTransition:
				if session.IsCanonicalState(ev.NewState) {
					tl.transitions = append(tl.transitions, stateChange{ts, ev.NewState})
				}
			case lifecycle.KindProcessExited, lifecycle.KindTranscriptRemoved:
				// A terminated session is ready, even if no state_transition
				// to ready was recorded for it.
				tl.transitions = append(tl.transitions, stateChange{ts, session.StateReady})
			}
		}); err != nil {
			return nil, err
		}
	}
	return timelines, nil
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
