package validate

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// defaultTolerancePct is the soft-diff band for cost/token drift vs the prior
// recording when the spec doesn't set one. Live agent runs jitter run-to-run,
// so equality would flap; 50% catches order-of-magnitude regressions without
// firing on normal variance.
const defaultTolerancePct = 50.0

// replaySummary is the metric vector the offline replay computes into each
// recording's *.replay.json.golden `summary` block. This is where token / cost
// / model live (the daemon's events.jsonl is lifecycle-only).
type replaySummary struct {
	EstimatedCostUSD       float64 `json:"estimated_cost_usd"`
	CumInputTokens         int64   `json:"cum_input_tokens"`
	CumOutputTokens        int64   `json:"cum_output_tokens"`
	CumCacheReadTokens     int64   `json:"cum_cache_read_tokens"`
	CumCacheCreationTokens int64   `json:"cum_cache_creation_tokens"`
	ModelName              string  `json:"model_name"`

	// Store-derived context vector (#766): present only for sessions whose tokens
	// come from an out-of-band store (antigravity #719), so they're distinct from
	// the cum_* usage above. Zero/absent for every cum-token adapter.
	TotalTokens        int64   `json:"total_tokens"`
	ContextWindow      int64   `json:"context_window"`
	ContextUtilization float64 `json:"context_utilization_percentage"`
}

func (s replaySummary) totalTokens() int64 { return s.CumInputTokens + s.CumOutputTokens }

// ObsAssert is one hard metric assertion from the spec's observations block.
type ObsAssert struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	OK       bool   `json:"ok"`
}

// ObsDrift is one soft-diff finding: a metric that moved vs the prior recording
// beyond tolerance (categorical: any change; numeric: > TolerancePct). Drifts
// are reported but never fail the run (live jitter is expected).
type ObsDrift struct {
	Field    string  `json:"field"`
	Prior    string  `json:"prior"`
	Current  string  `json:"current"`
	PctDelta float64 `json:"pct_delta,omitempty"`
}

// ObservationReport is the result of comparing a recording's metric vector
// against the spec (hard) and the prior recording (soft). Pass is false only
// when a hard assertion fails; drifts never flip it.
type ObservationReport struct {
	Pass    bool        `json:"pass"`
	Skipped bool        `json:"skipped,omitempty"` // no golden to read
	Note    string      `json:"note,omitempty"`
	Asserts []ObsAssert `json:"asserts,omitempty"`
	Drifts  []ObsDrift  `json:"drifts,omitempty"`
}

// sortedRecordingDirs returns the cell's recording dirs newest-first (names are
// timestamp-prefixed, so reverse-lexical == reverse-chronological).
func sortedRecordingDirs(scenarioDir string) []string {
	entries, err := os.ReadDir(filepath.Join(scenarioDir, "recordings"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(scenarioDir, "recordings", n)
	}
	return out
}

// readGoldenSummary reads the `summary` block of the *.replay.json.golden in a
// recording dir. ok is false when no golden is present or it doesn't parse.
func readGoldenSummary(recDir string) (replaySummary, bool) {
	matches, _ := filepath.Glob(filepath.Join(recDir, "*.replay.json.golden"))
	if len(matches) == 0 {
		return replaySummary{}, false
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return replaySummary{}, false
	}
	var doc struct {
		Summary replaySummary `json:"summary"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return replaySummary{}, false
	}
	return doc.Summary, true
}

// loadObservationSpec reads the optional observations block from a cell's
// expected.jsonl meta line. Absent file/block → nil (soft-diff only).
func loadObservationSpec(scenarioDir string) *ObservationSpec {
	b, err := os.ReadFile(filepath.Join(scenarioDir, "expected.jsonl"))
	if err != nil {
		return nil
	}
	for _, line := range splitLines(b) {
		if len(line) == 0 {
			continue
		}
		var m ExpectedMeta
		if json.Unmarshal(line, &m) == nil && m.SchemaVersion != 0 {
			return m.Observations
		}
		break // first non-empty line is the meta line
	}
	return nil
}

// ValidateObservations runs the go-test-style metric verify for a cell: it
// reads the newest recording's replay summary, hard-asserts the spec's
// observations block (exact model / nonzero cost+tokens), and soft-diffs the
// FULL vector against the prior recording within tolerance — so a cost/model/
// token regression is surfaced even for a scenario whose spec doesn't assert
// that field. No newest golden → Skipped (Pass=true).
func ValidateObservations(scenarioDir string) (*ObservationReport, error) {
	rep := &ObservationReport{Pass: true}
	dirs := sortedRecordingDirs(scenarioDir)
	if len(dirs) == 0 {
		rep.Skipped, rep.Note = true, "no recordings"
		return rep, nil
	}
	cur, ok := readGoldenSummary(dirs[0])
	if !ok {
		rep.Skipped, rep.Note = true, "newest recording has no replay golden"
		return rep, nil
	}

	// Hard assertions from the spec.
	if spec := loadObservationSpec(scenarioDir); spec != nil {
		if spec.Model != "" {
			ok := cur.ModelName == spec.Model
			rep.Asserts = append(rep.Asserts, ObsAssert{"model", spec.Model, cur.ModelName, ok})
			rep.Pass = rep.Pass && ok
		}
		if spec.CostNonzero {
			ok := cur.EstimatedCostUSD > 0
			rep.Asserts = append(rep.Asserts, ObsAssert{"cost_usd", ">0", fmt.Sprintf("%g", cur.EstimatedCostUSD), ok})
			rep.Pass = rep.Pass && ok
		}
		if spec.TokensNonzero {
			ok := cur.totalTokens() > 0
			rep.Asserts = append(rep.Asserts, ObsAssert{"tokens", ">0", fmt.Sprintf("%d", cur.totalTokens()), ok})
			rep.Pass = rep.Pass && ok
		}
		if spec.TotalTokensNonzero {
			ok := cur.TotalTokens > 0
			rep.Asserts = append(rep.Asserts, ObsAssert{"total_tokens", ">0", fmt.Sprintf("%d", cur.TotalTokens), ok})
			rep.Pass = rep.Pass && ok
		}
		if spec.ContextWindowNonzero {
			ok := cur.ContextWindow > 0
			rep.Asserts = append(rep.Asserts, ObsAssert{"context_window", ">0", fmt.Sprintf("%d", cur.ContextWindow), ok})
			rep.Pass = rep.Pass && ok
		}
		if spec.ContextUtilizationNonzero {
			ok := cur.ContextUtilization > 0
			rep.Asserts = append(rep.Asserts, ObsAssert{"context_utilization", ">0", fmt.Sprintf("%g", cur.ContextUtilization), ok})
			rep.Pass = rep.Pass && ok
		}
	}

	// Soft-diff the full vector vs the prior recording.
	tol := defaultTolerancePct
	if spec := loadObservationSpec(scenarioDir); spec != nil && spec.TolerancePct > 0 {
		tol = spec.TolerancePct
	}
	if len(dirs) > 1 {
		if prior, ok := readGoldenSummary(dirs[1]); ok {
			if cur.ModelName != prior.ModelName {
				rep.Drifts = append(rep.Drifts, ObsDrift{Field: "model", Prior: prior.ModelName, Current: cur.ModelName})
			}
			addNumDrift(rep, "cost_usd", prior.EstimatedCostUSD, cur.EstimatedCostUSD, tol)
			addNumDrift(rep, "input_tokens", float64(prior.CumInputTokens), float64(cur.CumInputTokens), tol)
			addNumDrift(rep, "output_tokens", float64(prior.CumOutputTokens), float64(cur.CumOutputTokens), tol)
			addNumDrift(rep, "cache_read_tokens", float64(prior.CumCacheReadTokens), float64(cur.CumCacheReadTokens), tol)
		}
	}
	return rep, nil
}

// addNumDrift records a drift when current deviates from prior by > tolPct. A
// zero→nonzero (or nonzero→zero) flip is always a drift (infinite pct).
func addNumDrift(rep *ObservationReport, field string, prior, cur, tolPct float64) {
	if prior == 0 && cur == 0 {
		return
	}
	var pct float64
	if prior == 0 {
		pct = math.Inf(1)
	} else {
		pct = math.Abs(cur-prior) / math.Abs(prior) * 100
	}
	if pct > tolPct {
		rep.Drifts = append(rep.Drifts, ObsDrift{
			Field:    field,
			Prior:    fmt.Sprintf("%g", prior),
			Current:  fmt.Sprintf("%g", cur),
			PctDelta: pct,
		})
	}
}

// splitLines splits raw JSONL into per-line byte slices (trailing newline ok).
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
