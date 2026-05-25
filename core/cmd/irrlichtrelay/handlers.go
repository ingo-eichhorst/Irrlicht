package main

import (
	"encoding/json"
	"net/http"

	"irrlicht/core/domain/session"
)

// handleSessions re-serves the daemon's /api/v1/sessions shape, built from the
// relay's flattened session cache. No orchestrator state is forwarded in v0,
// so the dashboard groups by project name (BuildDashboard with nil orch).
func handleSessions(h *hub) http.HandlerFunc {
	type sessionsResponse struct {
		Groups []*session.AgentGroup `json:"groups"`
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		groups := session.BuildDashboard(h.buildSessions(), nil)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionsResponse{Groups: groups})
	}
}

// handleAgents re-serves /api/v1/agents as the union of every connected
// daemon's adapter registry, matching the daemon's agentEntry shape.
func handleAgents(h *hub) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.buildAgents())
	}
}

// handleVersion serves the relay's own build version.
func handleVersion(version string) http.HandlerFunc {
	type versionResp struct {
		Version string `json:"version"`
	}
	body, _ := json.Marshal(versionResp{Version: version})
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}
