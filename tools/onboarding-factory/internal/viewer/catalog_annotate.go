package viewer

import (
	"os"
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// This file holds /api/catalog's in-place annotation passes: the measured
// recording status, the recipe/spec/recordings pipeline block, and the
// display-state rollup they feed. They live apart from catalog.go (which
// assembles the skeleton) because each pass walks the same nested response
// shape and the three together are most of the endpoint's complexity.
//
// Every pass is scoped to ONE execution profile (#1889): measurement and
// pipeline must agree about which profile's recordings they are describing, or
// a Desktop-scoped rollup would count CLI recordings as "recorded".

// measurementCell is one cell's measurement inputs: where to look, which
// (agent, scenario) pair it is, and which execution profile's recordings count.
type measurementCell struct {
	repoRoot   string
	recipes    recipeIndex
	scenarioID string
	agent      string
	profile    matrix.ExecutionProfile
	cell       map[string]any
}

// deriveDisplayState rolls the three orthogonal facts — agent support, daemon
// capability, driver capability — plus the MEASURED recording status and
// applicability up into one display state for the matrix (#476). It delegates to
// the canonical matrix model (#508) so the viewer and the gates can never
// disagree on what a cell's verdict means; hasRecording is true when a recording
// has been captured (measurement status is anything other than the no-recording
// / no-spec sentinels).
func deriveDisplayState(supports, daemon, driver string, hasRecording, applicable bool) string {
	return matrix.DeriveDisplayState(supports, daemon, driver, hasRecording, applicable)
}

// annotateDisplayState decorates each coverage cell with a derived
// `display_state` string (see deriveDisplayState), mutating top in place.
// Runs AFTER annotateMeasurements so the recording axis is available.
func annotateDisplayState(top map[string]any) {
	rawScenarios, ok := top["scenarios"].([]any)
	if !ok {
		return
	}
	for _, raw := range rawScenarios {
		sc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		coverage, ok := sc["coverage"].(map[string]any)
		if !ok {
			continue
		}
		for _, cellRaw := range coverage {
			cell, ok := cellRaw.(map[string]any)
			if !ok {
				continue
			}
			annotateCellDisplayState(cell)
		}
	}
}

// annotateCellDisplayState computes and stores display_state for one
// coverage cell, per deriveDisplayState.
func annotateCellDisplayState(cell map[string]any) {
	supports, _ := cell["agent_supports"].(string)
	daemon, _ := cell["daemon_capability"].(string)
	driver, _ := cell["driver_capability"].(string)
	applicable := true
	if v, ok := cell["applicable"].(bool); ok {
		applicable = v
	}
	cell["display_state"] = deriveDisplayState(supports, daemon, driver, cellHasRecording(cell), applicable)
}

// cellHasRecording reports whether the cell's `measurement` axis (set by
// annotateMeasurements, which must run first) indicates a captured
// recording rather than an absent/unspecced/unreadable one. "manifest_error"
// is explicitly NOT a recording: the cell's manifests could not be read, so
// claiming a recording exists would roll an unknown up as "observed".
func cellHasRecording(cell map[string]any) bool {
	meas, ok := cell["measurement"].(map[string]any)
	if !ok {
		return false
	}
	st, ok := meas["status"].(string)
	if !ok {
		return false
	}
	return st != "" && st != "no_recording" && st != "no_expected" && st != "manifest_error"
}

// annotateMeasurements decorates each scenarios[].coverage[<agent>] cell
// with a `measurement` object derived from the scenario's expected.jsonl +
// events.jsonl, mutating top in place. Lets the overview render BOTH the
// maintainer's matrix verdict AND the measured execution state. No-op when
// the shape is unexpected.
func annotateMeasurements(top map[string]any, repoRoot string, profile matrix.ExecutionProfile) {
	rawScenarios, ok := top["scenarios"].([]any)
	if !ok {
		return
	}
	recipes := loadRecipeMap(repoRoot)
	for _, raw := range rawScenarios {
		sc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		sid, _ := sc["id"].(string)
		if sid == "" {
			continue
		}
		coverage, ok := sc["coverage"].(map[string]any)
		if !ok {
			continue
		}
		for agentSlug, cellRaw := range coverage {
			cell, ok := cellRaw.(map[string]any)
			if !ok {
				continue
			}
			annotateMeasurementCell(measurementCell{
				repoRoot: repoRoot, recipes: recipes, scenarioID: sid,
				agent: agentSlug, profile: profile, cell: cell,
			})
		}
	}
}

// annotateMeasurementCell sets the `measurement` field on one coverage
// cell: "no_recording" when the (agent, scenario) has no cell on disk,
// otherwise the result of probing its recording + expected.jsonl.
func annotateMeasurementCell(input measurementCell) {
	folder, ok := resolveScenarioFolderForAgent(input.recipes, input.agent, input.scenarioID)
	if !ok {
		// No cell on disk for this (agent, scenario) — genuinely absent.
		input.cell["measurement"] = map[string]any{"status": "no_recording"}
		return
	}
	input.cell["measurement"] = measureScenario(input.repoRoot, input.agent, folder, input.profile)
}

// measureScenario probes one (agent, scenario) cell: looks for a recording
// (the newest under recordings/ WITHIN profile) + expected.jsonl, runs the
// validator, returns a compact status summary. The profile scope is the same
// one the scenario-detail endpoint uses, so the matrix and the detail page can
// never grade different recordings.
func measureScenario(repoRoot, agent, folder string, profile matrix.ExecutionProfile) map[string]any {
	scenarioDir := filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios", folder)
	recDir, ok, err := newestRecordingDirForProfile(scenarioDir, profile)
	if err != nil {
		// A cell whose manifests cannot be read is not a cell without
		// recordings. Its own status keeps the two apart in the matrix.
		return map[string]any{"status": "manifest_error", "summary": err.Error()}
	}
	if !ok {
		return map[string]any{"status": "no_recording"}
	}
	if _, err := os.Stat(filepath.Join(recDir, "events.jsonl")); err != nil {
		return map[string]any{"status": "no_recording"}
	}
	if _, err := os.Stat(filepath.Join(scenarioDir, "expected.jsonl")); err != nil {
		return map[string]any{"status": "no_expected"}
	}
	rep, err := validateExpectedRecording(scenarioDir, recDir)
	if err != nil || rep == nil {
		return map[string]any{"status": "validator_error"}
	}
	knownFailing := rep.Meta.KnownFailing
	switch {
	case rep.Pass && !knownFailing:
		return map[string]any{"status": "pass", "summary": rep.Summary}
	case rep.Pass && knownFailing:
		return map[string]any{"status": "known_failing_now_passing", "summary": rep.Summary}
	case knownFailing:
		return map[string]any{"status": "known_failing", "summary": rep.Summary}
	default:
		return map[string]any{"status": "fail", "summary": rep.Summary}
	}
}

// annotatePipelineState decorates each coverage cell with a `pipeline`
// object (recipe / spec / recordings status), mutating top in place. Reads
// scenarios.json once and reuses the parsed map per cell. No-op when the
// shape is unexpected.
func annotatePipelineState(top map[string]any, repoRoot string, profile matrix.ExecutionProfile) {
	rawScenarios, ok := top["scenarios"].([]any)
	if !ok {
		return
	}
	recipes := loadRecipeMap(repoRoot)
	for _, raw := range rawScenarios {
		sc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		sid, _ := sc["id"].(string)
		if sid == "" {
			continue
		}
		coverage, ok := sc["coverage"].(map[string]any)
		if !ok {
			continue
		}
		for agentSlug, cellRaw := range coverage {
			cell, ok := cellRaw.(map[string]any)
			if !ok {
				continue
			}
			annotatePipelineCell(measurementCell{
				repoRoot: repoRoot, recipes: recipes, scenarioID: sid,
				agent: agentSlug, profile: profile, cell: cell,
			})
		}
	}
}

// annotatePipelineCell sets the `pipeline` field on one coverage cell.
// folder is resolved from disk when available; a cell absent on disk still
// gets a pipeline block (via a "" folder) so recipe-authored-but-unrecorded
// cells still show their recipe/spec status.
func annotatePipelineCell(input measurementCell) {
	folder, ok := resolveScenarioFolderForAgent(input.recipes, input.agent, input.scenarioID)
	if !ok {
		// No cell on disk for this (agent, scenario) — genuinely absent.
		folder = ""
	}
	input.cell["pipeline"] = pipelineForCell(input, folder)
}

// pipelineForCell computes the recipe/spec/recordings status for one
// (agent, scenario) cell.
func pipelineForCell(input measurementCell, folder string) map[string]any {
	recipeAuthored, stepCount := recipeStats(input.recipes, input.agent, input.scenarioID, folder)
	specAuthored, phaseCount, recCount := specAndRecordingStats(input, folder)
	return map[string]any{
		"recipe":     map[string]any{"authored": recipeAuthored, "step_count": stepCount},
		"spec":       map[string]any{"authored": specAuthored, "phase_count": phaseCount},
		"recordings": map[string]any{"latest": recCount > 0, "archive_count": recCount},
	}
}

// recipeStats reports whether an authored (applicable) recipe script exists
// for (agent, scenario) and how many steps it has. Falls back from the
// on-disk folder name to the scenario's canonical coverage ID when no
// by-name recipe entry exists.
func recipeStats(recipes recipeIndex, agent, coverageID, folder string) (authored bool, stepCount int) {
	rec, ok := recipes.byName[folder]
	if !ok {
		rec = recipes.canonical[coverageID]
	}
	if rec.ByAdapter == nil {
		return false, 0
	}
	entry, ok := rec.ByAdapter[agent]
	if !ok {
		return false, 0
	}
	if entry.Applicable != nil && !*entry.Applicable {
		return false, 0
	}
	return true, len(entry.Script)
}

// specAndRecordingStats reports whether an expected.jsonl spec exists for
// this cell, how many phases it describes, and how many recording archives
// have been captured. folder == "" means the cell is absent on disk (no
// metadata.json for this agent/scenario): there is no spec or recording,
// and joining an empty folder would stat the scenarios/ parent, so the
// disk reads are skipped entirely.
func specAndRecordingStats(input measurementCell, folder string) (authored bool, phaseCount, recCount int) {
	if folder == "" {
		return false, 0, 0
	}
	scenarioDir := filepath.Join(input.repoRoot, "replaydata", "agents", input.agent, "scenarios", folder)
	// Recordings live under recordings/<name>/, and the count is scoped to the
	// SAME execution profile the measurement axis used (#1889). Counting both
	// profiles here would let a cell's CLI recordings mark it "recorded" in a
	// Desktop-scoped rollup while its measurement said no_recording.
	recordings, err := matrix.RecordingsForProfile(scenarioDir, input.profile)
	if err != nil {
		logViewerError("specAndRecordingStats: %s in %s: %v", input.profile, scenarioDir, err)
	}
	recCount = len(recordings)
	specBytes, err := os.ReadFile(filepath.Join(scenarioDir, "expected.jsonl"))
	if err != nil {
		return false, 0, recCount
	}
	return true, countJSONLPhases(specBytes), recCount
}

// countJSONLPhases counts expected.jsonl phase lines: total lines minus the
// leading meta object.
func countJSONLPhases(specBytes []byte) int {
	lines := 0
	for _, b := range specBytes {
		if b == '\n' {
			lines++
		}
	}
	if lines == 0 {
		return 0
	}
	return lines - 1
}
