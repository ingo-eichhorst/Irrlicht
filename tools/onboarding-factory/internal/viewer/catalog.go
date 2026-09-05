package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/shard"
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
// The matrix is scoped to one execution profile too (#1889): ?profile= picks
// which profile's recordings the measurement axis judges, defaulting to
// cli-local so the pre-existing overview is unchanged. Measuring across both
// profiles would let a Desktop recording decide a CLI cell's colour.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	profile, err := profileFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b, sourceTag, err := s.buildCatalogJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("build catalog: %v", err), http.StatusInternalServerError)
		return
	}
	var top map[string]any
	if json.Unmarshal(b, &top) == nil {
		top["execution_profile"] = string(profile)
		annotateMeasurements(top, s.RepoRoot, profile)
		annotatePipelineState(top, s.RepoRoot, profile)
		annotateDisplayState(top) // after measurements: the recording axis feeds the rollup
		if out, mErr := json.Marshal(top); mErr == nil {
			b = out
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Catalog-Source", sourceTag)
	w.Header().Set("X-Execution-Profile", string(profile))
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

	// The capability model supplies the axes for a declared-dead pair that has
	// no cell directory (#1369). Without it those render "unknown" here while
	// `of status` reports n/a or unobservable for the same pair — and the
	// whole point of the model is that such a cell need not exist on disk.
	caps, err := matrix.LoadCapabilities(s.RepoRoot)
	if err != nil {
		return nil, "", err
	}

	scenarios := make([]map[string]any, 0, len(shards))
	for _, sh := range shards {
		coverage := make(map[string]any, len(agentSlugs))
		for _, slug := range agentSlugs {
			cell := adapterCells[slug][sh.Name]
			if cell == nil {
				// A pair the capability model declares structurally dead has
				// no directory by design (#1369). Fill it from the model so
				// this endpoint agrees with `of status`; without it the cell
				// renders "unknown", which is what the rest of the system
				// means by "nobody has assessed this".
				cell = caps.SyntheticCell(slug, sh.Name)
			}
			coverage[slug] = buildCellVerdict(cell)
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
		// applicable is false only when the recipe explicitly marks applicable:false
		// (a deliberate record_blocked deferral); absent/true recipe → true. Feeds
		// the display state so such cells read not-applicable, not pending-record.
		"applicable": true,
	}
	if ag == nil {
		return cell
	}
	if recipeApplicableFalse(ag.Details.Recipe) {
		cell["applicable"] = false
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

// recipeApplicableFalse reports whether a cell's recipe explicitly marks
// applicable:false (a deliberate record_blocked deferral). Absent or
// applicable:true/nil recipe → false.
func recipeApplicableFalse(recipe json.RawMessage) bool {
	if len(recipe) == 0 {
		return false
	}
	var r recipeAdapterEntry
	if json.Unmarshal(recipe, &r) != nil {
		return false
	}
	return r.Applicable != nil && !*r.Applicable
}
