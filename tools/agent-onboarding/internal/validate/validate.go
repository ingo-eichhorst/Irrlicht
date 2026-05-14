// Package validate implements Phase 5 of #268: compare a recording's
// emitted state transitions against its ground_truth.jsonl labels.
//
// The validator is intentionally offline. It reads events.jsonl (the
// daemon's emitted state_transitions) and a hand-authored or
// synthesizer-derived ground_truth.jsonl from the scenario directory,
// then asserts that every label finds a matching emitted transition
// within tolerance_ms. Live re-recording is Phase 6's job — Phase 5
// only validates what's on disk.
package validate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"irrlicht/tools/agent-onboarding/internal/groundtruth"
)

// ErrCoverageScenarioNotFound is returned by WriteCoverage when the
// caller's scenarioID doesn't appear in the coverage matrix. Surfaced
// rather than silently no-op'd so a typoed --coverage-id is visible.
var ErrCoverageScenarioNotFound = errors.New("coverage: scenario id not found in matrix")

// EmittedTransition is one state_transition event from the daemon's
// events.jsonl. Fields mirror the legacy record format.
type EmittedTransition struct {
	Ts        time.Time `json:"ts"`
	NewState  string    `json:"new_state"`
	PrevState string    `json:"prev_state,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// LabelResult is the per-label verdict.
type LabelResult struct {
	Marker        string `json:"marker"`
	ExpectedState string `json:"expected_state"`
	ObservedState string `json:"observed_state,omitempty"`
	ToleranceMs   int    `json:"tolerance_ms"`
	DeltaMs       int    `json:"delta_ms"`
	Pass          bool   `json:"pass"`
	Note          string `json:"note,omitempty"`
}

// ScenarioResult is the verdict for one scenario+agent.
type ScenarioResult struct {
	Agent          string        `json:"agent"`
	Scenario       string        `json:"scenario"`
	GeneratedAt    time.Time     `json:"generated_at"`
	Labels         []LabelResult `json:"labels"`
	Pass           bool          `json:"pass"`
	AgentVersion   string        `json:"agent_version,omitempty"`
	AdapterVersion string        `json:"adapter_version,omitempty"`
}

// Result is the final value used by coverage writeback: pass | fail |
// skipped | blocked-by-prereq.
func (s ScenarioResult) Result() string {
	if s.Pass {
		return "pass"
	}
	return "fail"
}

// Input bundles what the validator needs to evaluate one scenario.
type Input struct {
	Agent          string
	Scenario       string
	EventsPath     string // events.jsonl with state_transition records
	GroundTruth    string // ground_truth.jsonl
	AgentVersion   string // recorded into the result for coverage staleness
	AdapterVersion string // ditto
}

// Run compares emitted transitions to ground-truth labels and returns
// the verdict. Per-label tolerance defaults to 1000ms when zero.
func Run(ctx context.Context, in Input) (ScenarioResult, error) {
	if in.Agent == "" || in.Scenario == "" || in.EventsPath == "" || in.GroundTruth == "" {
		return ScenarioResult{}, errors.New("validate: agent, scenario, events, ground-truth are required")
	}
	transitions, err := readTransitions(in.EventsPath)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("read events: %w", err)
	}
	gtMeta, labels, err := readGroundTruth(in.GroundTruth)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("read ground-truth: %w", err)
	}
	start := gtMeta.RecordingStartedAt
	if start.IsZero() && len(transitions) > 0 {
		start = transitions[0].Ts
	}
	if start.IsZero() {
		return ScenarioResult{}, errors.New("validate: cannot determine recording start time")
	}

	result := ScenarioResult{
		Agent: in.Agent, Scenario: in.Scenario, GeneratedAt: time.Now().UTC(),
		AgentVersion: in.AgentVersion, AdapterVersion: in.AdapterVersion,
		Pass: true,
	}

	for _, l := range labels {
		tol := l.ToleranceMs
		if tol == 0 {
			tol = 1000
		}
		target := start.Add(time.Duration(l.TsOffsetMs) * time.Millisecond)
		closest, delta, ok := closestTransition(transitions, target)
		lr := LabelResult{
			Marker: l.Marker, ExpectedState: l.ExpectedState, ToleranceMs: tol,
		}
		if !ok {
			lr.Pass = false
			lr.Note = "no emitted state_transition found in events.jsonl"
		} else {
			lr.ObservedState = closest.NewState
			lr.DeltaMs = int(delta / time.Millisecond)
			if delta < 0 {
				lr.DeltaMs = -lr.DeltaMs
			}
			lr.Pass = closest.NewState == l.ExpectedState && lr.DeltaMs <= tol
			if !lr.Pass {
				if closest.NewState != l.ExpectedState {
					lr.Note = fmt.Sprintf("state mismatch: expected %s, observed %s", l.ExpectedState, closest.NewState)
				} else {
					lr.Note = fmt.Sprintf("outside tolerance (Δ=%dms > tol=%dms)", lr.DeltaMs, tol)
				}
			}
		}
		if !lr.Pass {
			result.Pass = false
		}
		result.Labels = append(result.Labels, lr)
	}
	return result, nil
}

// closestTransition returns the emitted transition whose timestamp is
// nearest to target (regardless of side). Returns (zero, 0, false) when
// the slice is empty.
func closestTransition(in []EmittedTransition, target time.Time) (EmittedTransition, time.Duration, bool) {
	if len(in) == 0 {
		return EmittedTransition{}, 0, false
	}
	best := in[0]
	bestAbs := absDuration(best.Ts.Sub(target))
	for _, e := range in[1:] {
		d := absDuration(e.Ts.Sub(target))
		if d < bestAbs {
			best = e
			bestAbs = d
		}
	}
	return best, best.Ts.Sub(target), true
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func readTransitions(path string) ([]EmittedTransition, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var out []EmittedTransition
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		var kind string
		if v, ok := raw["kind"]; ok {
			_ = json.Unmarshal(v, &kind)
		}
		if kind != "state_transition" {
			continue
		}
		var t EmittedTransition
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &t); err == nil && !t.Ts.IsZero() {
			out = append(out, t)
		}
	}
}

func readGroundTruth(path string) (groundtruth.Meta, []groundtruth.Label, error) {
	f, err := os.Open(path)
	if err != nil {
		return groundtruth.Meta{}, nil, err
	}
	defer f.Close()
	return groundtruth.Read(f)
}

// CoverageCell is the validator-owned subset of one cell in
// .specs/agent-scenarios-coverage.json. The maintainer-authored fields
// (agent_supports, irrlicht_observes, notes) are merged separately and
// never overwritten.
type CoverageCell struct {
	LastTested     time.Time `json:"last_tested"`
	AgentVersion   string    `json:"agent_version,omitempty"`
	AdapterVersion string    `json:"adapter_version,omitempty"`
	Result         string    `json:"result"`
}

// WriteCoverage merges a per-cell validator update into
// .specs/agent-scenarios-coverage.json without disturbing maintainer
// cells. scenarioID is the canonical id from agent-scenarios-coverage.json
// (NOT the skill's scenarios.json name) — the caller is responsible for
// mapping.
//
// If the coverage file or the cell doesn't exist yet, the function
// returns nil silently — the validator is informational in that case
// and the maintainer can establish the cell first.
func WriteCoverage(coveragePath, scenarioID, agent string, cell CoverageCell) error {
	b, err := os.ReadFile(coveragePath)
	if err != nil {
		return fmt.Errorf("read coverage: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("parse coverage: %w", err)
	}
	scenarios, ok := root["scenarios"].([]any)
	if !ok {
		return errors.New("coverage: missing scenarios array")
	}
	updated := false
	for _, s := range scenarios {
		so, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if so["id"] != scenarioID {
			continue
		}
		cov, ok := so["coverage"].(map[string]any)
		if !ok {
			cov = map[string]any{}
			so["coverage"] = cov
		}
		agentCell, ok := cov[agent].(map[string]any)
		if !ok {
			agentCell = map[string]any{}
			cov[agent] = agentCell
		}
		// Validator-owned fields only — never touch the maintainer trio.
		agentCell["last_tested"] = cell.LastTested.UTC().Format(time.RFC3339)
		if cell.AgentVersion != "" {
			agentCell["agent_version"] = cell.AgentVersion
		}
		if cell.AdapterVersion != "" {
			agentCell["adapter_version"] = cell.AdapterVersion
		}
		agentCell["result"] = cell.Result
		updated = true
		break
	}
	if !updated {
		return fmt.Errorf("%w: id=%q agent=%q", ErrCoverageScenarioNotFound, scenarioID, agent)
	}
	// Write back with stable indent.
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal coverage: %w", err)
	}
	tmp := coveragePath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, coveragePath)
}

// WriteResultJSON dumps the ScenarioResult to <outDir>/<agent>-<scenario>-validate.json.
func WriteResultJSON(outDir string, r ScenarioResult) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(outDir, fmt.Sprintf("%s-%s-validate.json", r.Agent, r.Scenario))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return "", err
	}
	return path, nil
}
