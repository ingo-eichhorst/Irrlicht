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

// identityCtxKey carries the validated token identity (id + workspace) from
// requireToken to the handlers. Its own type avoids collisions with any other
// context value.
type identityCtxKey struct{}

// withIdentity attaches the token's identity to a request context.
func withIdentity(ctx context.Context, ident tokenIdentity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, ident)
}

// workspaceOf reads the workspace requireToken attached, or "" (the default
// workspace) on a no-auth relay where the gate is a pass-through.
func workspaceOf(r *http.Request) string {
	ident, _ := r.Context().Value(identityCtxKey{}).(tokenIdentity)
	return ident.workspace
}

// tokenIDOf reads the id of the token requireToken validated, or "" on a
// no-auth relay. The subscription registry keys entries by it, so a delivery
// address can only ever be written under the identity that authenticated the
// request — never under anything client-supplied.
func tokenIDOf(r *http.Request) string {
	ident, _ := r.Context().Value(identityCtxKey{}).(tokenIdentity)
	return ident.id
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
