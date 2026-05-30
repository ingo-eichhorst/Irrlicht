package viewer

import (
	"fmt"
	"net/http"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// handleScenarioSpec returns the agent-AGNOSTIC spec for one scenario, read
// straight from the catalog shard (replaydata/agents/scenarios.json). The
// scenario detail view shows ONLY this — description + process + acceptance
// criteria — nothing agent-specific. The path param is the scenario name
// (kebab slug), the same id the catalog rows carry. 404 if unknown.
func (s *Server) handleScenarioSpec(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/scenario-spec/")
	if id == "" {
		http.Error(w, "scenario id required", http.StatusBadRequest)
		return
	}
	sh, ok := shard.Load(s.RepoRoot, id)
	if !ok {
		http.Error(w, fmt.Sprintf("scenario %q not found in catalog", id), http.StatusNotFound)
		return
	}
	writeJSON(w, ScenarioSpec{
		ID:                 sh.ID,
		Name:               sh.Name,
		Description:        sh.Description,
		Process:            sh.Process,
		AcceptanceCriteria: sh.AcceptanceCriteria,
	})
}
