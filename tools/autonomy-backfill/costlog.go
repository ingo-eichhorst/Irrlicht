package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The cost log (<data-dir>/cost/<project>.jsonl) is the older and blunter of
// the two sources, and the only one that reaches back past the event log's
// rotation window. It has been written unconditionally since long before the
// Autonomy feature existed.
//
// What it records is WHEN a session was consuming tokens: one row per project
// per session, at most every 60 s, and only when a value changed. What it does
// NOT record — at all, in any form — is why a run stopped. Every span
// reconstructed from it therefore carries session.AutonomyReasonUnknown; see
// reconstructCostSpans.

const (
	// costGlob is where the daemon writes the cost log, relative to the data
	// dir. One file per project, JSONL, append-only.
	costGlob = "cost/*.jsonl"

	// scanBufInitial/scanBufMax size the line scanner both parsers share. A
	// cost or event line is short, but a corrupted file can present an
	// arbitrarily long "line", and the default 64 KiB limit would turn that
	// into a scan ERROR — which readEventLog correctly treats as fatal.
	scanBufInitial = 64 * 1024
	scanBufMax     = 4 * 1024 * 1024
)

// costRow is the subset of a cost-log row this tool reads. The row carries
// cumulative cost and token counters too; none of them matter here, because
// the only question being asked of this source is "was this session alive at
// this instant".
type costRow struct {
	TS      int64  `json:"ts"`
	Project string `json:"project"`
	Session string `json:"session"`
}

// sessionKey identifies one session's activity series. Project is part of the
// key, not decoration: the cost log is sharded per project, and a session id
// appearing under two projects is two series, not one interleaved one.
type sessionKey struct {
	Project string
	Session string
}

// costLog is the cost log reduced to what the reconstruction needs: one sorted
// timestamp series per session, plus the parse census.
type costLog struct {
	Series map[sessionKey][]int64
	Stats  parseStats
}

// readCostLog parses every cost file under dataDir into per-session timestamp
// series, sorted ascending and de-duplicated.
//
// De-duplication is not tidiness: the log routinely writes the same second
// twice (two counters changing in one flush), and a duplicate timestamp would
// otherwise inflate the row counts the report prints without changing a single
// span boundary — a figure that drifts away from what it measures.
func readCostLog(dataDir string) (*costLog, error) {
	paths, err := filepath.Glob(filepath.Join(dataDir, costGlob))
	if err != nil {
		return nil, fmt.Errorf("glob cost log: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no cost log under %s — this machine has nothing to reconstruct from",
			filepath.Join(dataDir, costGlob))
	}
	sort.Strings(paths)

	out := &costLog{Series: map[sessionKey][]int64{}, Stats: parseStats{Files: len(paths)}}
	for _, p := range paths {
		if err := scanCostFile(p, out); err != nil {
			return nil, err
		}
	}
	for k, series := range out.Series {
		sort.Slice(series, func(i, j int) bool { return series[i] < series[j] })
		out.Series[k] = dedupeSorted(series)
	}
	return out, nil
}

// scanCostFile folds one cost file into out.
func scanCostFile(path string, out *costLog) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, scanBufInitial), scanBufMax)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		out.Stats.Lines++
		var r costRow
		if err := json.Unmarshal(line, &r); err != nil {
			out.Stats.Malformed++
			continue
		}
		// A row with no timestamp, no project or no session cannot be placed
		// on a timeline or filed under a strip row. It is malformed FOR THIS
		// PURPOSE even though the daemon may have written it deliberately, and
		// counting it as such is what keeps a silently-changed row shape from
		// reading as a quiet month.
		if r.TS <= 0 || r.Project == "" || r.Session == "" {
			out.Stats.Malformed++
			continue
		}
		out.Stats.Parsed++
		out.Stats.Relevant++
		k := sessionKey{Project: r.Project, Session: r.Session}
		out.Series[k] = append(out.Series[k], r.TS)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// dedupeSorted collapses runs of equal values in an already-sorted slice.
func dedupeSorted(in []int64) []int64 {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
