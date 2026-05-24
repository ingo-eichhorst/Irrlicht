package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/tools/agent-onboarding/internal/validate"
)

// handleCatalog serves the maintainer-curated scenario coverage catalog.
// The skeleton (scenarios + agents) is built from tracked sources
// (scenarios.json's catalog[] + agents.All()); per-cell verdicts come
// from assessment.json, falling back to the .specs/ coverage overlay,
// falling back to "unknown". Re-read on every request so maintainer edits
// land on next refresh without a rebuild.
//
// The response is annotated in a single parse/marshal cycle: unmarshal
// once, run the three in-place passes (codes → measurements → pipeline),
// marshal once. Previously each pass re-unmarshalled and re-marshalled the
// whole catalog — three JSON round-trips for one response.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	b, sourceTag, err := s.buildCatalogJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("build catalog: %v", err), http.StatusInternalServerError)
		return
	}
	var top map[string]any
	if json.Unmarshal(b, &top) == nil {
		annotateCatalogCodes(top)
		annotateMeasurements(top, s.RepoRoot)
		annotatePipelineState(top, s.RepoRoot)
		if out, mErr := json.Marshal(top); mErr == nil {
			b = out
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Catalog-Source", sourceTag)
	w.Write(b)
}

// buildCatalogJSON assembles the /api/catalog response from tracked
// sources, with optional .specs/ overlay. Returns the marshaled JSON, a
// source tag for the X-Catalog-Source header, and any error.
//
// Source tag is one of:
//   - "tracked"               — no .specs/ overlay; verdicts from
//     assessment.json or "unknown"
//   - "tracked+specs-overlay" — .specs/ present, used as fallback for
//     cells without assessment.json
func (s *Server) buildCatalogJSON() ([]byte, string, error) {
	scenariosPath := filepath.Join(s.RepoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	scenariosBytes, err := os.ReadFile(scenariosPath)
	if err != nil {
		return nil, "", fmt.Errorf("read scenarios.json: %w", err)
	}
	var scenariosDoc struct {
		Catalog []struct {
			ID      string `json:"id"`
			Section string `json:"section"`
			Feature string `json:"feature"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(scenariosBytes, &scenariosDoc); err != nil {
		return nil, "", fmt.Errorf("parse scenarios.json: %w", err)
	}

	// Agents list from the daemon's adapter registry. normalizeAdapter maps
	// hyphenated Identity.Name (e.g. "claude-code") to the on-disk slug
	// (e.g. "claudecode") used under replaydata/agents/ and in .specs/.
	allAgents := agents.All()
	agentEntries := make([]map[string]any, 0, len(allAgents))
	agentSlugs := make([]string, 0, len(allAgents))
	for _, a := range allAgents {
		slug := normalizeAdapter(a.Identity.Name)
		agentEntries = append(agentEntries, map[string]any{"id": slug, "onboarded": true})
		agentSlugs = append(agentSlugs, slug)
	}

	overlay := loadSpecsOverlay(s.resolveCoveragePath())
	sourceTag := "tracked"
	if overlay != nil {
		sourceTag = "tracked+specs-overlay"
	}

	// Build scenarios[] with coverage[] per agent. Precedence per cell:
	// assessment.json > .specs/ overlay > "unknown".
	scenarios := make([]map[string]any, 0, len(scenariosDoc.Catalog))
	for _, sc := range scenariosDoc.Catalog {
		coverage := make(map[string]any, len(agentSlugs))
		for _, slug := range agentSlugs {
			coverage[slug] = buildCellVerdict(s.RepoRoot, slug, sc.ID, overlay)
		}
		scenarios = append(scenarios, map[string]any{
			"id":       sc.ID,
			"section":  sc.Section,
			"feature":  sc.Feature,
			"coverage": coverage,
		})
	}

	out := map[string]any{
		"version":        1,
		"generated_at":   time.Now().UTC().Format("2006-01-02"),
		"source_catalog": ".claude/skills/ir:onboard-agent/scenarios.json (catalog)",
		"agents":         agentEntries,
		"scenarios":      scenarios,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	return b, sourceTag, nil
}

// buildCellVerdict produces one coverage[agent] entry. Reads
// replaydata/agents/<agent>/scenarios/<scenarioID>/assessment.json when
// present; falls back to the .specs/ overlay entry; falls back to
// "unknown" with no notes.
func buildCellVerdict(repoRoot, agentSlug, scenarioID string, overlay map[string]map[string]map[string]any) map[string]any {
	cell := map[string]any{
		"agent_supports":    "unknown",
		"irrlicht_observes": "unknown",
		"notes":             "",
	}
	if overlay != nil {
		if sc, ok := overlay[scenarioID]; ok {
			if v, ok := sc[agentSlug]; ok {
				if s, ok := v["agent_supports"].(string); ok && s != "" {
					cell["agent_supports"] = s
				}
				if o, ok := v["irrlicht_observes"].(string); ok && o != "" {
					cell["irrlicht_observes"] = o
				}
				if n, ok := v["notes"].(string); ok {
					cell["notes"] = n
				}
			}
		}
	}
	apath := filepath.Join(repoRoot, "replaydata", "agents", agentSlug, "scenarios", scenarioID, "assessment.json")
	if b, err := os.ReadFile(apath); err == nil {
		var asmt struct {
			AgentSupports    string  `json:"agent_supports"`
			IrrlichtObserves string  `json:"irrlicht_observes"`
			Confidence       float64 `json:"confidence"`
			Body             string  `json:"body"`
			Notes            string  `json:"notes"`
		}
		if json.Unmarshal(b, &asmt) == nil {
			if asmt.AgentSupports != "" {
				cell["agent_supports"] = asmt.AgentSupports
			}
			if asmt.IrrlichtObserves != "" {
				cell["irrlicht_observes"] = asmt.IrrlichtObserves
			}
			if asmt.Confidence > 0 {
				cell["confidence"] = asmt.Confidence
			}
			if asmt.Notes != "" {
				cell["notes"] = asmt.Notes
			} else if asmt.Body != "" {
				cell["notes"] = firstParagraph(asmt.Body)
			}
		}
	}
	return cell
}

// loadSpecsOverlay reads the coverage rollup (if reachable) and returns a
// flat map keyed by scenarioID → agentSlug → verdict fields. Returns nil
// if the file is unreachable or malformed — callers treat nil as "no
// overlay" and rely on assessment.json plus the "unknown" default.
func loadSpecsOverlay(covPath string) map[string]map[string]map[string]any {
	if covPath == "" {
		return nil
	}
	b, err := os.ReadFile(covPath)
	if err != nil {
		return nil
	}
	var doc struct {
		Scenarios []struct {
			ID       string                            `json:"id"`
			Coverage map[string]map[string]interface{} `json:"coverage"`
		} `json:"scenarios"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	out := make(map[string]map[string]map[string]any, len(doc.Scenarios))
	for _, sc := range doc.Scenarios {
		if sc.ID == "" {
			continue
		}
		out[sc.ID] = sc.Coverage
	}
	return out
}

// firstParagraph returns the first non-empty paragraph of a markdown body,
// with surrounding whitespace trimmed. Used to derive a short note from
// assessment.json.body when no explicit notes field is set.
func firstParagraph(body string) string {
	for _, para := range strings.Split(body, "\n\n") {
		p := strings.TrimSpace(para)
		if strings.HasPrefix(p, "#") {
			continue
		}
		if p != "" {
			return strings.Join(strings.Fields(p), " ")
		}
	}
	return ""
}

// annotateCatalogCodes assigns each scenario a "<section>.<index>" code
// (e.g. "1.3" for the third scenario in section 1), mutating top in place.
// Section numbering follows first-appearance order; scenario index resets
// at each new section. No-op when the shape is unexpected.
func annotateCatalogCodes(top map[string]any) {
	rawScenarios, ok := top["scenarios"].([]any)
	if !ok {
		return
	}
	sectionIdx := map[string]int{}
	sectionOrder := 0
	withinSection := map[string]int{}
	for _, raw := range rawScenarios {
		sc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		section, _ := sc["section"].(string)
		if section == "" {
			section = "(other)"
		}
		if _, seen := sectionIdx[section]; !seen {
			sectionOrder++
			sectionIdx[section] = sectionOrder
		}
		withinSection[section]++
		sc["code"] = fmt.Sprintf("%d.%d", sectionIdx[section], withinSection[section])
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
			folder := resolveScenarioFolderForAgent(recipes, agentSlug, sid)
			if folder == "" {
				folder = sid
			}
			cell["measurement"] = measureScenario(repoRoot, agentSlug, folder)
		}
	}
}

// measureScenario probes one (agent, scenario) cell: looks for a recording
// + expected.jsonl, runs the validator, returns a compact status summary.
func measureScenario(repoRoot, agent, folder string) map[string]any {
	scenarioDir := filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios", folder)
	if _, err := os.Stat(filepath.Join(scenarioDir, "events.jsonl")); err != nil {
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
			folder := resolveScenarioFolderForAgent(recipes, agentSlug, sid)
			if folder == "" {
				folder = sid
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

	latest := false
	if _, err := os.Stat(filepath.Join(scenarioDir, "events.jsonl")); err == nil {
		latest = true
	}
	archiveCount := len(RecordingStore{RepoRoot: repoRoot}.listArchiveDirs(scenarioDir))
	out["recordings"] = map[string]any{"latest": latest, "archive_count": archiveCount}

	return out
}

// resolveScenarioFolderForAgent returns the replaydata folder name for one
// (agent, coverage_id) pair, preferring the folder whose
// replaydata/agents/<agent>/scenarios/<folder>/expected.jsonl exists.
// Returns "" when the agent is not declared by any scenarios.json entry
// under this coverage_id — callers then fall back to the coverage_id.
func resolveScenarioFolderForAgent(idx recipeIndex, agent, coverageID string) string {
	if perAgent, ok := idx.folderByAgent[coverageID]; ok {
		if folder, ok := perAgent[agent]; ok {
			return folder
		}
	}
	return ""
}

// resolveCoveragePath finds the maintainer's coverage rollup. Looks in the
// repo root first, then in the main checkout when the repo root is a git
// worktree (.git is a "gitdir:" pointer file). Returns "" if neither has it.
func (s *Server) resolveCoveragePath() string {
	direct := filepath.Join(s.RepoRoot, ".claude", "skills", "ir:onboard-agent", "agent-scenarios-coverage.json")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	gitMeta := filepath.Join(s.RepoRoot, ".git")
	st, err := os.Stat(gitMeta)
	if err != nil || st.IsDir() {
		return ""
	}
	data, err := os.ReadFile(gitMeta)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	// gitdir = <main>/.git/worktrees/<id>; main checkout = grandparent of
	// grandparent (worktrees/<id> → worktrees → .git → <main>).
	main := filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
	candidate := filepath.Join(main, ".claude", "skills", "ir:onboard-agent", "agent-scenarios-coverage.json")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
