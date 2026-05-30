package viewer

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// recipeEntry captures the slice of recipe fields the pipeline-state code uses,
// reconstructed per (scenario, agent) from each shard agent's Details.Recipe.
type recipeEntry struct {
	Name       string
	CoverageID string
	ByAdapter  map[string]struct {
		Applicable *bool `json:"applicable"`
		Script     []any `json:"script"`
	}
}

// recipeIndex is the result of one shard read. Holds the canonical
// recipe-per-coverage-id map plus a per-(agent, coverage_id) folder lookup.
// With shards there is exactly one row per coverage_id, so canonical and byName
// are keyed identically (shard.Name); the duplicated maps preserve the call
// surface the pipeline code already uses.
type recipeIndex struct {
	canonical     map[string]recipeEntry       // coverageID → recipe
	folderByAgent map[string]map[string]string // [coverageID][agent] → folder name
	byName        map[string]recipeEntry       // scenario name → recipe
}

// recipeAdapterEntry is the per-agent recipe shape (applicable + script) used
// both in recipeEntry.ByAdapter and when parsing a shard's Details.Recipe.
type recipeAdapterEntry struct {
	Applicable *bool `json:"applicable"`
	Script     []any `json:"script"`
}

// loadRecipeMap reads the per-scenario shards and builds the recipe index.
// Each shard is one row; each agent's recipeEntry comes from its metadata.json
// Details.Recipe (applicable + script); the folder comes from the agent's
// RecordingDir basename. Missing/malformed cells → empty index; callers
// tolerate "no recipe authored."
func loadRecipeMap(repoRoot string) recipeIndex {
	out := recipeIndex{
		canonical:     map[string]recipeEntry{},
		folderByAgent: map[string]map[string]string{},
		byName:        map[string]recipeEntry{},
	}
	// Scan each adapter's cells once, keyed by scenario_id.
	adapterCells := map[string]map[string]*shard.ShardAgent{}
	for _, agent := range shard.Agents(repoRoot) {
		adapterCells[agent] = shard.LoadAdapterCells(repoRoot, agent)
	}
	for _, sh := range shard.LoadAll(repoRoot) {
		cid := sh.Name
		entry := recipeEntry{
			Name:       cid,
			CoverageID: cid,
			ByAdapter: map[string]struct {
				Applicable *bool `json:"applicable"`
				Script     []any `json:"script"`
			}{},
		}
		out.folderByAgent[cid] = map[string]string{}
		for agent, cells := range adapterCells {
			cell, ok := cells[cid]
			if !ok {
				continue
			}
			// Recipe block (applicable + script) for the pipeline strip.
			if len(cell.Details.Recipe) > 0 {
				var rec recipeAdapterEntry
				if json.Unmarshal(cell.Details.Recipe, &rec) == nil {
					entry.ByAdapter[agent] = struct {
						Applicable *bool `json:"applicable"`
						Script     []any `json:"script"`
					}{Applicable: rec.Applicable, Script: rec.Script}
				}
			}
			// Folder: the recording-dir basename (e.g. "aider/scenarios/foo" →
			// "foo"), so measureScenario / the spec strip resolve the on-disk
			// recording for this (agent, cell).
			if cell.RecordingDir != "" {
				out.folderByAgent[cid][agent] = filepath.Base(cell.RecordingDir)
			}
		}
		out.canonical[cid] = entry
		out.byName[cid] = entry
	}
	return out
}

// resolveScenarioFolderForAgent returns the replaydata folder name for one
// (agent, coverage_id) pair from the shard's recording-dir. Returns "" when the
// agent has no recording for this coverage_id — callers fall back to the
// coverage_id itself.
func resolveScenarioFolderForAgent(idx recipeIndex, agent, coverageID string) string {
	if perAgent, ok := idx.folderByAgent[coverageID]; ok {
		if folder, ok := perAgent[agent]; ok {
			return folder
		}
	}
	return ""
}

// resolveCellFolder resolves the on-disk recording folder for one (agent,
// scenario) cell. It prefers the recording-dir-derived folder from the shard
// (which also captures variant-folder cells like pi's
// user-blocking-question → agent-question-pending), then the canonical
// "<dashed-id>_<name>" folder, and only as a last resort the bare coverage_id.
//
// The canonical fallback matters: catalog cells always live under the
// id-prefixed folder, so falling back to the bare slug — as the call sites
// once did — could never find a recording when the shard lacked a
// recording_dir (e.g. a recording wired only via artifacts, not recording_dir),
// silently rendering a recorded cell as pending-record in the viewer.
func resolveCellFolder(repoRoot string, idx recipeIndex, agent, coverageID string) string {
	if folder := resolveScenarioFolderForAgent(idx, agent, coverageID); folder != "" {
		return folder
	}
	if folder := shard.FolderForScenario(repoRoot, coverageID); folder != "" {
		return folder
	}
	return coverageID
}

// handleRecipes serves the run-cell.sh scenario recipe catalog. Built from the
// shards (one row per coverage_id, so no dedup is needed). Shape:
//
//	{"scenarios":[{"name":<coverage_id>,"coverage_id":<coverage_id>,
//	               "by_adapter":{<agent>:<recipe>},
//	               "folder_by_agent":{<agent>:<recording-folder>}}, ...]}
//
// — the structure the client's recipesByCoverageId map consumes. folder_by_agent
// gives the on-disk recording folder per agent: it equals the coverage_id for
// all but the variant-folder cells (e.g. pi user-blocking-question →
// agent-question-pending), where the client needs it to resolve the recording
// link/panel (viewer.js) — without it, those cells' detail recording can't be
// found (the #512 review finding this closes).
func (s *Server) handleRecipes(w http.ResponseWriter, r *http.Request) {
	shards := shard.LoadAll(s.RepoRoot)

	type recipeRow struct {
		Name          string                     `json:"name"`
		CoverageID    string                     `json:"coverage_id"`
		ByAdapter     map[string]json.RawMessage `json:"by_adapter"`
		FolderByAgent map[string]string          `json:"folder_by_agent,omitempty"`
	}
	// Scan each adapter's cells once, keyed by scenario_id.
	adapterCells := map[string]map[string]*shard.ShardAgent{}
	for _, a := range shard.Agents(s.RepoRoot) {
		adapterCells[a] = shard.LoadAdapterCells(s.RepoRoot, a)
	}
	rows := make([]recipeRow, 0, len(shards))
	for _, sh := range shards {
		row := recipeRow{Name: sh.Name, CoverageID: sh.Name, ByAdapter: map[string]json.RawMessage{}, FolderByAgent: map[string]string{}}
		// Cells for THIS scenario, per adapter.
		cells := map[string]*shard.ShardAgent{}
		for a, byCID := range adapterCells {
			if c, ok := byCID[sh.Name]; ok {
				cells[a] = c
			}
		}
		// Sorted agent keys for deterministic output.
		agentKeys := make([]string, 0, len(cells))
		for a := range cells {
			agentKeys = append(agentKeys, a)
		}
		sort.Strings(agentKeys)
		for _, a := range agentKeys {
			cell := cells[a]
			if cell == nil {
				continue
			}
			if rec := cell.Details.Recipe; len(rec) > 0 {
				row.ByAdapter[a] = rec
			}
			// Resolve the recording folder for this agent (variant-folder aware);
			// fall back to the coverage_id when the cell has no recording_dir.
			folder := sh.Name
			if rd := cell.RecordingDir; rd != "" {
				folder = filepath.Base(rd)
			}
			row.FolderByAgent[a] = folder
		}
		rows = append(rows, row)
	}

	doc := map[string]any{"scenarios": rows}
	b, err := json.Marshal(doc)
	if err != nil {
		http.Error(w, "marshal recipes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}
