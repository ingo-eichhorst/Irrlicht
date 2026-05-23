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
)

const (
	costDirName = "cost"

	// costWriteInterval throttles how often a new row may be appended for a
	// given session. Keeps files small when cumulative totals tick forward
	// every couple of seconds during active work.
	costWriteInterval = 60 * time.Second
)

// snapshotRow is the on-disk JSON shape for one cost-tracker line. One row
// per line (JSONL).
type snapshotRow struct {
	TS        int64   `json:"ts"`
	Project   string  `json:"project,omitempty"`  // raw SessionState.ProjectName (filename is sanitized)
	Provider  string  `json:"provider,omitempty"` // "anthropic", "openai", or "" (unknown); see providerForSession
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
		Provider:  t.providerOf(state),
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
		Provider:  t.providerOf(state),
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

// ProjectCostsInWindows returns per-timeframe cost maps in a single pass over
// each project file. The returned map keys mirror the keys of windowSeconds;
// each inner map is projectName → USD for that window. O(files × rows) once,
// regardless of how many windows are requested — the per-row aggregator
// maintains one `windowAgg` tuple per requested window.
func (t *CostTracker) ProjectCostsInWindows(windowSeconds map[string]int64) (map[string]map[string]float64, error) {
	out := make(map[string]map[string]float64, len(windowSeconds))
	for k := range windowSeconds {
		out[k] = make(map[string]float64)
	}
	if len(windowSeconds) == 0 {
		return out, nil
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	now := time.Now().Unix()
	cutoffs := make(map[string]int64, len(windowSeconds))
	for k, secs := range windowSeconds {
		cutoffs[k] = now - secs
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		fallback := strings.TrimSuffix(name, ".jsonl")
		if err := t.sumProjectWindows(filepath.Join(t.dir, name), cutoffs, fallback, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ProviderCostsInWindows mirrors ProjectCostsInWindows but buckets each
// session's contribution by its billing provider ("anthropic", "openai")
// rather than its project. A single project can mix providers (e.g. Claude
// Code + Codex in one repo), so providers cannot be re-derived from the
// per-project map client-side without double-counting. Rows with an empty
// provider (pre-schema rows, wrapper agents) are excluded from the result.
func (t *CostTracker) ProviderCostsInWindows(windowSeconds map[string]int64) (map[string]map[string]float64, error) {
	out := make(map[string]map[string]float64, len(windowSeconds))
	for k := range windowSeconds {
		out[k] = make(map[string]float64)
	}
	if len(windowSeconds) == 0 {
		return out, nil
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	now := time.Now().Unix()
	cutoffs := make(map[string]int64, len(windowSeconds))
	for k, secs := range windowSeconds {
		cutoffs[k] = now - secs
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if err := t.sumProviderWindows(filepath.Join(t.dir, e.Name()), cutoffs, out); err != nil {
			return nil, err
		}
	}
	return out, nil
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
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	agg := make(map[string]*sessionWindows)
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
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, err
	}
	return agg, nil
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

// sumProjectWindows scans a project file and buckets each session's
// contribution under its project name (falling back to the filename when a
// row carries no project).
func (t *CostTracker) sumProjectWindows(path string, cutoffs map[string]int64, fallbackName string, out map[string]map[string]float64) error {
	agg, err := t.scanWindows(path, cutoffs)
	if err != nil {
		return err
	}
	for _, s := range agg {
		key := s.project
		if key == "" {
			key = fallbackName
		}
		addContributions(s, key, out)
	}
	return nil
}

// sumProviderWindows scans a cost file and buckets each session's
// contribution under its provider, skipping sessions with no known provider.
func (t *CostTracker) sumProviderWindows(path string, cutoffs map[string]int64, out map[string]map[string]float64) error {
	agg, err := t.scanWindows(path, cutoffs)
	if err != nil {
		return err
	}
	for _, s := range agg {
		if s.provider == "" {
			continue
		}
		addContributions(s, s.provider, out)
	}
	return nil
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
