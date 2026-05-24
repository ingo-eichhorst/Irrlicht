// Package viewer serves the replay viewer: a localhost web UI for
// inspecting recordings (events.jsonl + transcript.jsonl + expected.jsonl
// validation + archive history).
//
// API:
//   GET  /                                                  — embedded SPA
//   GET  /api/scenarios                                     — list of recordings
//   GET  /api/scenarios/{agent}/{subtree}/{id}              — recording detail
//   GET  /api/scenarios/{agent}/{subtree}/{id}/recordings   — archived recordings
//   GET  /api/scenarios/{agent}/{subtree}/{id}/recordings/{name} — one archive
//
// `subtree` is "scenarios" or "regression"; the recordings live at
// replaydata/agents/<agent>/<subtree>/<id>/.
package viewer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/tools/agent-onboarding/internal/validate"
)

// slugRE constrains user-supplied URL components (agent, scenario id) so
// they can never traverse out of replaydata/agents/. Matches the same
// shape the assess schema enforces for agent slugs.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

//go:embed web/*
var webFS embed.FS

// Server is the viewer HTTP server.
type Server struct {
	RepoRoot string // path containing replaydata/

	// playback manages the single active replay session. Lazily created
	// on Handler() so callers can construct a Server with just RepoRoot
	// (matching the pre-playback API).
	playback *PlaybackManager
}

// PlaybackManager returns the server's playback manager, initialising it
// if necessary. Used by main.go to seed an auto-playback at boot.
func (s *Server) PlaybackManager() *PlaybackManager {
	if s.playback == nil {
		s.playback = NewPlaybackManager(s.RepoRoot)
	}
	return s.playback
}

// Handler returns the http.Handler the CLI wires into ListenAndServe.
func (s *Server) Handler() http.Handler {
	if s.playback == nil {
		s.playback = NewPlaybackManager(s.RepoRoot)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/scenarios/", s.handleScenarioDetail) // path with trailing parts
	mux.HandleFunc("/api/scenarios", s.handleScenariosList)
	mux.HandleFunc("/api/catalog", s.handleCatalog)
	mux.HandleFunc("/api/recipes", s.handleRecipes)
	mux.HandleFunc("/api/scenario-spec/", s.handleScenarioSpec)
	s.playback.registerPlaybackRoutes(mux)
	mux.Handle("/", s.staticHandler())
	return mux
}

// staticHandler serves the embedded web/ tree at /.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Embedded FS misconfiguration; fall back to "no UI" handler.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "embedded UI unavailable", http.StatusInternalServerError)
		})
	}
	return http.FileServerFS(sub)
}

// handleCatalog serves the maintainer-curated scenario coverage
// catalog at `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json` — the source of
// truth for the per-agent applicability matrix (38 scenarios × 5
// agents, each with agent_supports / irrlicht_observes verdicts and
// notes). Falls back to `.claude/skills/ir:onboard-agent/scenarios.json`
// (which only carries the 8 actively-driven cells) when the coverage
// file isn't available.
//
// `.specs/` is gitignored, so in a git worktree (`.git` is a file
// pointing back to the common `.git/worktrees/<id>/`) the coverage
// file lives in the user's main checkout, not the worktree. We
// resolve the main checkout via `git rev-parse --git-common-dir`
// equivalent — read the worktree's .git file, follow its gitdir, and
// walk back up to find the main worktree.
//
// Re-reads on every request so maintainer edits land on next page
// refresh without a server rebuild.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	// Catalog assembly. Source precedence:
	//   1. Skeleton (scenarios + agents) is ALWAYS built from tracked
	//      sources: scenarios.json's `catalog[]` field + agents.All().
	//      This guarantees the dashboard reflects the on-disk state of
	//      the repo, not a stale maintainer rollup.
	//   2. Per-cell verdict (agent_supports, irrlicht_observes, notes,
	//      confidence) comes from assessment.json when present (the
	//      committed Stage-1 artifact).
	//   3. If .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json is reachable, it
	//      provides FALLBACK verdicts for cells that lack assessment.json.
	//      Optional — maintainers without .specs/ see "unknown" until
	//      assessments are authored.
	//
	// Annotation as today: codes, measurements, pipeline.
	b, sourceTag, err := s.buildCatalogJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("build catalog: %v", err), http.StatusInternalServerError)
		return
	}
	b = annotateCatalogCodes(b)
	b = annotateMeasurements(b, s.RepoRoot)
	b = annotatePipelineState(b, s.RepoRoot)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Catalog-Source", sourceTag)
	w.Write(b)
}

// buildCatalogJSON assembles the /api/catalog response from tracked
// sources, with optional .specs/ overlay. Returns the marshaled JSON,
// a source tag for the X-Catalog-Source header, and any error.
//
// Source tag is one of:
//   - "tracked"               — no .specs/ overlay; verdicts from
//                                assessment.json or "unknown"
//   - "tracked+specs-overlay" — .specs/ present, used as fallback for
//                                cells without assessment.json
func (s *Server) buildCatalogJSON() ([]byte, string, error) {
	// 1. Read tracked scenarios.json -> catalog[].
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

	// 2. Agents list from the daemon's adapter registry. normalizeAdapter
	//    maps hyphenated Identity.Name (e.g. "claude-code") to the
	//    canonical slug used on disk under replaydata/agents/ and in
	//    .specs/ (e.g. "claudecode"). Without this, assessment.json
	//    lookups would fail for adapters whose internal slug differs
	//    from their replaydata path.
	allAgents := agents.All()
	agentEntries := make([]map[string]any, 0, len(allAgents))
	agentSlugs := make([]string, 0, len(allAgents))
	for _, a := range allAgents {
		slug := normalizeAdapter(a.Identity.Name)
		agentEntries = append(agentEntries, map[string]any{
			"id":        slug,
			"onboarded": true,
		})
		agentSlugs = append(agentSlugs, slug)
	}

	// 3. Load .specs/ overlay if reachable.
	overlay := loadSpecsOverlay(s.resolveCoveragePath())
	sourceTag := "tracked"
	if overlay != nil {
		sourceTag = "tracked+specs-overlay"
	}

	// 4. Build scenarios[] with coverage[] per agent. Precedence per cell:
	//    assessment.json > .specs/ overlay > "unknown".
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
	// Default placeholder.
	cell := map[string]any{
		"agent_supports":    "unknown",
		"irrlicht_observes": "unknown",
		"notes":             "",
	}
	// .specs/ overlay fallback.
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
	// assessment.json overrides if present.
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
			// Prefer explicit notes field; fall back to first paragraph of body.
			if asmt.Notes != "" {
				cell["notes"] = asmt.Notes
			} else if asmt.Body != "" {
				cell["notes"] = firstParagraph(asmt.Body)
			}
		}
	}
	return cell
}

// loadSpecsOverlay reads .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json (if it
// exists) and returns a flat map keyed by scenarioID → agentSlug →
// verdict fields. Returns nil if the file is unreachable or malformed
// — callers treat nil as "no overlay" and rely on assessment.json plus
// the "unknown" default.
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

// firstParagraph returns the first non-empty paragraph of a markdown
// body, with surrounding whitespace trimmed. Used to derive a short
// note from assessment.json.body when no explicit notes field is set.
func firstParagraph(body string) string {
	for _, para := range strings.Split(body, "\n\n") {
		p := strings.TrimSpace(para)
		// Skip leading headings like "## Verdict".
		if strings.HasPrefix(p, "#") {
			continue
		}
		if p != "" {
			// Collapse internal newlines to spaces for compact inline display.
			return strings.Join(strings.Fields(p), " ")
		}
	}
	return ""
}

// annotateCatalogCodes walks the catalog JSON, assigns each scenario
// a "<section>.<index-within-section>" code (e.g. "1.3" for the third
// scenario in section 1), and returns the re-marshaled JSON. Section
// numbering follows first-appearance order in the file. Scenario
// index resets to 1 at each new section.
//
// Failure is graceful — if the JSON doesn't parse or doesn't have the
// expected shape, return the input bytes unchanged so the frontend
// still gets a usable catalog (just without codes).
func annotateCatalogCodes(b []byte) []byte {
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		return b
	}
	rawScenarios, ok := top["scenarios"].([]any)
	if !ok {
		return b
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
	out, err := json.Marshal(top)
	if err != nil {
		return b
	}
	return out
}

// annotateMeasurements walks each scenarios[].coverage[<agent>] cell
// and decorates it with a `measurement` object derived from the
// scenario's expected.jsonl + events.jsonl (if present). Lets the
// overview UI render BOTH the maintainer's matrix verdict (coverage
// breadth) AND the measured execution state — they're separate signals
// and the matrix can be stale relative to what the current recording
// actually proves.
//
// Output shape per cell, when there's a recording:
//   "measurement": {
//     "status": "pass" | "fail" | "known_failing" | "no_recording" | "no_expected",
//     "summary": "X/N phases passed"
//   }
//
// Failure is graceful — bad JSON returns the input unchanged.
func annotateMeasurements(b []byte, repoRoot string) []byte {
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		return b
	}
	rawScenarios, ok := top["scenarios"].([]any)
	if !ok {
		return b
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
	out, err := json.Marshal(top)
	if err != nil {
		return b
	}
	return out
}

// measureScenario probes one (agent, scenario) cell: looks for a
// recording, looks for expected.jsonl, runs the validator. Returns a
// compact summary suitable for embedding in the catalog response.
//
// The matrix's `scenarioID` is the coverage_id (e.g. "user-esc-interrupt"),
// while replaydata folders use the recipe `name` (e.g. "interrupted-turn").
// scenarios.json carries the mapping; we resolve it here so the matrix's
// scenario id is the only thing the caller needs to know.
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
		// Validator passing despite a known_failing flag means the gap
		// closed — surface so the maintainer drops the flag.
		return map[string]any{"status": "known_failing_now_passing", "summary": rep.Summary}
	case knownFailing:
		return map[string]any{"status": "known_failing", "summary": rep.Summary}
	default:
		return map[string]any{"status": "fail", "summary": rep.Summary}
	}
}

// annotatePipelineState decorates each coverage cell with a `pipeline`
// object describing where the (agent × scenario) pair currently sits in
// the multi-stage workflow:
//
//	pipeline: {
//	  recipe:     { authored: bool, step_count: N },
//	  spec:       { authored: bool, phase_count: N },
//	  recordings: { latest: bool, archive_count: N },
//	}
//
// `measurement` (added by annotateMeasurements) already carries the
// fifth stage's outcome. The overview UI composes the 5-segment strip
// per cell from these three blobs + the existing verdict + measurement.
//
// Reads scenarios.json ONCE per request and reuses the parsed map per
// cell. Spec phase count is a cheap line-count scan; recording counts
// are filesystem stats only.
func annotatePipelineState(b []byte, repoRoot string) []byte {
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		return b
	}
	rawScenarios, ok := top["scenarios"].([]any)
	if !ok {
		return b
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
			// Resolve the scenario folder name PER AGENT — coverage_id
			// collisions (e.g. user-esc-interrupt mapped by both
			// interrupted-turn and user-esc-interrupt) need the agent's
			// own canonical folder, not a global one.
			folder := resolveScenarioFolderForAgent(recipes, agentSlug, sid)
			if folder == "" {
				folder = sid
			}
			cell["pipeline"] = pipelineForCell(repoRoot, agentSlug, sid, folder, recipes)
		}
	}
	out, err := json.Marshal(top)
	if err != nil {
		return b
	}
	return out
}

// recipeEntry captures the slice of scenarios.json fields the pipeline
// state code uses.
type recipeEntry struct {
	Name       string
	CoverageID string
	ByAdapter  map[string]struct {
		Applicable *bool `json:"applicable"`
		Script     []any `json:"script"`
	}
}

// recipeIndex is the result of one scenarios.json read. Holds both the
// canonical recipe-per-coverage-id map (used for recipe step counts /
// adapter applicability) and a per-(agent, coverage_id) folder lookup.
// Two scenarios.json entries may share a coverage_id while covering
// disjoint sets of agents (e.g. "interrupted-turn" → codex/pi/aider and
// "user-esc-interrupt" → claudecode both have coverage_id
// user-esc-interrupt). The canonical entry alone can't pick the right
// replaydata folder for each agent — folderByAgent does.
type recipeIndex struct {
	canonical     map[string]recipeEntry       // coverageID → canonical recipe
	folderByAgent map[string]map[string]string // [coverageID][agent] → folder name
	byName        map[string]recipeEntry       // scenario name → recipe (for per-agent recipe lookups)
}

// loadRecipeMap reads .claude/skills/ir:onboard-agent/scenarios.json
// once per request and returns a coverageID-keyed lookup plus a
// per-(agent, coverage_id) folder map. Missing or malformed file →
// empty index; callers tolerate "no recipe authored."
func loadRecipeMap(repoRoot string) recipeIndex {
	out := recipeIndex{
		canonical:     map[string]recipeEntry{},
		folderByAgent: map[string]map[string]string{},
		byName:        map[string]recipeEntry{},
	}
	path := filepath.Join(repoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var doc struct {
		Scenarios []struct {
			Name       string `json:"name"`
			CoverageID string `json:"coverage_id"`
			ByAdapter  map[string]struct {
				Applicable *bool `json:"applicable"`
				Script     []any `json:"script"`
			} `json:"by_adapter"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return out
	}
	for _, sc := range doc.Scenarios {
		cid := sc.CoverageID
		if cid == "" {
			cid = sc.Name
		}
		// Per-agent folder: for each agent this entry declares, prefer
		// the entry whose own folder has expected.jsonl FOR THAT AGENT.
		// If none does, the first entry wins.
		if _, ok := out.folderByAgent[cid]; !ok {
			out.folderByAgent[cid] = map[string]string{}
		}
		for agent := range sc.ByAdapter {
			hasSpec := agentHasExpectedJSONL(repoRoot, agent, sc.Name)
			cur, set := out.folderByAgent[cid][agent]
			if !set {
				out.folderByAgent[cid][agent] = sc.Name
			} else if hasSpec && !agentHasExpectedJSONL(repoRoot, agent, cur) {
				out.folderByAgent[cid][agent] = sc.Name
			}
		}
		// Multiple scenarios may share a coverage_id (e.g. basic-turn is
		// targeted by both basic-turn and multi-turn-conversation).
		// Prefer the entry whose folder has on-disk artifacts
		// (expected.jsonl) so the pipeline-strip annotation reflects the
		// canonical recording rather than whichever happened to be
		// listed last in the file.
		incoming := recipeEntry{Name: sc.Name, CoverageID: cid, ByAdapter: sc.ByAdapter}
		out.byName[sc.Name] = incoming
		if existing, dup := out.canonical[cid]; dup {
			incomingHasSpec := hasExpectedJSONL(repoRoot, sc.Name)
			existingHasSpec := hasExpectedJSONL(repoRoot, existing.Name)
			// Keep existing unless the incoming candidate is strictly
			// better (it has expected.jsonl, the existing one doesn't).
			if !(incomingHasSpec && !existingHasSpec) {
				continue
			}
		}
		out.canonical[cid] = incoming
	}
	return out
}

// agentHasExpectedJSONL reports whether
// replaydata/agents/<agent>/scenarios/<scenarioName>/expected.jsonl
// exists for one specific agent. Tighter than hasExpectedJSONL, which
// checks across all agents.
func agentHasExpectedJSONL(repoRoot, agent, scenarioName string) bool {
	_, err := os.Stat(filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios", scenarioName, "expected.jsonl"))
	return err == nil
}

// hasExpectedJSONL reports whether any agent's scenario folder for the
// given scenario `name` contains an expected.jsonl. Used by
// loadRecipeMap to disambiguate coverage_id collisions in favour of
// the scenario whose folder actually backs the matrix cell.
func hasExpectedJSONL(repoRoot, scenarioName string) bool {
	agentsDir := filepath.Join(repoRoot, "replaydata", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(agentsDir, e.Name(), "scenarios", scenarioName, "expected.jsonl")); err == nil {
			return true
		}
	}
	return false
}

// resolveScenarioFolderFromMap is the in-memory equivalent of
// resolveScenarioFolder: avoids re-reading scenarios.json N×M times in
// the catalog walker. Returns the canonical folder; for per-agent
// resolution (preferred when two scenarios share a coverage_id but
// cover different agents) use resolveScenarioFolderForAgent.
func resolveScenarioFolderFromMap(idx recipeIndex, coverageID string) string {
	if e, ok := idx.canonical[coverageID]; ok {
		return e.Name
	}
	return ""
}

// resolveScenarioFolderForAgent returns the replaydata folder name for
// one (agent, coverage_id) pair, preferring the folder whose
// replaydata/agents/<agent>/scenarios/<folder>/expected.jsonl exists.
// Returns "" when the agent is not declared by any scenarios.json entry
// under this coverage_id — callers should then fall back to the
// coverage_id directly rather than reading some unrelated agent's
// canonical folder.
func resolveScenarioFolderForAgent(idx recipeIndex, agent, coverageID string) string {
	if perAgent, ok := idx.folderByAgent[coverageID]; ok {
		if folder, ok := perAgent[agent]; ok {
			return folder
		}
	}
	return ""
}

// pipelineForCell computes the recipe/spec/recordings status for one
// (agent, scenario) cell.
func pipelineForCell(repoRoot, agent, coverageID, folder string, recipes recipeIndex) map[string]any {
	out := map[string]any{}

	// Recipe — present if scenarios.json has a per-adapter entry AND
	// (applicable is nil OR true). applicable=false explicitly marks
	// the cell N/A even when an entry exists. Prefer the recipe entry
	// whose name matches the per-agent canonical folder, so multi-entry
	// coverage_ids (e.g. interrupted-turn vs user-esc-interrupt) report
	// the recipe that actually produced the agent's recording.
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

	// Spec — count newline-terminated lines in expected.jsonl, minus
	// the meta line. Cheap byte scan; no JSON parse needed.
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
		// First line is the meta object; remainder are phases.
		if lines > 0 {
			phaseCount = lines - 1
		}
	}
	out["spec"] = map[string]any{"authored": specAuthored, "phase_count": phaseCount}

	// Recordings — top-level events.jsonl present = 1 latest; count
	// archive subdirs under recordings/.
	latest := false
	if _, err := os.Stat(filepath.Join(scenarioDir, "events.jsonl")); err == nil {
		latest = true
	}
	archiveCount := 0
	if entries, err := os.ReadDir(filepath.Join(scenarioDir, "recordings")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				archiveCount++
			}
		}
	}
	out["recordings"] = map[string]any{"latest": latest, "archive_count": archiveCount}

	return out
}

// handleScenarioSpec parses .specs/agent-scenarios.md on demand and
// returns the structured spec for one scenario id. Lookup matches by
// the kebab-case slug of each "### Feature: <name>" heading — same
// slug the coverage JSON uses for its scenarios[].id field.
//
// Response shape:
//
//	{ id, section, feature, scenarios: [{ text, expected: [..] }] }
//
// A scenario heading can have multiple Scenario:/Expected: blocks
// (e.g. session-end has clean-exit, SIGKILL, and crash variants); all
// are returned in order. 404 if the id has no matching heading.
func (s *Server) handleScenarioSpec(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/scenario-spec/")
	if id == "" {
		http.Error(w, "scenario id required", http.StatusBadRequest)
		return
	}
	specPath := s.resolveSpecPath()
	if specPath == "" {
		http.Error(w, ".specs/agent-scenarios.md not found in repo or main checkout", http.StatusNotFound)
		return
	}
	b, err := os.ReadFile(specPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("read %s: %v", specPath, err), http.StatusInternalServerError)
		return
	}
	out := parseScenarioSpec(string(b), id)
	if out == nil {
		http.Error(w, fmt.Sprintf("scenario %q not found in %s", id, specPath), http.StatusNotFound)
		return
	}
	writeJSON(w, out)
}

// resolveSpecPath mirrors resolveCoveragePath: look in the worktree
// first, then walk back to the main checkout via .git/worktrees if the
// worktree's `.git` is a pointer file. Returns "" if neither has the
// file.
func (s *Server) resolveSpecPath() string {
	direct := filepath.Join(s.RepoRoot, ".specs", "agent-scenarios.md")
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
	const prefix = "gitdir:"
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	main := filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
	candidate := filepath.Join(main, ".specs", "agent-scenarios.md")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// ScenarioSpec is the parsed shape of one Feature: heading from the
// catalog markdown.
type ScenarioSpec struct {
	ID        string             `json:"id"`
	Section   string             `json:"section"`
	Feature   string             `json:"feature"`
	Scenarios []ScenarioSpecCase `json:"scenarios"`
}

// ScenarioSpecCase is one Scenario:/Expected: pair under a Feature
// heading. Multi-paragraph descriptions are joined with newlines.
type ScenarioSpecCase struct {
	Text     string   `json:"text"`
	Expected []string `json:"expected"`
}

// parseScenarioSpec walks the catalog markdown and pulls out the
// Feature heading matching `id`. The catalog's structure is regular:
//
//	## <Section>
//	### Feature: <Name>
//	Scenario: <one or more lines>
//	Expected:
//	- bullet
//	- bullet
//	(blank)
//	Scenario: <next>
//	Expected:
//	- ...
//	---
//
// Lookup matches the kebab-case slug of `<Name>` against `id`.
func parseScenarioSpec(md string, id string) *ScenarioSpec {
	wantSlug := strings.ToLower(id)
	var (
		section    string
		feature    string
		curSlug    string
		out        *ScenarioSpec
		curCase    *ScenarioSpecCase
		inExpected bool
	)
	flush := func() {
		if curCase == nil {
			return
		}
		curCase.Text = strings.TrimSpace(curCase.Text)
		if out != nil {
			out.Scenarios = append(out.Scenarios, *curCase)
		}
		curCase = nil
		inExpected = false
	}
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			if out != nil {
				return out
			}
			section = strings.TrimSpace(strings.TrimPrefix(line, "## "))
		case strings.HasPrefix(line, "### Feature:"):
			flush()
			if out != nil {
				return out
			}
			feature = strings.TrimSpace(strings.TrimPrefix(line, "### Feature:"))
			curSlug = slugifyFeature(feature)
			if curSlug == wantSlug {
				out = &ScenarioSpec{ID: id, Section: section, Feature: feature}
			}
		case strings.HasPrefix(line, "### "):
			// Other H3s (rare in this file) — treat as feature break.
			flush()
			if out != nil {
				return out
			}
			feature = ""
			curSlug = ""
		case out != nil && strings.HasPrefix(line, "Scenario:"):
			flush()
			curCase = &ScenarioSpecCase{Text: strings.TrimSpace(strings.TrimPrefix(line, "Scenario:"))}
			inExpected = false
		case out != nil && strings.HasPrefix(line, "Expected:"):
			if curCase == nil {
				curCase = &ScenarioSpecCase{}
			}
			inExpected = true
		case out != nil && inExpected && strings.HasPrefix(strings.TrimSpace(line), "- "):
			curCase.Expected = append(curCase.Expected, strings.TrimPrefix(strings.TrimSpace(line), "- "))
		case out != nil && !inExpected && curCase != nil && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "---"):
			// Continuation of the scenario text.
			if curCase.Text != "" {
				curCase.Text += " "
			}
			curCase.Text += strings.TrimSpace(line)
		case strings.HasPrefix(line, "---"):
			flush()
			if out != nil {
				return out
			}
		}
	}
	flush()
	return out
}

// slugifyFeature converts a Feature: heading text like
// "Session reset (`/clear`, `/new`)" into the kebab id "session-reset"
// the coverage JSON uses. Strip parenthetical examples, lowercase,
// keep alnum + hyphens. The mapping must match
// .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json — there's a custom alias map
// for the handful of features whose canonical id diverges (e.g.
// "User-blocking tool call (question)" → "user-blocking-question").
func slugifyFeature(f string) string {
	if alias, ok := featureSlugAliases[f]; ok {
		return alias
	}
	// Drop parenthetical content.
	if i := strings.Index(f, "("); i >= 0 {
		f = strings.TrimSpace(f[:i])
	}
	out := make([]byte, 0, len(f))
	prevDash := false
	for i := 0; i < len(f); i++ {
		c := f[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
			prevDash = false
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			out = append(out, c)
			prevDash = false
		default:
			if !prevDash && len(out) > 0 {
				out = append(out, '-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(string(out), "-")
}

// featureSlugAliases handles the cases where the markdown Feature
// heading wording doesn't slugify cleanly into the coverage id. Keep
// this in sync with .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json — every id
// not derivable via slugifyFeature's default rule must have an entry.
var featureSlugAliases = map[string]string{
	"User-blocking tool call (question)":         "user-blocking-question",
	"User-blocking tool call (plan-mode approval)": "user-blocking-plan-mode-approval",
	"Tool gate via permission prompt":            "tool-gate-permission-prompt",
	"Session reset (`/clear`, `/new`)":           "session-reset",
	"Architect/Editor model pair":                "architect-editor-pair",
	"User ESC interrupt":                         "user-esc-interrupt",
}

// handleRecipes serves the run-cell.sh scenario recipe catalog
// (.claude/skills/ir:onboard-agent/scenarios.json). Used alongside
// /api/catalog: the catalog is the maintainer's "what could be
// tested" matrix, recipes is the "how it's actually driven" recipe
// book joined by each entry's `coverage_id` field.
//
// Multiple scenarios may share a coverage_id (e.g. basic-turn is
// targeted by both basic-turn and multi-turn-conversation). The
// client builds `recipesByCoverageId` as a 1:1 map and Map.set is
// last-wins, so without server-side dedup the wrong recipe would
// "own" the matrix row. We dedupe here using the same preference
// rule loadRecipeMap uses (favour the entry whose folder has
// expected.jsonl on disk).
func (s *Server) handleRecipes(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.RepoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("read scenarios.json: %v", err), http.StatusInternalServerError)
		return
	}
	deduped, err := dedupeRecipesByCoverageID(b, s.RepoRoot)
	if err != nil {
		// On any parse failure, fall back to serving the raw file —
		// the client may handle it less correctly but better than 500.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(deduped)
}

// dedupeRecipesByCoverageID parses scenarios.json's `scenarios` array
// and collapses entries sharing a coverage_id into one merged entry,
// then returns the re-serialized document. Non-`scenarios` fields
// (like `orchestrator_scenarios`) are passed through untouched.
//
// Merge rules for a (coverage_id) collision:
//   - The "primary" entry — whose top-level fields (name, description,
//     verify, requires, …) the merged entry inherits — is the one with
//     an on-disk expected.jsonl, tiebreaking on first-occurrence order.
//   - by_adapter is merged per-agent: for each agent declared by any
//     entry, the agent's block comes from the scenario whose folder
//     has expected.jsonl FOR THAT AGENT. This makes coverage_id
//     collisions where two entries cover different agents (e.g.
//     interrupted-turn for codex/pi/aider + user-esc-interrupt for
//     claudecode) carry both agents' canonical recipes through to the
//     client.
func dedupeRecipesByCoverageID(raw []byte, repoRoot string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	scenariosRaw, ok := doc["scenarios"]
	if !ok {
		return raw, nil
	}
	var scenarios []json.RawMessage
	if err := json.Unmarshal(scenariosRaw, &scenarios); err != nil {
		return nil, err
	}
	// First pass: identify the primary entry index for each coverage_id
	// (the one whose top-level fields the merged entry will inherit).
	type slot struct {
		index int
		name  string
	}
	primary := map[string]slot{}
	headers := make([]entryHeader, len(scenarios))
	cids := make([]string, len(scenarios))
	for i, sc := range scenarios {
		var h entryHeader
		if err := json.Unmarshal(sc, &h); err != nil {
			continue
		}
		headers[i] = h
		cid := h.CoverageID
		if cid == "" {
			cid = h.Name
		}
		cids[i] = cid
		if cid == "" {
			continue
		}
		if existing, dup := primary[cid]; dup {
			incomingHas := hasExpectedJSONL(repoRoot, h.Name)
			existingHas := hasExpectedJSONL(repoRoot, existing.name)
			if incomingHas && !existingHas {
				primary[cid] = slot{index: i, name: h.Name}
			}
			continue
		}
		primary[cid] = slot{index: i, name: h.Name}
	}
	// Second pass: for each coverage_id, build the merged by_adapter
	// from all sibling entries, picking per-agent the entry whose
	// folder has expected.jsonl for that agent.
	mergedByAdapter := map[string]map[string]json.RawMessage{}
	for i, cid := range cids {
		if cid == "" {
			continue
		}
		var sc struct {
			ByAdapter map[string]json.RawMessage `json:"by_adapter"`
		}
		if err := json.Unmarshal(scenarios[i], &sc); err != nil {
			continue
		}
		if _, ok := mergedByAdapter[cid]; !ok {
			mergedByAdapter[cid] = map[string]json.RawMessage{}
		}
		for agent, block := range sc.ByAdapter {
			cur, set := mergedByAdapter[cid][agent]
			if !set {
				mergedByAdapter[cid][agent] = block
				continue
			}
			// Find current source — replace only when incoming wins.
			incomingFolder := headers[i].Name
			currentFolder := folderForByAdapter(scenarios, headers, cid, agent, cur)
			if agentHasExpectedJSONL(repoRoot, agent, incomingFolder) &&
				!agentHasExpectedJSONL(repoRoot, agent, currentFolder) {
				mergedByAdapter[cid][agent] = block
			}
		}
	}
	// Third pass: emit one entry per coverage_id (the primary), with
	// by_adapter rewritten to the merged map. Non-primary indices are
	// dropped.
	emitted := map[string]bool{}
	filtered := make([]json.RawMessage, 0, len(primary))
	for i, sc := range scenarios {
		cid := cids[i]
		if cid == "" {
			// Pass through entries without a coverage_id.
			filtered = append(filtered, sc)
			continue
		}
		p, ok := primary[cid]
		if !ok || p.index != i || emitted[cid] {
			continue
		}
		emitted[cid] = true
		// Rewrite by_adapter with the merged map.
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(sc, &entry); err != nil {
			filtered = append(filtered, sc)
			continue
		}
		if merged, ok := mergedByAdapter[cid]; ok && len(merged) > 0 {
			b, err := json.Marshal(merged)
			if err != nil {
				// Marshal failure would silently fall back to the primary's
				// original by_adapter, hiding sibling agents the merge added.
				// Fail loudly so the maintainer sees the regression.
				return nil, fmt.Errorf("marshal merged by_adapter for coverage_id=%q: %w", cid, err)
			}
			entry["by_adapter"] = b
		}
		rewritten, err := json.Marshal(entry)
		if err != nil {
			filtered = append(filtered, sc)
			continue
		}
		filtered = append(filtered, rewritten)
	}
	newScenarios, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	doc["scenarios"] = newScenarios
	return json.Marshal(doc)
}

// folderForByAdapter finds which scenario name (folder) supplied the
// given by_adapter block for one (coverage_id, agent) cell during
// merging. Used to compare current vs. incoming sources by their
// per-agent expected.jsonl presence.
func folderForByAdapter(scenarios []json.RawMessage, headers []entryHeader, cid, agent string, block json.RawMessage) string {
	for i, h := range headers {
		hcid := h.CoverageID
		if hcid == "" {
			hcid = h.Name
		}
		if hcid != cid {
			continue
		}
		var sc struct {
			ByAdapter map[string]json.RawMessage `json:"by_adapter"`
		}
		if err := json.Unmarshal(scenarios[i], &sc); err != nil {
			continue
		}
		if b, ok := sc.ByAdapter[agent]; ok && bytes.Equal(b, block) {
			return h.Name
		}
	}
	return ""
}

// entryHeader is the slim header fields parsed from each scenarios.json
// entry. Re-declared at package scope so folderForByAdapter can take a
// slice of them.
type entryHeader struct {
	Name       string `json:"name"`
	CoverageID string `json:"coverage_id"`
}

// resolveCoveragePath finds the maintainer's
// .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json. Looks in the repo root first,
// then in the main checkout when the repo root is a git worktree.
// Returns "" if neither has the file.
func (s *Server) resolveCoveragePath() string {
	// Direct hit (main checkout, or a worktree the user has populated).
	direct := filepath.Join(s.RepoRoot, ".claude", "skills", "ir:onboard-agent", "agent-scenarios-coverage.json")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	// Worktree: <repo>/.git is a file containing "gitdir: <path>" where
	// <path> is <main>/.git/worktrees/<id>. The main checkout is the
	// parent of <path>/../.. (two levels up: workouts/<id> → workouts/
	// → .git/), then one more for the .git dir itself.
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
	// gitdir = <main>/.git/worktrees/<id>; main checkout = grandparent
	// of grandparent (worktrees/<id> → worktrees → .git → <main>).
	main := filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
	candidate := filepath.Join(main, ".claude", "skills", "ir:onboard-agent", "agent-scenarios-coverage.json")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// ScenarioListEntry is one row in /api/scenarios.
type ScenarioListEntry struct {
	Agent   string `json:"agent"`
	Subtree string `json:"subtree"` // "scenarios" | "regression"
	ID      string `json:"id"`
}

func (s *Server) handleScenariosList(w http.ResponseWriter, r *http.Request) {
	root := filepath.Join(s.RepoRoot, "replaydata", "agents")
	entries, _ := os.ReadDir(root)
	var out []ScenarioListEntry
	for _, agentEntry := range entries {
		if !agentEntry.IsDir() {
			continue
		}
		agent := agentEntry.Name()
		for _, subtree := range []string{"scenarios", "regression"} {
			subPath := filepath.Join(root, agent, subtree)
			scns, _ := os.ReadDir(subPath)
			for _, sd := range scns {
				if !sd.IsDir() {
					continue
				}
				out = append(out, ScenarioListEntry{
					Agent: agent, Subtree: subtree, ID: sd.Name(),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		if out[i].Subtree != out[j].Subtree {
			return out[i].Subtree < out[j].Subtree
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

// ScenarioDetail is the payload for /api/scenarios/{agent}/{subtree}/{id}.
type ScenarioDetail struct {
	Agent          string                   `json:"agent"`
	Subtree        string                   `json:"subtree"`
	ID             string                   `json:"id"`
	Meta           json.RawMessage          `json:"meta,omitempty"`            // recording-meta.json or null
	Degraded       bool                     `json:"degraded"`                  // true when there is no events.jsonl sidecar — the timeline is synthesized from the transcript via the shared classifier engine, not daemon-recorded
	Expected       *validate.ExpectedReport `json:"expected,omitempty"`        // expected.jsonl validated against events.jsonl (if file present)
	Transitions    []json.RawMessage        `json:"transitions"`               // state_transition rows from events.jsonl
	Tools          []ToolCall               `json:"tools,omitempty"`           // tool_use blocks extracted from transcript.jsonl
	LatestManifest *RecordingArchive        `json:"latest_manifest,omitempty"` // synthesized manifest for the live top-level recording, mirroring archive manifest fields so the viewer can render a uniform metadata panel
	Assessment     *AssessmentReport        `json:"assessment,omitempty"`      // Stage 1 (Assessment) point-in-time record from assessment.json, if present
}

// AssessmentReport is the persisted artifact of one Stage-1 assessment
// (per cell-lifecycle.md). One file per (agent, scenario) at
// replaydata/agents/<agent>/scenarios/<scenario>/assessment.json,
// overwritten on re-assessment — git is the history. The matrix in
// .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json is the current-state rollup;
// this struct preserves when and why the verdict was reached.
type AssessmentReport struct {
	SchemaVersion    int                `json:"schema_version"`
	ScenarioID       string             `json:"scenario_id"`
	Agent            string             `json:"agent"`
	AssessedAt       string             `json:"assessed_at"`
	AgentSupports    string             `json:"agent_supports"`    // yes / partial / no / unknown
	IrrlichtObserves string             `json:"irrlicht_observes"` // yes / partial / no / unknown / n/a
	Confidence       float64            `json:"confidence,omitempty"`
	Body             string             `json:"body"`
	Sources          []AssessmentSource `json:"sources,omitempty"`
	// Caveats documents known limitations / metric drifts that don't
	// invalidate the verdict but a maintainer should know about. E.g.
	// "feature is invisible to file-watching, but spec compliance is
	// unaffected" or "context utilization % overstates after a rewind".
	// One string per caveat, plain prose. Rendered as a bulleted
	// list in the viewer's Assessment panel.
	Caveats []string `json:"caveats,omitempty"`
}

// AssessmentSource is one citation backing an assessment verdict.
type AssessmentSource struct {
	Kind string `json:"kind"` // "url" | "file" | other
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

// ToolCall is one Anthropic-style tool_use block lifted from the
// transcript. Today this is the only signal irrlicht has for
// "agent invoked a tool" — the daemon's events.jsonl carries
// transcript_activity / parent_linked / hook_received but NOT a
// first-class tool_use Kind. Promoting tool_use to a lifecycle Kind
// is future work (issue TBD); until then the viewer derives it
// client-side from the transcript content.
type ToolCall struct {
	Ts        string `json:"ts"`                   // RFC3339 (from the message line's timestamp)
	SessionID string `json:"session_id,omitempty"` // sessionId on the message line
	Name      string `json:"name"`                 // tool name (e.g. "Bash", "Agent", "Read")
	ID        string `json:"id,omitempty"`         // tool_use id (toolu_…)
}

func (s *Server) handleScenarioDetail(w http.ResponseWriter, r *http.Request) {
	// URL form: /api/scenarios/{agent}/{subtree}/{id}[/frame/{name}]
	rest := strings.TrimPrefix(r.URL.Path, "/api/scenarios/")
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		http.Error(w, "usage: /api/scenarios/{agent}/{subtree}/{id}", http.StatusBadRequest)
		return
	}
	agent, subtree, id := parts[0], parts[1], parts[2]
	if subtree != "scenarios" && subtree != "regression" {
		http.Error(w, "subtree must be 'scenarios' or 'regression'", http.StatusBadRequest)
		return
	}
	if !slugRE.MatchString(agent) || !slugRE.MatchString(id) {
		http.Error(w, "agent and id must match ^[a-z0-9][a-z0-9_-]*$", http.StatusBadRequest)
		return
	}
	scenarioDir := filepath.Join(s.RepoRoot, "replaydata", "agents", agent, subtree, id)
	if _, err := os.Stat(scenarioDir); err != nil {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}
	// Recording history endpoints:
	//   /api/scenarios/{a}/{s}/{id}/recordings              → list archived recordings
	//   /api/scenarios/{a}/{s}/{id}/recordings/{name}       → archived recording detail (events + transcript + manifest)
	if len(parts) >= 4 && parts[3] == "recordings" {
		if len(parts) == 4 {
			s.handleRecordingsList(w, scenarioDir)
			return
		}
		if len(parts) == 5 {
			s.handleArchivedRecording(w, scenarioDir, parts[4])
			return
		}
	}

	d := ScenarioDetail{Agent: agent, Subtree: subtree, ID: id}
	if b, err := os.ReadFile(filepath.Join(scenarioDir, "recording-meta.json")); err == nil {
		d.Meta = b
	}
	// No events.jsonl sidecar → the viewer will synthesize the timeline
	// from the transcript via the shared classifier engine. Flag it so the
	// UI badges a reconstructed arc rather than passing it off as recorded.
	if _, err := os.Stat(filepath.Join(scenarioDir, "events.jsonl")); err != nil {
		d.Degraded = true
	}
	d.Transitions = readTransitionsRaw(filepath.Join(scenarioDir, "events.jsonl"))
	// Synthesize meta from events.jsonl when no recording-meta.json
	// exists on disk — every committed recording falls into this bucket
	// today; without synthesis the dropdown's metadata panel is empty.
	if d.Meta == nil {
		if synth := synthesizeMetaFromEvents(filepath.Join(scenarioDir, "events.jsonl")); synth != nil {
			d.Meta = synth
		}
	}
	// Spec-grounded expected.jsonl validation. Errors are swallowed so a
	// malformed expected.jsonl doesn't 500 the whole detail response —
	// the frontend treats a missing report as "not configured".
	if rep, err := validate.ValidateExpected(scenarioDir); err == nil && rep != nil {
		d.Expected = rep
	}
	d.Tools = extractToolCalls(filepath.Join(scenarioDir, "transcript.jsonl"))
	d.LatestManifest = buildLatestManifest(scenarioDir, agent, &d, s.RepoRoot)
	d.Assessment = loadAssessment(scenarioDir)
	writeJSON(w, d)
}

// loadAssessment reads <scenarioDir>/assessment.json if present.
// Returns nil on any error (missing file, malformed JSON) — the
// frontend treats absence as "no assessment recorded yet" and skips
// the panel.
func loadAssessment(scenarioDir string) *AssessmentReport {
	b, err := os.ReadFile(filepath.Join(scenarioDir, "assessment.json"))
	if err != nil {
		return nil
	}
	var rep AssessmentReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil
	}
	return &rep
}

// buildLatestManifest produces a RecordingArchive-shaped manifest for
// the live top-level recording so the viewer can render a uniform
// metadata panel for both Latest and archives. Prefers a real
// manifest.json at the scenario root (written by promote-recording.sh)
// when present; otherwise synthesizes from the data we already have
// loaded (meta.started_at, expected.summary, scenarios.json recipe
// hash). Returns nil when there isn't even a top-level events.jsonl
// to describe.
func buildLatestManifest(scenarioDir, agent string, d *ScenarioDetail, repoRoot string) *RecordingArchive {
	if _, err := os.Stat(filepath.Join(scenarioDir, "events.jsonl")); err != nil {
		return nil
	}
	m := &RecordingArchive{Name: "latest", DaemonVersion: "dev"}
	if b, err := os.ReadFile(filepath.Join(scenarioDir, "manifest.json")); err == nil {
		_ = json.Unmarshal(b, m)
		// `name` is internal-only — force "latest" regardless of file content.
		m.Name = "latest"
		return m
	}
	// Fall back to synthesis from in-memory data.
	if d.Expected != nil {
		if !d.Expected.RecordingStart.IsZero() {
			m.RecordingStartedAt = d.Expected.RecordingStart.Format(time.RFC3339Nano)
		}
		m.ExpectedPassRate = d.Expected.Summary
	}
	if m.RecordingStartedAt == "" && d.Meta != nil {
		var meta struct {
			StartedAt string `json:"started_at"`
		}
		if err := json.Unmarshal(d.Meta, &meta); err == nil {
			m.RecordingStartedAt = meta.StartedAt
		}
	}
	scenarioName := filepath.Base(scenarioDir)
	m.RecipeHash = computeRecipeHash(repoRoot, agent, scenarioName)
	return m
}

// computeRecipeHash mirrors promote-recording.sh's recipe_hash:
// sha256 of the compact-JSON serialization of scenarios.json's
// .scenarios[name].by_adapter[agent] block. Empty string on any
// failure — the dropdown panel renders the rest of the fields fine
// without it.
func computeRecipeHash(repoRoot, agent, scenarioName string) string {
	path := filepath.Join(repoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Scenarios []struct {
			Name      string                     `json:"name"`
			ByAdapter map[string]json.RawMessage `json:"by_adapter"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return ""
	}
	for _, sc := range doc.Scenarios {
		if sc.Name != scenarioName {
			continue
		}
		raw, ok := sc.ByAdapter[agent]
		if !ok {
			return ""
		}
		// Re-marshal compact to match jq -c's spacing exactly.
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return ""
		}
		compact, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(compact)
		return hex.EncodeToString(sum[:])
	}
	return ""
}

// extractToolCalls walks transcript.jsonl looking for Anthropic-style
// tool_use blocks inside message.content[]. Returns a flat list in
// chronological (transcript) order. Empty when the transcript has no
// tool calls or the file isn't a JSONL transcript (e.g. aider's .md).
//
// Schema notes:
//
//	{"timestamp":"…","sessionId":"…","message":{"content":[
//	  {"type":"tool_use","id":"toolu_…","name":"Bash","input":{…}}
//	]}}
//
// For multi-session recordings (session-end, session-reset chains)
// every UUID's content is concatenated in the file, so this single
// walk picks up tool calls across all of them.
func extractToolCalls(transcriptPath string) []ToolCall {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []ToolCall
	for scanner.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		msg, _ := raw["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].([]any)
		if len(content) == 0 {
			continue
		}
		ts, _ := raw["timestamp"].(string)
		sid, _ := raw["sessionId"].(string)
		for _, blkRaw := range content {
			blk, ok := blkRaw.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := blk["type"].(string); t != "tool_use" {
				continue
			}
			name, _ := blk["name"].(string)
			id, _ := blk["id"].(string)
			out = append(out, ToolCall{Ts: ts, SessionID: sid, Name: name, ID: id})
		}
	}
	return out
}

// RecordingArchive is one row of the recordings-list response —
// names a historical recording's directory plus its manifest fields.
type RecordingArchive struct {
	Name               string `json:"name"`               // dir name under recordings/
	PromotedAt         string `json:"promoted_at,omitempty"`
	DaemonVersion      string `json:"daemon_version,omitempty"`
	AgentCLIVersion    string `json:"agent_cli_version,omitempty"`
	RecipeHash         string `json:"recipe_hash,omitempty"`
	ExpectedPassRate   string `json:"expected_pass_rate,omitempty"`
	RecordingStartedAt string `json:"recording_started_at,omitempty"`
}

// ArchivedRecordingDetail is the payload for fetching one archived
// recording — events + transcript + the manifest + a fresh
// validation against the CURRENT top-level expected.jsonl. The
// re-validation is the drift signal: an archive that passed at
// promote-time (per manifest.expected_pass_rate) but fails the
// fresh evaluation means either the spec changed or the daemon
// drifted between then and now.
type ArchivedRecordingDetail struct {
	Name        string                   `json:"name"`
	Manifest    RecordingArchive         `json:"manifest"`
	Transitions []json.RawMessage        `json:"transitions"`
	Expected    *validate.ExpectedReport `json:"expected,omitempty"` // current spec vs this archive's events
	Tools       []ToolCall               `json:"tools,omitempty"`    // tool_use blocks extracted from archive's transcript.jsonl
}

// handleRecordingsList walks the scenario's recordings/ subdir and
// returns a sorted (newest-first) list of archived recordings with
// their manifest contents. Empty array when the dir doesn't exist
// or has no entries.
func (s *Server) handleRecordingsList(w http.ResponseWriter, scenarioDir string) {
	recordingsDir := filepath.Join(scenarioDir, "recordings")
	entries, err := os.ReadDir(recordingsDir)
	if err != nil {
		writeJSON(w, []RecordingArchive{}) // no recordings/ yet
		return
	}
	out := make([]RecordingArchive, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		archive := RecordingArchive{Name: e.Name()}
		if b, err := os.ReadFile(filepath.Join(recordingsDir, e.Name(), "manifest.json")); err == nil {
			_ = json.Unmarshal(b, &archive)
			archive.Name = e.Name() // defensive: manifest may not echo name
		}
		out = append(out, archive)
	}
	sort.Slice(out, func(i, j int) bool {
		// Newest-first by promoted_at (or name as a fallback for
		// archives that predate the manifest field).
		ai, bi := out[i].PromotedAt, out[j].PromotedAt
		if ai != "" || bi != "" {
			return ai > bi
		}
		return out[i].Name > out[j].Name
	})
	writeJSON(w, out)
}

// handleArchivedRecording returns the events / transcript / ground
// truth for one archived recording. Mirrors the shape of the main
// scenario detail response but pulls from recordings/<name>/.
func (s *Server) handleArchivedRecording(w http.ResponseWriter, scenarioDir, name string) {
	// Defense in depth — the slug regex on the URL parser only
	// constrained agent + id, not the archive name. Disallow path
	// traversal here.
	if strings.Contains(name, "..") || strings.ContainsRune(name, filepath.Separator) {
		http.Error(w, "invalid archive name", http.StatusBadRequest)
		return
	}
	archiveDir := filepath.Join(scenarioDir, "recordings", name)
	if _, err := os.Stat(archiveDir); err != nil {
		http.Error(w, "archive not found", http.StatusNotFound)
		return
	}
	d := ArchivedRecordingDetail{Name: name}
	if b, err := os.ReadFile(filepath.Join(archiveDir, "manifest.json")); err == nil {
		_ = json.Unmarshal(b, &d.Manifest)
		d.Manifest.Name = name
	}
	d.Transitions = readTransitionsRaw(filepath.Join(archiveDir, "events.jsonl"))
	// Re-evaluate the archive against the CURRENT top-level
	// expected.jsonl. Drift signal: archive may have passed at
	// promote-time but fail today because the spec moved.
	if rep, err := validate.ValidateExpectedAgainst(
		filepath.Join(scenarioDir, "expected.jsonl"),
		filepath.Join(archiveDir, "events.jsonl"),
	); err == nil && rep != nil {
		d.Expected = rep
	}
	d.Tools = extractToolCalls(filepath.Join(archiveDir, "transcript.jsonl"))
	writeJSON(w, d)
}

// synthesizeMetaFromEvents builds a recording-meta.json-compatible
// summary by scanning events.jsonl. Used as a fallback when the actual
// recording-meta.json doesn't exist (the case for every committed
// pre-Phase-1 recording). Marked `synthesized: true` so the frontend
// can render the panel with an honest provenance label.
//
// Output shape (compact JSON):
//
//	{
//	  "synthesized": true,
//	  "adapter": "<first transcript_new's adapter>",
//	  "started_at": "<events[0].ts>",
//	  "ended_at":   "<events[last].ts>",
//	  "duration_ms": <int>,
//	  "total_events": <int>,
//	  "kinds":        {"transcript_new": 2, ...},
//	  "presession_session_ids": ["proc-XXXX"],
//	  "real_session_ids":       ["8f4d493a-..."]
//	}
func synthesizeMetaFromEvents(path string) json.RawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	type rawEvent struct {
		Ts        string `json:"ts"`
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
		Adapter   string `json:"adapter,omitempty"`
	}
	var (
		adapter            string
		firstTs, lastTs    string
		total              int
		kinds              = map[string]int{}
		presessionSet      = map[string]struct{}{}
		realSet            = map[string]struct{}{}
	)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		b := scanner.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var ev rawEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			continue
		}
		total++
		if firstTs == "" {
			firstTs = ev.Ts
		}
		lastTs = ev.Ts
		if ev.Kind != "" {
			kinds[ev.Kind]++
		}
		if adapter == "" && ev.Adapter != "" {
			adapter = ev.Adapter
		}
		if ev.SessionID != "" {
			if strings.HasPrefix(ev.SessionID, "proc-") {
				presessionSet[ev.SessionID] = struct{}{}
			} else {
				realSet[ev.SessionID] = struct{}{}
			}
		}
	}
	if total == 0 {
		return nil
	}
	var durationMs int64
	if t0, err0 := time.Parse(time.RFC3339Nano, firstTs); err0 == nil {
		if t1, err1 := time.Parse(time.RFC3339Nano, lastTs); err1 == nil {
			durationMs = t1.Sub(t0).Milliseconds()
		}
	}
	keys := func(m map[string]struct{}) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	doc := map[string]any{
		"synthesized":            true,
		"adapter":                adapter,
		"started_at":             firstTs,
		"ended_at":               lastTs,
		"duration_ms":            durationMs,
		"total_events":           total,
		"kinds":                  kinds,
		"presession_session_ids": keys(presessionSet),
		"real_session_ids":       keys(realSet),
		"session_count":          map[string]int{"presession": len(presessionSet), "real": len(realSet)},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return b
}

func readTransitionsRaw(path string) []json.RawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var out []json.RawMessage
	// Track which sessions have already been "ended" so a daemon
	// re-firing transcript_removed for the same proc-<PID> doesn't
	// double up. The first of {presession_removed, transcript_removed,
	// process_exited} per session_id wins.
	ended := make(map[string]bool)
	// Track each session's last observed new_state so the synthetic
	// "ended" row can read as e.g. ready → ∅ instead of ∅ → ∅.
	lastState := make(map[string]string)
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return out
			}
			return out
		}
		var kind string
		if v, ok := raw["kind"]; ok {
			_ = json.Unmarshal(v, &kind)
		}
		var sid string
		if v, ok := raw["session_id"]; ok {
			_ = json.Unmarshal(v, &sid)
		}
		// Pick real state_transitions plus the three session-end
		// lifecycle kinds — the panel renders them as the visible
		// session disappearing from the dashboard. Without these the
		// "ready → ∅" tail is invisible and the panel reads as if the
		// session is still alive at recording end.
		switch kind {
		case "state_transition":
			// Track the running state so a follow-on session-end row
			// reads as <state> → ∅ instead of ∅ → ∅.
			var newState string
			if v, ok := raw["new_state"]; ok {
				_ = json.Unmarshal(v, &newState)
			}
			if newState != "" {
				lastState[sid] = newState
			}
			b, _ := json.Marshal(raw)
			out = append(out, b)
		case "transcript_removed", "process_exited", "presession_removed":
			if ended[sid] {
				continue
			}
			ended[sid] = true
			// Reshape the lifecycle event into a state_transition-
			// shaped row so the existing renderer just works. The
			// synthetic new_state "∅" renders as a neutral grey chip
			// (.badge.ended) — different from working/waiting/ready
			// so the reader can spot the lifecycle exit.
			raw["kind"] = json.RawMessage(`"state_transition"`)
			raw["new_state"] = json.RawMessage(`"∅"`)
			if kindJSON, err := json.Marshal(kind); err == nil {
				raw["reason"] = json.RawMessage(kindJSON)
			}
			if prev := lastState[sid]; prev != "" {
				if prevJSON, err := json.Marshal(prev); err == nil {
					raw["prev_state"] = json.RawMessage(prevJSON)
				}
			}
			b, _ := json.Marshal(raw)
			out = append(out, b)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
