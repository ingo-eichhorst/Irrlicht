package viewer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestScenarioCatalogNoDrift guards the three sources of truth that the
// overview matrix depends on staying in sync. The matrix is projected ONLY
// from scenarios.json's catalog[] (buildCatalogJSON), and annotateCatalogCodes
// derives each row's section.index from that same list — so a scenario with
// no catalog row is invisible AND gets no index number. The three sources:
//
//   - catalog[]   — the only thing the overview renders; drives row + index.
//   - scenarios[] — the recipe registry; each entry's coverage_id must name a
//     real catalog row, else the recipe folds into a nonexistent cell.
//   - replaydata/agents/<agent>/scenarios/<folder> — recorded fixtures; each
//     must resolve to a catalog row either directly (folder == catalog id) or
//     via a registry entry whose name == folder and whose coverage_id is a
//     catalog id (the deliberate-alias case, e.g. multi-turn-conversation →
//     basic-turn).
//
// A folder resolving to neither is the drift this test catches — exactly how
// quota-burndown / subscription-detection sat invisible after being committed
// as raw folders with no catalog or registry entry. Retired fixtures live
// under <agent>/regression/, a SIBLING of scenarios/, so the glob below leaves
// them out of scope by construction.
func TestScenarioCatalogNoDrift(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	scenariosPath := filepath.Join(root, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(scenariosPath)
	if err != nil {
		t.Skipf("scenarios.json not reachable from test cwd (hermetic build?): %v", err)
	}
	var doc struct {
		Catalog []struct {
			ID string `json:"id"`
		} `json:"catalog"`
		Scenarios []struct {
			Name       string `json:"name"`
			CoverageID string `json:"coverage_id"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse scenarios.json: %v", err)
	}

	catalogIDs := make(map[string]bool, len(doc.Catalog))
	for _, c := range doc.Catalog {
		catalogIDs[c.ID] = true
	}
	coverageByName := make(map[string]string, len(doc.Scenarios)) // registry folder name → coverage_id
	for _, s := range doc.Scenarios {
		coverageByName[s.Name] = s.CoverageID
	}

	// Guard 1: every registry coverage_id names a real catalog row. A dangling
	// coverage_id silently routes a recipe to a matrix cell that never renders.
	for _, s := range doc.Scenarios {
		switch {
		case s.CoverageID == "":
			t.Errorf("registry scenario %q has empty coverage_id", s.Name)
		case !catalogIDs[s.CoverageID]:
			t.Errorf("registry scenario %q -> coverage_id %q has no catalog[] row", s.Name, s.CoverageID)
		}
	}

	// Guard 2: every recorded scenario folder resolves to a catalog row.
	folders, _ := filepath.Glob(filepath.Join(root, "replaydata", "agents", "*", "scenarios", "*"))
	if len(folders) == 0 {
		t.Skip("no replaydata scenario folders reachable; skipping on-disk drift check")
	}
	var orphans []string
	for _, dir := range folders {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		folder := filepath.Base(dir)
		resolves := catalogIDs[folder]
		if !resolves {
			if cov, ok := coverageByName[folder]; ok && catalogIDs[cov] {
				resolves = true
			}
		}
		if !resolves {
			// path shape: …/<agent>/scenarios/<folder>
			agent := filepath.Base(filepath.Dir(filepath.Dir(dir)))
			orphans = append(orphans, agent+"/"+folder)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Errorf("orphan scenario folders with no catalog[] row and no registry alias to one — "+
			"invisible in the overview matrix, no index number. Fix by adding a catalog[] entry, "+
			"or a scenarios[] recipe whose coverage_id names a catalog row, or retiring the folder "+
			"to <agent>/regression/:\n  %v", orphans)
	}
}

// knownTransports is the closed set of transport tags (#496 RC7). They mirror
// the domain Source sum: FilesUnderRoot / FilesUnderCWD are line_based;
// ProcessOwnedStore (opencode) is structured_store.
var knownTransports = map[string]bool{"line_based": true, "structured_store": true}

// TestTransportAxis guards the requires_transport axis: every adapter's
// capabilities.json must declare a known transport, and every scenario's
// requires_transport (when present) must list only known transports — else a
// scenario silently applies to (or excludes) the wrong adapters.
func TestTransportAxis(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")

	caps, _ := filepath.Glob(filepath.Join(root, "replaydata", "agents", "*", "capabilities.json"))
	if len(caps) == 0 {
		t.Skip("no capabilities.json reachable; skipping transport-axis check")
	}
	for _, p := range caps {
		agent := filepath.Base(filepath.Dir(p))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: read: %v", agent, err)
			continue
		}
		var c struct {
			Transport string `json:"transport"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Errorf("%s: parse capabilities.json: %v", agent, err)
			continue
		}
		if !knownTransports[c.Transport] {
			t.Errorf("%s: capabilities.json transport %q is not a known transport %v", agent, c.Transport, keysOf(knownTransports))
		}
	}

	scenariosPath := filepath.Join(root, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(scenariosPath)
	if err != nil {
		t.Skipf("scenarios.json not reachable: %v", err)
	}
	var doc struct {
		Scenarios []struct {
			Name              string   `json:"name"`
			RequiresTransport []string `json:"requires_transport"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse scenarios.json: %v", err)
	}
	for _, s := range doc.Scenarios {
		for _, tr := range s.RequiresTransport {
			if !knownTransports[tr] {
				t.Errorf("scenario %q requires_transport %q is not a known transport %v", s.Name, tr, keysOf(knownTransports))
			}
		}
	}
}

// TestCatalogRollupBijection guards the catalog ⟺ rollup leg (#496 RC5): the
// overview renders rows from scenarios.json catalog[], and overlays editorial
// coverage from agent-scenarios-coverage.json. A rollup row naming no catalog
// cell is a phantom; a catalog cell with no rollup row renders with no coverage
// data. Either is drift the prose `comm` check only printed — this fails it.
func TestCatalogRollupBijection(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	skill := filepath.Join(root, ".claude", "skills", "ir:onboard-agent")

	cb, err := os.ReadFile(filepath.Join(skill, "scenarios.json"))
	if err != nil {
		t.Skipf("scenarios.json not reachable: %v", err)
	}
	var sdoc struct {
		Catalog []struct {
			ID string `json:"id"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(cb, &sdoc); err != nil {
		t.Fatalf("parse scenarios.json: %v", err)
	}
	rb, err := os.ReadFile(filepath.Join(skill, "agent-scenarios-coverage.json"))
	if err != nil {
		t.Skipf("agent-scenarios-coverage.json not reachable: %v", err)
	}
	var rdoc struct {
		Scenarios []struct {
			ID string `json:"id"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(rb, &rdoc); err != nil {
		t.Fatalf("parse agent-scenarios-coverage.json: %v", err)
	}

	catalog := make(map[string]bool, len(sdoc.Catalog))
	for _, c := range sdoc.Catalog {
		catalog[c.ID] = true
	}
	rollup := make(map[string]bool, len(rdoc.Scenarios))
	for _, s := range rdoc.Scenarios {
		rollup[s.ID] = true
		if !catalog[s.ID] {
			t.Errorf("rollup id %q has no catalog[] row (phantom editorial cell)", s.ID)
		}
	}
	for id := range catalog {
		if !rollup[id] {
			t.Errorf("catalog[] row %q is missing from the rollup — renders with no editorial coverage; add a row to agent-scenarios-coverage.json", id)
		}
	}
}

func keysOf(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
