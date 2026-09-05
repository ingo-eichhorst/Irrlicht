package filesystem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// The OPEN-RUN JOURNAL (#1905 recording).
//
// The span log records runs that ENDED. That was the whole of the feature, and
// it is why the feature lost most of what it was built to report: a run only
// reaches the log when it closes, so a run still in progress contributed
// nothing, and a run whose daemon died mid-flight contributed nothing EVER.
// The bias is what makes it a defect rather than a lag — the longer a run, the
// likelier it crosses a restart, so the runs that vanished were precisely the
// long autonomous ones.
//
// The journal is the missing half: the runs that have STARTED. Two consumers,
// one file:
//
//   - a restarting daemon reads it and adopts the start of a run that is still
//     going, so a 3-hour run that spans four restarts is one 3-hour run and
//     not four lost ones;
//   - a window read serves it, so a run in progress is visible WHILE it
//     happens instead of only in retrospect.
//
// ONE FILE, REWRITTEN WHOLE, not the append-only JSONL the closed log uses —
// because this is a keyed set with deletes, not a history. Its size is bounded
// by how many sessions are working at once (tens), which is why rewriting it is
// affordable and appending would not be.

const (
	// autonomyOpenFileName is the journal's name under the autonomy dir,
	// sibling to the per-project closed logs. `.json`, not `.jsonl`, and that
	// matters: SpansInWindow and Prune both walk the directory for
	// autonomyFileExt, so a journal named `.jsonl` would be read back as a
	// project's closed log and every open run would be filed twice.
	autonomyOpenFileName = "open.json"

	// autonomyOpenSchemaVersion documents the on-disk shape. v1 is the
	// original (#1905 recording). Documentary, like autonomySchemaVersion —
	// the file is a JSON object of omitempty-tolerant rows, so a row written
	// by another build decodes with unknown fields dropped and absent fields
	// zero.
	autonomyOpenSchemaVersion = 1
)

// openRow is one run that has started and not finished.
//
// LastSeen is the load-bearing field and the one a reader must not confuse
// with an end: it is the last instant the daemon OBSERVED this run still
// going. For a live daemon it is a few seconds ago. For a journal left behind
// by a daemon that died, it is the last thing anyone knows — which is exactly
// where such a run gets closed, rather than at "now", which would credit the
// run with the whole time the daemon was down.
type openRow struct {
	Start    int64  `json:"start"`
	LastSeen int64  `json:"last_seen"`
	Project  string `json:"project,omitempty"`
	Session  string `json:"session,omitempty"`
	Adapter  string `json:"adapter,omitempty"`
	Model    string `json:"model,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Parent   string `json:"parent,omitempty"`

	// StartLowerBound marks a run Irrlicht met already in progress, so Start
	// is when it started WATCHING and not when the run began.
	StartLowerBound bool `json:"start_lower_bound,omitempty"`
}

// openPath is the journal's path.
func (t *AutonomySpanTracker) openPath() string {
	return filepath.Join(t.dir, autonomyOpenFileName)
}

// RecordOpenSpan records or refreshes one run that is under way.
//
// A read-modify-write under the journal's own lock. No-ops on a span with no
// session id: the journal is keyed by session, and an entry nothing can be
// matched back to could never be adopted after a restart nor cleared when the
// run ends — it would just be a run that is permanently "still going".
func (t *AutonomySpanTracker) RecordOpenSpan(span outbound.AutonomySpan) error {
	if span.Session == "" {
		return nil
	}
	t.openMu.Lock()
	defer t.openMu.Unlock()
	rows, err := t.readOpenRows()
	if err != nil {
		return err
	}
	rows[span.Session] = openRowFrom(span)
	return t.writeOpenRows(rows)
}

// SyncOpenSpans replaces the journal with exactly these runs.
//
// The reconciling write, and the reason the journal cannot leak: a session that
// disappeared without ever closing its run is simply absent from the next sync,
// so its entry goes with it. RecordOpenSpan alone could never do that — it only
// ever adds.
func (t *AutonomySpanTracker) SyncOpenSpans(spans []outbound.AutonomySpan) error {
	rows := make(map[string]openRow, len(spans))
	for _, s := range spans {
		if s.Session == "" {
			continue
		}
		rows[s.Session] = openRowFrom(s)
	}
	t.openMu.Lock()
	defer t.openMu.Unlock()
	return t.writeOpenRows(rows)
}

// OpenSpans returns every run in the journal, ordered by start ascending so two
// reads of an unchanged journal agree (a map's iteration order does not).
//
// Each carries Running: an open run's End is where it was last SEEN, not where
// it ended, and a caller that lost that distinction would file "the daemon
// checked on it four seconds ago" as "the run finished four seconds ago".
func (t *AutonomySpanTracker) OpenSpans() ([]outbound.AutonomySpan, error) {
	t.openMu.Lock()
	rows, err := t.readOpenRows()
	t.openMu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]outbound.AutonomySpan, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.span())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].Session < out[j].Session
	})
	return out, nil
}

// clearOpenSpan drops one session's entry. Called by RecordSpan: a run that has
// been filed as closed is no longer open, and leaving the entry behind would
// make the same stretch of time appear twice — once as a finished run and once
// as one still going.
func (t *AutonomySpanTracker) clearOpenSpan(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	t.openMu.Lock()
	defer t.openMu.Unlock()
	rows, err := t.readOpenRows()
	if err != nil {
		return err
	}
	if _, ok := rows[sessionID]; !ok {
		return nil
	}
	delete(rows, sessionID)
	return t.writeOpenRows(rows)
}

// readOpenRows loads the journal. A missing file is an empty journal, not an
// error — that is every machine before the first run of the day.
//
// A journal that exists but cannot be PARSED is a different case and is treated
// as empty too, deliberately: the alternative is refusing to record anything
// until someone deletes a file, and the recovery a corrupt journal costs is at
// most the open runs, which the next sync rebuilds from the live sessions.
// Caller holds openMu.
func (t *AutonomySpanTracker) readOpenRows() (map[string]openRow, error) {
	data, err := os.ReadFile(t.openPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]openRow{}, nil
		}
		return nil, err
	}
	rows := map[string]openRow{}
	if err := json.Unmarshal(data, &rows); err != nil {
		return map[string]openRow{}, nil
	}
	return rows, nil
}

// writeOpenRows persists the journal atomically. Caller holds openMu.
//
// An EMPTY journal is written as an empty object rather than removed, so
// "nothing is running" and "no daemon has ever written here" stay
// distinguishable on disk — the same reason the span log's provenance block
// separates an empty window from an empty log.
func (t *AutonomySpanTracker) writeOpenRows(rows map[string]openRow) error {
	if err := os.MkdirAll(t.dir, 0700); err != nil {
		return fmt.Errorf("create autonomy dir: %w", err)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("marshal open autonomy runs: %w", err)
	}
	return writeFileAtomic(t.openPath(), data, 0600)
}

// openRowFrom converts a span the detector is holding open into its row. Kind
// is normalized on the way in, exactly as RecordSpan normalizes it, so a run
// adopted after a restart carries the same classification it had before.
func openRowFrom(s outbound.AutonomySpan) openRow {
	return openRow{
		Start:           s.Start,
		LastSeen:        s.End,
		Project:         s.Project,
		Session:         s.Session,
		Adapter:         s.Adapter,
		Model:           s.Model,
		Kind:            session.AutonomyKindOrUnknown(s.Kind),
		Parent:          s.Parent,
		StartLowerBound: s.StartLowerBound,
	}
}

// span converts a row back, always marked Running.
func (r openRow) span() outbound.AutonomySpan {
	return outbound.AutonomySpan{
		Start:           r.Start,
		End:             r.LastSeen,
		Project:         r.Project,
		Session:         r.Session,
		Adapter:         r.Adapter,
		Model:           r.Model,
		Kind:            session.AutonomyKindOrUnknown(r.Kind),
		Parent:          r.Parent,
		Running:         true,
		StartLowerBound: r.StartLowerBound,
		// Reason is deliberately left empty: a run that has not ended has no
		// end reason, and session.AutonomyReasonUnknown would claim it ended
		// in a way nothing could name.
	}
}

// foldOpenRun folds one in-progress run into a window result.
//
// OVERLAP, not "ended inside the window", because a run that has not ended
// cannot be asked where it ended. It is in view when it had begun before the
// window closed and was still alive after the window opened. Its end-so-far is
// clamped to the window, so a trailing window ending "now" reports the run as
// running up to now rather than up to the last heartbeat.
func foldOpenRun(s outbound.AutonomySpan, q outbound.AutonomySpanQuery, res *outbound.AutonomySpanResult) {
	if s.Start >= q.End || s.End < q.Start {
		return
	}
	if s.End > q.End {
		s.End = q.End
	}
	if s.End < s.Start {
		s.End = s.Start
	}
	countSpanKind(s.Kind, res)
	res.Spans = append(res.Spans, s)
}
