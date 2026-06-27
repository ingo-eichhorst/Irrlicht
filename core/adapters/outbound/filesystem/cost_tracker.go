package filesystem

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

const (
	costDirName = "cost"

	// costWriteInterval throttles how often a new row may be appended for a
	// given session. Keeps files small when cumulative totals tick forward
	// every couple of seconds during active work.
	costWriteInterval = 60 * time.Second

	// costSchemaVersion documents the on-disk row shape. v2 (#750) added the
	// branch and model columns; the format is forward/backward compatible
	// JSONL with omitempty, so pre-v2 rows simply decode with empty branch and
	// model (read back as "unknown" for those group axes) — no migration and
	// no daemon-restart requirement. The constant is documentary; rows do not
	// carry it.
	costSchemaVersion = 2
)

// snapshotRow is the on-disk JSON shape for one cost-tracker line. One row
// per line (JSONL).
type snapshotRow struct {
	TS        int64   `json:"ts"`
	Project   string  `json:"project,omitempty"`  // raw SessionState.ProjectName (filename is sanitized)
	Branch    string  `json:"branch,omitempty"`   // SessionState.GitBranch ("" = detached/unknown); see #750
	Provider  string  `json:"provider,omitempty"` // "anthropic", "openai", or "" (unknown); see providerForSession
	Model     string  `json:"model,omitempty"`    // SessionState.Model ("" = unknown)
	Session   string  `json:"session"`
	Cost      float64 `json:"cost"`
	CumIn     int64   `json:"cum_in,omitempty"`
	CumOut    int64   `json:"cum_out,omitempty"`
	CumRead   int64   `json:"cum_read,omitempty"`
	CumCreate int64   `json:"cum_create,omitempty"`
}

// providerForSession maps a session's source adapter to the billing provider
// whose subscription/usage its cost draws from. Only first-party CLIs are
// mapped today: wrapper agents (pi, opencode) resolve their provider
// dynamically via rate-limit inheritance at request time, which isn't
// available here at write time, so they record "" (unknown). Rows written
// before this field existed also read back as "". Such rows are excluded from
// the per-provider rollup but still counted in the per-project totals.
func providerForSession(state *session.SessionState) string {
	switch state.Adapter {
	case "claude-code":
		return "anthropic"
	case "codex":
		return "openai"
	default:
		return ""
	}
}

// CostTracker persists per-session cost snapshots in append-only JSONL files,
// one file per project, under <appSupport>/cost/.
type CostTracker struct {
	dir string

	// providerOf resolves a session's billing provider for the snapshot row.
	// Defaults to providerForSession (first-party adapters only); the daemon
	// injects a resolver that also attributes wrapper agents — see
	// SetProviderResolver.
	providerOf func(*session.SessionState) string

	// mu guards fileMus and lastWrite.
	mu        sync.Mutex
	fileMus   map[string]*sync.Mutex // projectName → per-file write mutex
	lastWrite map[string]snapshotRow // sessionID → last row we wrote
}

// NewCostTracker returns a tracker rooted at the user's Application Support
// directory. The directory is created on the first write.
func NewCostTracker() (*CostTracker, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return NewCostTrackerWithDir(filepath.Join(homeDir, appSupportDir, costDirName)), nil
}

// NewCostTrackerWithDir returns a tracker rooted at the given directory
// (useful for tests).
func NewCostTrackerWithDir(dir string) *CostTracker {
	return &CostTracker{
		dir:        dir,
		providerOf: providerForSession,
		fileMus:    make(map[string]*sync.Mutex),
		lastWrite:  make(map[string]snapshotRow),
	}
}

// Dir returns the directory where cost files live.
func (t *CostTracker) Dir() string { return t.dir }

// SetProviderResolver overrides how snapshot rows are attributed to a billing
// provider. The daemon injects a resolver backed by services.ProviderForSession
// so wrapper agents (pi, opencode) attribute to the subscription they inherit;
// the built-in default handles only first-party adapters. Call once at wiring
// time — not safe to call concurrently with RecordSnapshot.
func (t *CostTracker) SetProviderResolver(fn func(*session.SessionState) string) {
	if fn != nil {
		t.providerOf = fn
	}
}

// RecordSnapshot appends a row for the session if cost or any cumulative
// token count has changed since the last stored row, and at least
// costWriteInterval has elapsed since that row. No-ops on sessions without
// metrics or a project name — nothing useful to store.
func (t *CostTracker) RecordSnapshot(state *session.SessionState) error {
	if state == nil || state.Metrics == nil {
		return nil
	}
	project := projectKey(state.ProjectName)
	if project == "" {
		return nil
	}
	m := state.Metrics

	row := snapshotRow{
		TS:        time.Now().Unix(),
		Project:   state.ProjectName,
		Branch:    state.GitBranch,
		Provider:  t.providerOf(state),
		Model:     state.Model,
		Session:   state.SessionID,
		Cost:      m.EstimatedCostUSD,
		CumIn:     m.CumInputTokens,
		CumOut:    m.CumOutputTokens,
		CumRead:   m.CumCacheReadTokens,
		CumCreate: m.CumCacheCreationTokens,
	}

	t.mu.Lock()
	prev, hasPrev := t.lastWrite[state.SessionID]
	if hasPrev {
		unchanged := prev.Cost == row.Cost &&
			prev.CumIn == row.CumIn &&
			prev.CumOut == row.CumOut &&
			prev.CumRead == row.CumRead &&
			prev.CumCreate == row.CumCreate
		if unchanged {
			t.mu.Unlock()
			return nil
		}
		if row.TS-prev.TS < int64(costWriteInterval/time.Second) {
			t.mu.Unlock()
			return nil
		}
	}
	fm, ok := t.fileMus[project]
	if !ok {
		fm = &sync.Mutex{}
		t.fileMus[project] = fm
	}
	t.lastWrite[state.SessionID] = row
	t.mu.Unlock()

	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(t.dir, 0700); err != nil {
		return fmt.Errorf("create cost dir: %w", err)
	}

	fm.Lock()
	defer fm.Unlock()
	f, err := os.OpenFile(t.filePath(project), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open cost file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("append snapshot: %w", err)
	}
	return nil
}

// RecordBaseline writes a single snapshot row for an existing session using
// FirstSeen as the timestamp. Used on daemon startup so the table has a
// starting point for sessions that predate the tracker.
func (t *CostTracker) RecordBaseline(state *session.SessionState) error {
	if state == nil || state.Metrics == nil {
		return nil
	}
	project := projectKey(state.ProjectName)
	if project == "" {
		return nil
	}
	m := state.Metrics
	ts := state.FirstSeen
	if ts == 0 {
		ts = time.Now().Unix()
	}
	row := snapshotRow{
		TS:        ts,
		Project:   state.ProjectName,
		Branch:    state.GitBranch,
		Provider:  t.providerOf(state),
		Model:     state.Model,
		Session:   state.SessionID,
		Cost:      m.EstimatedCostUSD,
		CumIn:     m.CumInputTokens,
		CumOut:    m.CumOutputTokens,
		CumRead:   m.CumCacheReadTokens,
		CumCreate: m.CumCacheCreationTokens,
	}

	t.mu.Lock()
	if _, exists := t.lastWrite[state.SessionID]; exists {
		t.mu.Unlock()
		return nil
	}
	fm, ok := t.fileMus[project]
	if !ok {
		fm = &sync.Mutex{}
		t.fileMus[project] = fm
	}
	t.lastWrite[state.SessionID] = row
	t.mu.Unlock()

	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("marshal baseline: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(t.dir, 0700); err != nil {
		return fmt.Errorf("create cost dir: %w", err)
	}

	fm.Lock()
	defer fm.Unlock()
	f, err := os.OpenFile(t.filePath(project), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open cost file: %w", err)
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

// projectCostsInWindow is a test convenience around ProjectCostsInWindows
// for a single trailing window. Not part of the CostTracker port — the
// handler uses the plural form directly.
func (t *CostTracker) projectCostsInWindow(windowSeconds int64) (map[string]float64, error) {
	const k = "w"
	all, err := t.ProjectCostsInWindows(map[string]int64{k: windowSeconds})
	if err != nil {
		return nil, err
	}
	return all[k], nil
}

// ProjectCostsInWindows returns per-timeframe cost maps keyed by project name.
// Convenience wrapper around CostsInWindows for callers (and tests) that only
// need the project axis.
func (t *CostTracker) ProjectCostsInWindows(windowSeconds map[string]int64) (map[string]map[string]float64, error) {
	byProject, _, err := t.CostsInWindows(windowSeconds)
	return byProject, err
}

// ProviderCostsInWindows returns per-timeframe cost maps keyed by billing
// provider. Convenience wrapper around CostsInWindows for callers (and tests)
// that only need the provider axis.
func (t *CostTracker) ProviderCostsInWindows(windowSeconds map[string]int64) (map[string]map[string]float64, error) {
	_, byProvider, err := t.CostsInWindows(windowSeconds)
	return byProvider, err
}

// CostsInWindows returns per-timeframe cost maps bucketed by project AND by
// provider in a single pass over each cost file: byProject keys each inner map
// by project name (falling back to the filename when a row carries no
// project); byProvider keys by billing provider ("anthropic"/"openai"),
// excluding rows with no known provider (pre-schema rows, unattributed
// wrappers). A project can mix providers, so the provider axis can't be
// re-derived from the project map client-side without double-counting — and
// the sessions handler needs both for one response, so computing them together
// halves the I/O vs. two separate scans. O(files × rows) once, regardless of
// how many windows are requested.
func (t *CostTracker) CostsInWindows(windowSeconds map[string]int64) (byProject, byProvider map[string]map[string]float64, err error) {
	byProject = newWindowMap(windowSeconds)
	byProvider = newWindowMap(windowSeconds)
	if len(windowSeconds) == 0 {
		return byProject, byProvider, nil
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return byProject, byProvider, nil
		}
		return nil, nil, err
	}
	cutoffs := cutoffsFor(windowSeconds)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		agg, err := t.scanWindows(filepath.Join(t.dir, e.Name()), cutoffs)
		if err != nil {
			return nil, nil, err
		}
		fallback := strings.TrimSuffix(e.Name(), ".jsonl")
		for _, s := range agg {
			projectKey := s.project
			if projectKey == "" {
				projectKey = fallback
			}
			addContributions(s, projectKey, byProject)
			if s.provider != "" {
				addContributions(s, s.provider, byProvider)
			}
		}
	}
	return byProject, byProvider, nil
}

// newWindowMap allocates an outer timeframe→inner-map structure with one empty
// inner map per requested window.
func newWindowMap(windowSeconds map[string]int64) map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(windowSeconds))
	for k := range windowSeconds {
		out[k] = make(map[string]float64)
	}
	return out
}

// cutoffsFor converts trailing-window durations into absolute cutoff
// timestamps anchored at a single `now`.
func cutoffsFor(windowSeconds map[string]int64) map[string]int64 {
	now := time.Now().Unix()
	cutoffs := make(map[string]int64, len(windowSeconds))
	for k, secs := range windowSeconds {
		cutoffs[k] = now - secs
	}
	return cutoffs
}

// windowAgg accumulates the baseline/max needed to compute one session's
// contribution to one trailing window. Shared by the project and provider
// rollups via scanWindows.
type windowAgg struct {
	baseline       float64
	hasBaseline    bool
	baselineInside bool
	max            float64
	hasMax         bool
}

// sessionWindows holds a single session's per-timeframe aggregators plus the
// project and provider it belongs to (both constant across a session's rows).
type sessionWindows struct {
	project  string
	provider string
	windows  map[string]*windowAgg
}

// scanWindows streams one cost file once and returns per-session window
// aggregators. The same scan feeds both the per-project and per-provider
// rollups; callers pick which key to bucket by. Each timeframe's contribution
// follows the same rules as the single-window case:
//   - baseline = cost at the row just before cutoff if one exists, otherwise
//     the minimum cost observed inside the window.
//   - contribution = max(0, MAX(cost) − baseline).
func (t *CostTracker) scanWindows(path string, cutoffs map[string]int64) (map[string]*sessionWindows, error) {
	agg := make(map[string]*sessionWindows)
	err := scanCostFile(path, func(r snapshotRow) {
		s := agg[r.Session]
		if s == nil {
			s = &sessionWindows{windows: make(map[string]*windowAgg, len(cutoffs))}
			for k := range cutoffs {
				s.windows[k] = &windowAgg{}
			}
			agg[r.Session] = s
		}
		if r.Project != "" {
			s.project = r.Project
		}
		if r.Provider != "" {
			s.provider = r.Provider
		}
		for k, cutoff := range cutoffs {
			w := s.windows[k]
			if r.TS < cutoff {
				w.baseline = r.Cost
				w.hasBaseline = true
				w.baselineInside = false
				continue
			}
			if !w.hasBaseline {
				w.baseline = r.Cost
				w.hasBaseline = true
				w.baselineInside = true
			} else if w.baselineInside && r.Cost < w.baseline {
				w.baseline = r.Cost
			}
			if !w.hasMax || r.Cost > w.max {
				w.max = r.Cost
				w.hasMax = true
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return agg, nil
}

// scanCostFile streams one cost file, invoking perRow for each parsed snapshot
// row. A missing file and malformed lines are skipped. Shared by scanWindows
// (trailing-window baseline/max) and scanSeries (per-bucket deltas) so the
// on-disk format, line-buffer sizing, and skip policy live in one place — the
// two row folds must stay in lockstep for a series' sum to match its window
// total.
func scanCostFile(path string, perRow func(snapshotRow)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r snapshotRow
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

// addContributions folds one session's per-window deltas into out under key.
func addContributions(s *sessionWindows, key string, out map[string]map[string]float64) {
	for tf, w := range s.windows {
		if !w.hasMax {
			continue
		}
		d := w.max - w.baseline
		if d <= 0 {
			continue
		}
		out[tf][key] += d
	}
}

// seriesAgg tracks one session's running cumulative values while a cost file is
// scanned, so the next in-range row's delta can be measured. prev is the
// previous cumulative value of the selected metric; prevIn/prevOut/prevCache
// back the tokens in/out/cache split. Unlike Phase 1, deltas are attributed to
// the group key on the row that closes each interval (see scanSeries), so a
// session that changes branch/model/provider mid-flight splits correctly.
type seriesAgg struct {
	prev      float64
	hasPrev   bool
	prevIn    float64
	prevOut   float64
	prevCache float64
}

// advance seeds/updates a session's running baselines to the current row's
// values. A method (not a per-row closure) so the scan loop allocates nothing.
func (s *seriesAgg) advance(cur, in, out, cache float64) {
	s.prev = cur
	s.prevIn, s.prevOut, s.prevCache = in, out, cache
	s.hasPrev = true
}

// maxSeriesBuckets caps how many buckets CostSeries will allocate, so a wide
// span paired with a tiny bucket (e.g. a custom range with ?bucket=1) can't
// force a multi-gigabyte allocation. When the requested granularity would
// exceed it, the bucket is coarsened to keep [start, end) covered within the
// ceiling; the result reports the BucketSeconds actually used.
const maxSeriesBuckets = 10000

// rowKey returns a row's value on the requested group axis. For project an
// empty value falls back to the filename (matching CostsInWindows); the other
// axes return "" for a missing value, which the handler buckets as "unknown".
func rowKey(r snapshotRow, group, fallback string) string {
	switch group {
	case "branch":
		return r.Branch
	case "provider":
		return r.Provider
	case "model":
		return r.Model
	case "session":
		return r.Session
	default: // project
		if r.Project != "" {
			return r.Project
		}
		return fallback
	}
}

// rowMetric returns a row's cumulative value for the selected metric: total
// tokens (in + out + cache) for "tokens", else estimated USD cost.
func rowMetric(r snapshotRow, metric string) float64 {
	if metric == "tokens" {
		return float64(r.CumIn + r.CumOut + r.CumRead + r.CumCreate)
	}
	return r.Cost
}

// CostSeries returns an incremental time series bucketed into fixed
// bucketSeconds-wide buckets spanning [Start, End) (unix seconds), keyed by the
// query's group axis, plus each key's total over the range. A session's
// increment in a bucket is the increase in its cumulative metric across the
// rows that fall in that bucket, attributed to the group value on the row that
// closes the interval; the last row before Start (or, failing that, the first
// in-range row) seeds the baseline so pre-range cost is never attributed to the
// first bucket. Because snapshot values are monotonic, summing a key's buckets
// yields the same (max − baseline) total CostsInWindows computes for the
// matching trailing window. One pass over every cost file.
func (t *CostTracker) CostSeries(q outbound.SeriesQuery) (*outbound.CostSeriesResult, error) {
	start, end, bucketSeconds := q.Start, q.End, q.BucketSeconds
	if bucketSeconds <= 0 || end <= start {
		return &outbound.CostSeriesResult{
			Start:         start,
			End:           end,
			BucketSeconds: bucketSeconds,
			BucketStarts:  []int64{},
			ByKey:         map[string][]float64{},
			Totals:        map[string]float64{},
		}, nil
	}
	// Bound the bucket count: coarsen the bucket if the span would otherwise
	// blow past the ceiling, keeping the allocation (and JSON payload) bounded
	// regardless of caller-supplied start/end/bucket.
	if span := end - start; span/bucketSeconds+1 > maxSeriesBuckets {
		bucketSeconds = (span + maxSeriesBuckets - 1) / maxSeriesBuckets
	}
	n := int((end - start + bucketSeconds - 1) / bucketSeconds)
	out := &outbound.CostSeriesResult{
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		BucketStarts:  make([]int64, n),
		ByKey:         map[string][]float64{},
		Totals:        map[string]float64{},
	}
	if q.Metric == "tokens" {
		out.TokenSplit = &outbound.TokenSplit{}
	}
	for i := range out.BucketStarts {
		out.BucketStarts[i] = start + int64(i)*bucketSeconds
	}

	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fallback := strings.TrimSuffix(e.Name(), ".jsonl")
		if err := t.scanSeries(filepath.Join(t.dir, e.Name()), q, bucketSeconds, n, fallback, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanSeries streams one cost file once and folds its sessions' per-bucket
// increments into out, keyed by the group value on each interval's end row and
// filtered to the query's scope. A row whose group value is empty is keyed
// under "" (the unknown bucket); for the project axis the empty value falls
// back to the filename, mirroring CostsInWindows.
func (t *CostTracker) scanSeries(path string, q outbound.SeriesQuery, bucketSeconds int64, n int, fallback string, out *outbound.CostSeriesResult) error {
	aggs := make(map[string]*seriesAgg)
	return scanCostFile(path, func(r snapshotRow) {
		if q.ScopeField != "" && rowKey(r, q.ScopeField, fallback) != q.ScopeValue {
			return
		}
		s := aggs[r.Session]
		if s == nil {
			s = &seriesAgg{}
			aggs[r.Session] = s
		}
		cur := rowMetric(r, q.Metric)
		curIn := float64(r.CumIn)
		curOut := float64(r.CumOut)
		curCache := float64(r.CumRead + r.CumCreate)

		switch {
		case r.TS < q.Start:
			// Pre-range row: advance the baseline so the first in-range delta
			// measures spend since the last snapshot.
			s.advance(cur, curIn, curOut, curCache)
			return
		case r.TS >= q.End:
			return
		case !s.hasPrev:
			// First observation with no pre-range baseline: seed, no delta.
			s.advance(cur, curIn, curOut, curCache)
			return
		}

		// The guards above filter rows to [Start, End), so the index is always
		// within [0, n-1] — no clamp needed.
		idx := int((r.TS - q.Start) / bucketSeconds)
		if d := cur - s.prev; d > 0 {
			key := rowKey(r, q.Group, fallback)
			dst := out.ByKey[key]
			if dst == nil {
				dst = make([]float64, n)
				out.ByKey[key] = dst
			}
			dst[idx] += d
			out.Totals[key] += d
		}
		if out.TokenSplit != nil {
			if d := curIn - s.prevIn; d > 0 {
				out.TokenSplit.Input += d
			}
			if d := curOut - s.prevOut; d > 0 {
				out.TokenSplit.Output += d
			}
			if d := curCache - s.prevCache; d > 0 {
				out.TokenSplit.Cache += d
			}
		}
		s.advance(cur, curIn, curOut, curCache)
	})
}

// Prune rewrites each project file to drop rows older than olderThanDays.
// olderThanDays <= 0 is a no-op.
func (t *CostTracker) Prune(olderThanDays int) error {
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
	cutoff := time.Now().Add(-time.Duration(olderThanDays) * 24 * time.Hour).Unix()
	// Collect session IDs that survive pruning so we can opportunistically
	// drop lastWrite entries for sessions that no longer appear in any
	// file (e.g. whose baseline row was older than olderThanDays).
	survivors := make(map[string]struct{})
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		project := strings.TrimSuffix(e.Name(), ".jsonl")
		if err := t.pruneFile(filepath.Join(t.dir, e.Name()), project, cutoff, survivors); err != nil {
			return err
		}
	}
	t.mu.Lock()
	for sid := range t.lastWrite {
		if _, ok := survivors[sid]; !ok {
			delete(t.lastWrite, sid)
		}
	}
	t.mu.Unlock()
	return nil
}

func (t *CostTracker) pruneFile(path, project string, cutoff int64, survivors map[string]struct{}) error {
	t.mu.Lock()
	fm, ok := t.fileMus[project]
	if !ok {
		fm = &sync.Mutex{}
		t.fileMus[project] = fm
	}
	t.mu.Unlock()

	fm.Lock()
	defer fm.Unlock()

	in, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()

	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	kept := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r snapshotRow
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if r.TS < cutoff {
			continue
		}
		if _, err := w.Write(line); err != nil {
			out.Close()
			os.Remove(tmpPath)
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			out.Close()
			os.Remove(tmpPath)
			return err
		}
		if r.Session != "" {
			survivors[r.Session] = struct{}{}
		}
		kept++
	}
	if err := scanner.Err(); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := w.Flush(); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if kept == 0 {
		os.Remove(tmpPath)
		return os.Remove(path)
	}
	return os.Rename(tmpPath, path)
}

func (t *CostTracker) filePath(project string) string {
	return filepath.Join(t.dir, project+".jsonl")
}

// unsafeFileCharRe matches characters not allowed in a project filename.
// Project names come from folder basenames and may contain slashes or other
// unusual chars on odd systems; replace anything non-safe with '_'.
var unsafeFileCharRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func projectKey(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return unsafeFileCharRe.ReplaceAllString(name, "_")
}
