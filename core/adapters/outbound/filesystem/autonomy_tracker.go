package filesystem

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

const (
	// autonomyDirName is the span log's directory under the data dir, sibling
	// to costDirName.
	autonomyDirName = "autonomy"

	// autonomyFileExt is the on-disk extension for one project's per-line
	// JSONL span log (project+autonomyFileExt under autonomyDirName).
	autonomyFileExt = ".jsonl"

	// autonomySchemaVersion documents the on-disk row shape. v1 is the
	// original (#1905). Like costSchemaVersion the constant is documentary —
	// rows do not carry it — and for the same reason: every field is
	// omitempty-tolerant JSONL, so a row written by an older or newer daemon
	// decodes with the fields it does not know left at their zero values, and
	// there is nothing to migrate.
	autonomySchemaVersion = 1
)

// spanRow is the on-disk JSON shape for one closed autonomy span. One row per
// line (JSONL), append-only, one file per project — deliberately the same
// shape as the cost log's snapshotRow (#1905), including the omitempty
// forward-compatibility rule: a reader that meets a field it does not know
// ignores it, and a field a writer did not set reads back as the zero value
// rather than as an error.
//
// Short JSON keys because the log is the one thing here that grows without
// bound: at a few hundred spans a day, 400 days of retention is the whole
// budget this row has to fit in.
type spanRow struct {
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Project string `json:"project,omitempty"` // raw SessionState.ProjectName (the filename is sanitized)
	Session string `json:"session,omitempty"`
	Adapter string `json:"adapter,omitempty"`
	Model   string `json:"model,omitempty"`
	// Reason is one of session.AutonomyEndReasons() — the state the session
	// left `working` FOR. "" in a row written by a build that could not name
	// the state; such a row still carries a real duration, so it is kept and
	// simply ranks lowest on the strip's collapse ladder.
	//
	// session.AutonomyReasonUnknown appears here too, on a row the back-fill
	// reconstructed from a source that records activity but not outcome. It
	// is NOT a session state and never becomes one.
	Reason string `json:"reason,omitempty"`

	// Source is empty on every row the daemon wrote — absence IS the
	// live-measured case, which is what lets the back-fill append into an
	// existing log without rewriting a single row already in it. Set only by
	// tools/autonomy-backfill, to one of session.AutonomySources().
	Source string `json:"source,omitempty"`
}

// AutonomySpanTracker persists closed autonomy spans in append-only JSONL
// files, one file per project, under <dataDir>/autonomy/.
//
// It mirrors CostTracker's storage model on purpose (#1905): written
// unconditionally rather than behind the opt-in --record flag, pruned to the
// same 400 days at the same startup call site, and rewritten by the same
// atomic prune helper. The alternative — deriving spans from the lifecycle
// recordings — silently shows nothing to every user who is not recording,
// which for this feature is indistinguishable from "you never ran anything".
type AutonomySpanTracker struct {
	dir string

	// mu guards fileMus.
	mu      sync.Mutex
	fileMus map[string]*sync.Mutex // sanitized project name → per-file write mutex
}

// NewAutonomySpanTracker returns a tracker rooted at the user's Application
// Support directory. The directory is created on the first write.
func NewAutonomySpanTracker() (*AutonomySpanTracker, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return NewAutonomySpanTrackerWithDir(filepath.Join(homeDir, appSupportDir, autonomyDirName)), nil
}

// NewAutonomySpanTrackerWithDir returns a tracker rooted at the given
// directory (used by the daemon's IRRLICHT_HOME isolation, by tests, and by
// tools/seed-autonomy-spans).
func NewAutonomySpanTrackerWithDir(dir string) *AutonomySpanTracker {
	return &AutonomySpanTracker{
		dir:     dir,
		fileMus: make(map[string]*sync.Mutex),
	}
}

// Dir returns the directory where span files live.
func (t *AutonomySpanTracker) Dir() string { return t.dir }

// fileMu returns (creating if needed) the per-project write mutex.
func (t *AutonomySpanTracker) fileMu(project string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	fm, ok := t.fileMus[project]
	if !ok {
		fm = &sync.Mutex{}
		t.fileMus[project] = fm
	}
	return fm
}

// RecordSpan appends one closed span to its project's log.
//
// No-ops (without an error) on a span with no project or no positive
// duration: a span with nothing to file it under cannot be drawn on a
// per-project strip, and a zero-length span is a transition artefact, not a
// run. Matches RecordSnapshot's "nothing useful to store" rule.
func (t *AutonomySpanTracker) RecordSpan(span outbound.AutonomySpan) error {
	project := projectKey(span.Project)
	if project == "" || span.Duration() <= 0 {
		return nil
	}
	row := spanRow{
		Start:   span.Start,
		End:     span.End,
		Project: span.Project,
		Session: span.Session,
		Adapter: span.Adapter,
		Model:   span.Model,
		Reason:  span.Reason,
		Source:  span.Source,
	}
	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("marshal autonomy span: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(t.dir, 0700); err != nil {
		return fmt.Errorf("create autonomy dir: %w", err)
	}

	fm := t.fileMu(project)
	fm.Lock()
	defer fm.Unlock()
	f, err := os.OpenFile(t.filePath(project), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open autonomy file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("append autonomy span: %w", err)
	}
	return nil
}

// SpansInWindow returns every span that ENDED inside [q.Start, q.End), ordered
// by start ascending, plus two log-wide facts the honesty rules need: the
// earliest start on record and the total number of rows.
//
// Those two are computed over the WHOLE log, not the window, and that is the
// point: an empty window is ambiguous on its own ("nothing ran" vs "this
// feature had not shipped yet"), and only the earliest recorded span
// disambiguates it (#1905).
func (t *AutonomySpanTracker) SpansInWindow(q outbound.AutonomySpanQuery) (*outbound.AutonomySpanResult, error) {
	res := &outbound.AutonomySpanResult{
		Spans: []outbound.AutonomySpan{},
		// Never nil, even on a missing log: a caller reading an era out of a
		// nil map gets a zero either way, but a caller WRITING one would panic,
		// and "the log does not exist yet" is the most common path here.
		Provenance: outbound.AutonomySpanProvenance{EraStarts: map[string]int64{}},
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), autonomyFileExt) {
			continue
		}
		fallback := strings.TrimSuffix(e.Name(), autonomyFileExt)
		if err := scanSpanFile(filepath.Join(t.dir, e.Name()), func(r spanRow) {
			foldSpanRow(r, fallback, q, res)
		}); err != nil {
			return nil, err
		}
	}
	sort.Slice(res.Spans, func(i, j int) bool {
		if res.Spans[i].Start != res.Spans[j].Start {
			return res.Spans[i].Start < res.Spans[j].Start
		}
		return res.Spans[i].Session < res.Spans[j].Session
	})
	if q.Limit > 0 && len(res.Spans) > q.Limit {
		res.Spans = res.Spans[:q.Limit]
		res.Truncated = true
	}
	// Counted AFTER the limit clips the result, and that order is the point:
	// these two numbers are what the clients print as "N of the runs in view
	// are reconstructed", so they have to describe the spans actually
	// returned. Counted during the fold instead, a clipped window would
	// report reconstructions the caller never received.
	countSpanProvenance(res)
	return res, nil
}

// countSpanProvenance fills the window-scoped half of the provenance block
// from the spans the query is actually returning. LiveSince is NOT computed
// here: it is log-wide by definition and is accumulated over every row during
// the fold, including rows outside the window.
func countSpanProvenance(res *outbound.AutonomySpanResult) {
	res.Provenance.Reconstructed = 0
	res.Provenance.CostDerived = 0
	for _, s := range res.Spans {
		if !session.IsAutonomyReconstructed(s.Source) {
			continue
		}
		res.Provenance.Reconstructed++
		if s.Source == session.AutonomySourceCost {
			res.Provenance.CostDerived++
		}
	}
}

// foldSpanRow folds one parsed row into the running result: it always counts
// towards the log-wide total and earliest-start, and joins Spans only when it
// ended inside the query window.
func foldSpanRow(r spanRow, fallback string, q outbound.AutonomySpanQuery, res *outbound.AutonomySpanResult) {
	res.TotalRecorded++
	if r.Start > 0 && (res.EarliestStart == 0 || r.Start < res.EarliestStart) {
		res.EarliestStart = r.Start
	}
	// Era starts are log-wide, exactly like EarliestStart and for the same
	// reason: "everything before this date came from a different source" is a
	// claim about the whole log, and a window that happens to hold one source
	// cannot be allowed to answer it.
	//
	// Keyed by the row's own Source, including "" for a measured row, so a
	// source this build does not recognize still gets an era instead of being
	// folded into someone else's.
	if r.Start > 0 {
		if cur, ok := res.Provenance.EraStarts[r.Source]; !ok || r.Start < cur {
			res.Provenance.EraStarts[r.Source] = r.Start
		}
	}
	if r.End < q.Start || r.End >= q.End {
		return
	}
	project := r.Project
	if project == "" {
		project = fallback
	}
	res.Spans = append(res.Spans, outbound.AutonomySpan{
		Start:   r.Start,
		End:     r.End,
		Project: project,
		Session: r.Session,
		Adapter: r.Adapter,
		Model:   r.Model,
		Reason:  r.Reason,
		Source:  r.Source,
	})
}

// scanSpanFile streams one span file, invoking perRow for each parsed row. A
// missing file and malformed lines are skipped — the same policy scanCostFile
// applies to the cost log, for the same reason: one truncated tail line (a
// crash mid-append) must not blind the reader to everything before it.
func scanSpanFile(path string, perRow func(spanRow)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, jsonlScanBufInitial), jsonlScanBufMax)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r spanRow
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		perRow(r)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// Prune rewrites each project file to drop spans that ENDED more than
// olderThanDays ago. olderThanDays <= 0 is a no-op.
//
// Called from the same startup site, with the same 400 days, as the cost
// log's prune — see initAutonomyTracker.
func (t *AutonomySpanTracker) Prune(olderThanDays int) error {
	if olderThanDays <= 0 {
		return nil
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -olderThanDays).Unix()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), autonomyFileExt) {
			continue
		}
		project := strings.TrimSuffix(e.Name(), autonomyFileExt)
		fm := t.fileMu(project)
		fm.Lock()
		err := pruneJSONLFile(filepath.Join(t.dir, e.Name()), func(line []byte) bool {
			var r spanRow
			if err := json.Unmarshal(line, &r); err != nil {
				return false
			}
			return r.End >= cutoff
		})
		fm.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *AutonomySpanTracker) filePath(project string) string {
	return filepath.Join(t.dir, project+autonomyFileExt)
}
