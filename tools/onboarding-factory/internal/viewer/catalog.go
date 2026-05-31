package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// handleCatalog serves the scenario coverage catalog. The skeleton (scenarios
// + agents) is built from the per-scenario shards (#510) + agents.All(); each
// per-cell verdict comes from the shard's per-agent Metadata block (overview
// tier), falling back to "unknown". Re-read on every request so shard edits
// land on next refresh without a rebuild.
//
// The shard ID already carries the "<section>.<index>" code, so it's set
// directly in buildCatalogJSON — no separate annotateCatalogCodes pass. The
// response is annotated in a single parse/marshal cycle: unmarshal once, run
// the in-place passes (measurements → pipeline → display-state), marshal once.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	b, sourceTag, err := s.buildCatalogJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("build catalog: %v", err), http.StatusInternalServerError)
		return
	}
	var top map[string]any
	if json.Unmarshal(b, &top) == nil {
		annotateMeasurements(top, s.RepoRoot)
		annotatePipelineState(top, s.RepoRoot)
		annotateDisplayState(top) // after measurements: the recording axis feeds the rollup
		if out, mErr := json.Marshal(top); mErr == nil {
			b = out
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Catalog-Source", sourceTag)
	w.Write(b)
}

// buildCatalogJSON assembles the /api/catalog response from the per-scenario
// shards (#510). Returns the marshaled JSON, a source tag for the
// X-Catalog-Source header ("shards"), and any error.
//
// One shard is one scenario row, in shard (section.index) order; the shard ID
// IS the "<section>.<index>" code. The agents columns still come from the
// daemon's adapter registry (agents.All()) so the matrix stays code-registry-
// driven; each coverage cell is built from the shard's per-agent Metadata block.
func (s *Server) buildCatalogJSON() ([]byte, string, error) {
	shards := shard.LoadAll(s.RepoRoot)
	if len(shards) == 0 {
		return nil, "", fmt.Errorf("no scenarios in %s", shard.File(s.RepoRoot))
	}

	// Agents list from the daemon's adapter registry. normalizeAdapter maps
	// hyphenated Identity.Name (e.g. "claude-code") to the on-disk slug
	// (e.g. "claudecode") used as the shard's per-agent key.
	allAgents := agents.All()
	agentEntries := make([]map[string]any, 0, len(allAgents))
	agentSlugs := make([]string, 0, len(allAgents))
	adapterCells := make(map[string]map[string]*shard.ShardAgent, len(allAgents))
	for _, a := range allAgents {
		slug := normalizeAdapter(a.Identity.Name)
		agentEntries = append(agentEntries, map[string]any{"id": slug, "onboarded": true})
		agentSlugs = append(agentSlugs, slug)
		adapterCells[slug] = shard.LoadAdapterCells(s.RepoRoot, slug) // one scan per adapter
	}

	scenarios := make([]map[string]any, 0, len(shards))
	for _, sh := range shards {
		coverage := make(map[string]any, len(agentSlugs))
		for _, slug := range agentSlugs {
			coverage[slug] = buildCellVerdict(adapterCells[slug][sh.Name])
		}
		scenarios = append(scenarios, map[string]any{
			"id":       sh.Name,
			"code":     sh.ID, // shard ID already carries "<section>.<index>"
			"coverage": coverage,
		})
	}

	out := map[string]any{
		"version":        1,
		"generated_at":   time.Now().UTC().Format("2006-01-02"),
		"source_catalog": "replaydata/agents/scenarios.json",
		"agents":         agentEntries,
		"scenarios":      scenarios,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	return b, "shards", nil
}

// buildCellVerdict produces one coverage[agent] entry from the cell's Metadata
// overview block. Defaults to "unknown"/"unknown"/"ready"/"" when the cell is
// nil or leaves an axis empty — the same defaults the old per-cell reader used.
func buildCellVerdict(ag *shard.ShardAgent) map[string]any {
	cell := map[string]any{
		"agent_supports":    "unknown",
		"daemon_capability": "unknown",
		"driver_capability": "ready",
		"notes":             "",
	}
	if ag == nil {
		return cell
	}
	md := ag.Metadata
	if md.AgentSupports != "" {
		cell["agent_supports"] = md.AgentSupports
	}
	if md.DaemonCapability != "" {
		cell["daemon_capability"] = md.DaemonCapability
	}
	if md.DriverCapability != "" {
		cell["driver_capability"] = md.DriverCapability
	}
	if md.Confidence > 0 {
		cell["confidence"] = md.Confidence
	}
	if md.Notes != "" {
		cell["notes"] = md.Notes
	}
	return cell
}

// deriveDisplayState rolls the three orthogonal facts — agent support, daemon
// capability, driver capability — plus the MEASURED recording status up into
// one display state for the matrix (#476). It delegates to the canonical
// matrix model (#508) so the viewer and the gates can never disagree on what a
// cell's verdict means; hasRecording is true when a recording has been captured
// (measurement status is anything other than the no-recording / no-spec
// sentinels).
func deriveDisplayState(supports, daemon, driver string, hasRecording bool) string {
	return matrix.DeriveDisplayState(supports, daemon, driver, hasRecording)
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
			supports, _ := cell["agent_supports"].(string)
			daemon, _ := cell["daemon_capability"].(string)
			driver, _ := cell["driver_capability"].(string)
			hasRecording := false
			if meas, ok := cell["measurement"].(map[string]any); ok {
				if st, ok := meas["status"].(string); ok {
					hasRecording = st != "" && st != "no_recording" && st != "no_expected"
				}
			}
			cell["display_state"] = deriveDisplayState(supports, daemon, driver, hasRecording)
		}
	}
}

// annotateMeasurements decorates each scenarios[].coverage[<agent>] cell
// with a `measurement` object derived from the scenario's expected.jsonl +
// events.jsonl, mutating top in place. Lets the overview render BOTH the
// maintainer's matrix verdict AND the measured execution state. No-op when
// the shape is unexpected.
func annotateMeasurements(top map[string]any, repoRoot string) {
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
			folder, ok := resolveScenarioFolderForAgent(recipes, agentSlug, sid)
			if !ok {
				// No cell on disk for this (agent, scenario) — genuinely absent.
				cell["measurement"] = map[string]any{"status": "no_recording"}
				continue
			}
			cell["measurement"] = measureScenario(repoRoot, agentSlug, folder)
		}
	}
}

// measureScenario probes one (agent, scenario) cell: looks for a recording
// (the newest under recordings/) + expected.jsonl, runs the validator, returns
// a compact status summary.
func measureScenario(repoRoot, agent, folder string) map[string]any {
	scenarioDir := filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios", folder)
	recDir, ok := validate.NewestRecordingDir(scenarioDir)
	if !ok {
		return map[string]any{"status": "no_recording"}
	}
	if _, err := os.Stat(filepath.Join(recDir, "events.jsonl")); err != nil {
		return map[string]any{"status": "no_recording"}
	}
	if _, err := os.Stat(filepath.Join(scenarioDir, "expected.jsonl")); err != nil {
		return map[string]any{"status": "no_expected"}
	}
	rep, err := validate.ValidateExpected(scenarioDir)
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
func annotatePipelineState(top map[string]any, repoRoot string) {
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
			folder, ok := resolveScenarioFolderForAgent(recipes, agentSlug, sid)
			if !ok {
				// No cell on disk for this (agent, scenario) — genuinely absent.
				cell["pipeline"] = pipelineForCell(repoRoot, agentSlug, sid, "", recipes)
				continue
			}
			cell["pipeline"] = pipelineForCell(repoRoot, agentSlug, sid, folder, recipes)
		}
	}
}

// pipelineForCell computes the recipe/spec/recordings status for one
// (agent, scenario) cell.
func pipelineForCell(repoRoot, agent, coverageID, folder string, recipes recipeIndex) map[string]any {
	out := map[string]any{}

	rec, ok := recipes.byName[folder]
	if !ok {
		rec = recipes.canonical[coverageID]
	}
	recipeAuthored := false
	stepCount := 0
	if rec.ByAdapter != nil {
		if entry, ok := rec.ByAdapter[agent]; ok {
			if entry.Applicable == nil || *entry.Applicable {
				recipeAuthored = true
				stepCount = len(entry.Script)
			}
		}
	}
	out["recipe"] = map[string]any{"authored": recipeAuthored, "step_count": stepCount}

	scenarioDir := filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios", folder)
	specAuthored := false
	phaseCount := 0
	if specBytes, err := os.ReadFile(filepath.Join(scenarioDir, "expected.jsonl")); err == nil {
		specAuthored = true
		lines := 0
		for _, b := range specBytes {
			if b == '\n' {
				lines++
			}
		}
		if lines > 0 {
			phaseCount = lines - 1 // first line is the meta object
		}
	}
	out["spec"] = map[string]any{"authored": specAuthored, "phase_count": phaseCount}

	// Every recording lives under recordings/<name>/; "latest" means at least
	// one recording exists, "archive_count" is the total recording count.
	recCount := len(RecordingStore{RepoRoot: repoRoot}.listArchiveDirs(scenarioDir))
	out["recordings"] = map[string]any{"latest": recCount > 0, "archive_count": recCount}

	return out
}
