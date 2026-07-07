package main

import (
	"context"
	"encoding/json"
	"net/http"

	"irrlicht/core/domain/session"
)

// headerContentType and contentTypeJSON name the response header/value pair
// set by every JSON-encoding handler in this file.
const (
	headerContentType = "Content-Type"
	contentTypeJSON   = "application/json"
)

// workspaceCtxKey carries the validated workspace from requireToken to the read
// handlers. Its own type avoids collisions with any other context value.
type workspaceCtxKey struct{}

// withWorkspace attaches the token's workspace to a request context.
func withWorkspace(ctx context.Context, workspace string) context.Context {
	return context.WithValue(ctx, workspaceCtxKey{}, workspace)
}

// workspaceOf reads the workspace requireToken attached, or "" (the default
// workspace) on a no-auth relay where the gate is a pass-through.
func workspaceOf(r *http.Request) string {
	ws, _ := r.Context().Value(workspaceCtxKey{}).(string)
	return ws
}

// handleSessions re-serves the daemon's /api/v1/sessions shape, built from the
// caller's workspace slice of the relay's session cache. No orchestrator state
// is forwarded in v0, so the dashboard groups by project name (BuildDashboard
// with nil orch).
func handleSessions(h *hub) http.HandlerFunc {
	type sessionsResponse struct {
		Groups []*session.AgentGroup `json:"groups"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		groups := session.BuildDashboard(h.buildSessions(workspaceOf(r)), nil)
		w.Header().Set(headerContentType, contentTypeJSON)
		_ = json.NewEncoder(w).Encode(sessionsResponse{Groups: groups})
	}
}

// handleAgents re-serves /api/v1/agents as the union of the caller's workspace
// daemons' adapter registries, matching the daemon's agentEntry shape.
func handleAgents(h *hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentType, contentTypeJSON)
		_ = json.NewEncoder(w).Encode(h.buildAgents(workspaceOf(r)))
	}
}

// handleVersion serves the relay's own build version.
func handleVersion(version string) http.HandlerFunc {
	type versionResp struct {
		Version string `json:"version"`
	}
	body, _ := json.Marshal(versionResp{Version: version})
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, contentTypeJSON)
		_, _ = w.Write(body)
	}
}
