package matrix

import (
	"encoding/json"
	"os"
)

// This file derives agent-scenarios-coverage.json — the dashboard's coverage
// rollup — FROM the assessments, instead of letting it be a hand-maintained
// denormalized copy of the same axes. Before #508 the rollup carried
// {agent_supports, daemon_capability, driver_capability} per (agent, scenario)
// that mirrored each cell's assessment.json with NO generator and NO gate on
// the rollup⟺assessment leg, so it drifted freely (a fan-out grep found ~30
// cells out of sync). Generating it makes that drift impossible: `matrix
// rollup` regenerates, `matrix rollup --check` fails CI when the committed file
// no longer matches the assessments.
//
// The axes are DERIVED (assessment, or the unassessed default). The editorial
// `notes` and the `legend` block are human-owned OVERLAY: they are carried
// forward from the prior file verbatim, so regeneration never clobbers a
// maintainer's prose. This is the stable fixpoint — regenerating an unchanged
// tree reproduces the identical file.

// RollupDoc is the serialized shape of agent-scenarios-coverage.json. Field
// order here is the emitted JSON order.
type RollupDoc struct {
	Version       int              `json:"version"`
	GeneratedAt   string           `json:"generated_at"`
	SourceCatalog string           `json:"source_catalog"`
	Legend        any              `json:"legend"`
	Agents        []RollupAgent    `json:"agents"`
	Scenarios     []RollupScenario `json:"scenarios"`
}

// RollupAgent is one entry in the rollup's agents[].
type RollupAgent struct {
	ID        string `json:"id"`
	Onboarded bool   `json:"onboarded"`
}

// RollupScenario is one catalog row with its per-agent coverage.
type RollupScenario struct {
	ID       string                        `json:"id"`
	Coverage map[string]RollupCoverageCell `json:"coverage"`
}

// RollupCoverageCell is one (agent) cell: derived axes + editorial notes.
type RollupCoverageCell struct {
	AgentSupports    string `json:"agent_supports"`
	DaemonCapability string `json:"daemon_capability"`
	DriverCapability string `json:"driver_capability"`
	Notes            string `json:"notes"`
}

// RollupOverlay carries the human-owned / stable fields preserved across
// regeneration: per-(scenarioID, agent) notes, the legend block, source_catalog,
// and generated_at. Any may be zero — BuildRollup fills sensible defaults.
//
// GeneratedAt is carried forward (#510 P2): the shard model stores ONE
// assessment per cell — the migrator-chosen canonical variant — whose
// assessed_at can differ from the legacy candidate-dir-first resolution that
// originally stamped this field (e.g. aider/basic-turn: the shard keeps the
// recording folder's 2026-05-25 assessment, while the legacy rollup stamped the
// basic-turn folder's 2026-05-23). The axes are identical either way; only the
// timestamp diverges. Preserving the committed generated_at keeps the rollup a
// byte-stable fixpoint across the storage migration. A first-time generation
// (empty overlay) still derives it from the max assessed_at.
type RollupOverlay struct {
	Notes         map[string]map[string]string // scenarioID → agent → notes
	Legend        any
	SourceCatalog string
	GeneratedAt   string
}

// BuildRollup derives the coverage rollup from the loaded assessments. Axes
// come from each cell's assessment (or the unassessed default
// unknown/unknown/ready, matching the viewer's buildCellVerdict default);
// notes/legend/source_catalog come from the overlay. generated_at is set to the
// most recent assessment's assessed_at so the output is DETERMINISTIC (stable
// across re-runs of an unchanged tree) rather than wall-clock.
func (m *Matrix) BuildRollup(overlay RollupOverlay) RollupDoc {
	agents := m.Agents()
	doc := RollupDoc{
		Version:       1,
		SourceCatalog: overlay.SourceCatalog,
		Legend:        overlay.Legend,
	}
	if doc.SourceCatalog == "" {
		doc.SourceCatalog = "replaydata/agents/scenarios.json"
	}

	for _, a := range agents {
		doc.Agents = append(doc.Agents, RollupAgent{ID: a, Onboarded: true})
	}

	maxAssessedAt := ""
	for _, cat := range m.catalog {
		cov := make(map[string]RollupCoverageCell, len(agents))
		for _, a := range agents {
			supports, daemon, driver, assessedAt := m.rollupAxes(a, cat.ID)
			if assessedAt > maxAssessedAt {
				maxAssessedAt = assessedAt
			}
			notes := ""
			if byAgent, ok := overlay.Notes[cat.ID]; ok {
				notes = byAgent[a]
			}
			cov[a] = RollupCoverageCell{
				AgentSupports:    supports,
				DaemonCapability: daemon,
				DriverCapability: driver,
				Notes:            notes,
			}
		}
		doc.Scenarios = append(doc.Scenarios, RollupScenario{
			ID:       cat.ID,
			Coverage: cov,
		})
	}
	// Prefer the carried-forward generated_at (stable fixpoint across the shard
	// migration; see RollupOverlay.GeneratedAt). Fall back to the derived max for
	// a first-time generation with no prior file.
	if overlay.GeneratedAt != "" {
		doc.GeneratedAt = overlay.GeneratedAt
	} else {
		doc.GeneratedAt = maxAssessedAt
	}
	return doc
}

// rollupAxes resolves one (agent, catalog-id) cell's axes for the rollup from
// the loaded shard (m.shards, keyed by coverage_id), falling back to the
// unassessed default. The shard carries the canonical per-cell assessment the
// migrator chose, so this reproduces the legacy candidate-dir resolution
// byte-for-byte. Works for any catalog id, applicable or not — the rollup covers
// every catalog row × every agent.
func (m *Matrix) rollupAxes(agent, coverageID string) (supports, daemon, driver, assessedAt string) {
	if cells, ok := m.agentCells[agent]; ok {
		if c, ok := cells[coverageID]; ok {
			if _, rep := cellAssessment(c); rep != nil {
				return rep.AgentSupports, rep.DaemonCapability, rep.DriverCapability, rep.AssessedAt
			}
		}
	}
	return "unknown", "unknown", "ready", ""
}

// CatalogIDs returns the catalog row ids in catalog order.
func (m *Matrix) CatalogIDs() []string {
	out := make([]string, 0, len(m.catalog))
	for _, c := range m.catalog {
		out = append(out, c.ID)
	}
	return out
}

// MarshalRollup renders a rollup doc as 2-space-indented JSON with a trailing
// newline (the repo's JSON-file convention), so the written file is a stable
// fixpoint that `--check` can compare against byte-for-byte.
func MarshalRollup(doc RollupDoc) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ReadOverlay extracts the human-owned fields (per-cell notes, the legend
// block, source_catalog) from an existing rollup file so BuildRollup can carry
// them forward. A missing/unparseable file yields an empty overlay (defaults
// apply) — never an error, so a first-time generation just works.
func ReadOverlay(path string) RollupOverlay {
	var ov RollupOverlay
	b, err := os.ReadFile(path)
	if err != nil {
		return ov
	}
	var doc struct {
		GeneratedAt   string `json:"generated_at"`
		Legend        any    `json:"legend"`
		SourceCatalog string `json:"source_catalog"`
		Scenarios     []struct {
			ID       string `json:"id"`
			Coverage map[string]struct {
				Notes string `json:"notes"`
			} `json:"coverage"`
		} `json:"scenarios"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return ov
	}
	ov.GeneratedAt = doc.GeneratedAt
	ov.Legend = doc.Legend
	ov.SourceCatalog = doc.SourceCatalog
	ov.Notes = map[string]map[string]string{}
	for _, sc := range doc.Scenarios {
		byAgent := map[string]string{}
		for agent, cell := range sc.Coverage {
			if cell.Notes != "" {
				byAgent[agent] = cell.Notes
			}
		}
		if len(byAgent) > 0 {
			ov.Notes[sc.ID] = byAgent
		}
	}
	return ov
}
