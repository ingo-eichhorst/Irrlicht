package viewer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

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
// canonical recipe-per-coverage-id map and a per-(agent, coverage_id)
// folder lookup. Two entries may share a coverage_id while covering
// disjoint agent sets; the canonical entry alone can't pick the right
// replaydata folder for each agent — folderByAgent does.
type recipeIndex struct {
	canonical     map[string]recipeEntry       // coverageID → canonical recipe
	folderByAgent map[string]map[string]string // [coverageID][agent] → folder name
	byName        map[string]recipeEntry       // scenario name → recipe (for per-agent recipe lookups)
}

// entryHeader is the slim header fields parsed from each scenarios.json
// entry. Package scope so folderForByAdapter can take a slice of them.
type entryHeader struct {
	Name       string `json:"name"`
	CoverageID string `json:"coverage_id"`
}

// loadRecipeMap reads scenarios.json once and returns a coverageID-keyed
// lookup plus a per-(agent, coverage_id) folder map. Missing or malformed
// file → empty index; callers tolerate "no recipe authored."
func loadRecipeMap(repoRoot string) recipeIndex {
	store := RecordingStore{RepoRoot: repoRoot}
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
		// Per-agent folder: for each agent this entry declares, prefer the
		// entry whose own folder has expected.jsonl FOR THAT AGENT.
		if _, ok := out.folderByAgent[cid]; !ok {
			out.folderByAgent[cid] = map[string]string{}
		}
		for agent := range sc.ByAdapter {
			hasSpec := store.agentHasExpectedJSONL(agent, sc.Name)
			cur, set := out.folderByAgent[cid][agent]
			if !set {
				out.folderByAgent[cid][agent] = sc.Name
			} else if hasSpec && !store.agentHasExpectedJSONL(agent, cur) {
				out.folderByAgent[cid][agent] = sc.Name
			}
		}
		// Multiple scenarios may share a coverage_id. Prefer the entry whose
		// folder has on-disk artifacts (expected.jsonl) so the pipeline-strip
		// reflects the canonical recording rather than file order.
		incoming := recipeEntry{Name: sc.Name, CoverageID: cid, ByAdapter: sc.ByAdapter}
		out.byName[sc.Name] = incoming
		if existing, dup := out.canonical[cid]; dup {
			incomingHasSpec := store.hasExpectedJSONL(sc.Name)
			existingHasSpec := store.hasExpectedJSONL(existing.Name)
			if !(incomingHasSpec && !existingHasSpec) {
				continue
			}
		}
		out.canonical[cid] = incoming
	}
	return out
}

// handleRecipes serves the run-cell.sh scenario recipe catalog
// (scenarios.json), deduped by coverage_id so the client's last-wins
// recipesByCoverageId map keeps the canonical recipe per matrix row.
func (s *Server) handleRecipes(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.RepoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("read scenarios.json: %v", err), http.StatusInternalServerError)
		return
	}
	deduped, err := dedupeRecipesByCoverageID(b, s.RepoRoot)
	if err != nil {
		// On any parse failure, serve the raw file — the client handles it
		// less correctly but better than a 500.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(deduped)
}

// dedupeRecipesByCoverageID collapses scenarios.json `scenarios` entries
// sharing a coverage_id into one merged entry, then re-serializes the
// document. Non-`scenarios` fields pass through untouched.
//
// Merge rules for a (coverage_id) collision:
//   - The "primary" entry — whose top-level fields the merged entry
//     inherits — is the one with an on-disk expected.jsonl, tiebreaking on
//     first-occurrence order.
//   - by_adapter is merged per-agent: each agent's block comes from the
//     scenario whose folder has expected.jsonl FOR THAT AGENT.
func dedupeRecipesByCoverageID(raw []byte, repoRoot string) ([]byte, error) {
	store := RecordingStore{RepoRoot: repoRoot}
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
	// First pass: identify the primary entry index for each coverage_id.
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
			incomingHas := store.hasExpectedJSONL(h.Name)
			existingHas := store.hasExpectedJSONL(existing.name)
			if incomingHas && !existingHas {
				primary[cid] = slot{index: i, name: h.Name}
			}
			continue
		}
		primary[cid] = slot{index: i, name: h.Name}
	}
	// Second pass: build merged by_adapter from all sibling entries, picking
	// per-agent the entry whose folder has expected.jsonl for that agent.
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
			incomingFolder := headers[i].Name
			currentFolder := folderForByAdapter(scenarios, headers, cid, agent, cur)
			if store.agentHasExpectedJSONL(agent, incomingFolder) &&
				!store.agentHasExpectedJSONL(agent, currentFolder) {
				mergedByAdapter[cid][agent] = block
			}
		}
	}
	// Third pass: emit one entry per coverage_id (the primary), with
	// by_adapter rewritten to the merged map. Non-primary indices dropped.
	emitted := map[string]bool{}
	filtered := make([]json.RawMessage, 0, len(primary))
	for i, sc := range scenarios {
		cid := cids[i]
		if cid == "" {
			filtered = append(filtered, sc) // pass through entries without a coverage_id
			continue
		}
		p, ok := primary[cid]
		if !ok || p.index != i || emitted[cid] {
			continue
		}
		emitted[cid] = true
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(sc, &entry); err != nil {
			filtered = append(filtered, sc)
			continue
		}
		if merged, ok := mergedByAdapter[cid]; ok && len(merged) > 0 {
			b, err := json.Marshal(merged)
			if err != nil {
				// Silently falling back would hide sibling agents the merge
				// added; fail loudly so the maintainer sees the regression.
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

// folderForByAdapter finds which scenario name (folder) supplied the given
// by_adapter block for one (coverage_id, agent) cell during merging.
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
