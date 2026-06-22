// Package permissions provides the HTTP handlers for the consent-first
// permission wizard (issue #570): GET /api/v1/permissions serves every
// agent's declared permissions with their consent state, and
// POST /api/v1/permissions/answer applies the user's decisions.
package permissions

import (
	"encoding/json"
	"errors"
	"net/http"

	"irrlicht/core/adapters/outbound/httputil"
	services "irrlicht/core/application/services"
	"irrlicht/core/ports/outbound"
)

// target is the interface both handlers call into. Satisfied by
// *services.PermissionService.
type target interface {
	Snapshot() services.PermissionsSnapshot
	Answer(answers []services.PermissionAnswer) error
}

// answerRequest is the POST /api/v1/permissions/answer body.
type answerRequest struct {
	Answers []services.PermissionAnswer `json:"answers"`
}

// NewGetHandler returns the handler for GET /api/v1/permissions.
func NewGetHandler(t target, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeSnapshot(w, t, log)
	}
}

// NewAnswerHandler returns the handler for POST /api/v1/permissions/answer.
// The first surface to answer wins: the service broadcasts
// permissions_updated so the other surface dismisses its wizard live, and
// a duplicate submission of the same answer is a no-op.
//
// Responses:
//   - 200: answers applied; body is the updated permissions snapshot
//   - 400: malformed JSON or unknown agent/permission pair
//   - 403: cross-origin browser request
func NewAnswerHandler(t target, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Granting a modify permission rewrites sensitive user files
		// (~/.claude/settings.json, ~/.claude/CLAUDE.md), so a cross-origin
		// POST from any webpage the user visits must be rejected — the JSON
		// body alone is no defense (a text/plain form can carry one). Same
		// guard as the activation alias: same-origin (the dashboard) and a
		// missing header (native clients) pass.
		if httputil.IsCrossOriginBrowserRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req answerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
			return
		}
		if err := t.Answer(req.Answers); err != nil {
			log.LogError("permissions", "", err.Error())
			if errors.Is(err, services.ErrUnknownPermission) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Return the updated snapshot so the answering surface refreshes
		// without a second round-trip.
		writeSnapshot(w, t, log)
	}
}

func writeSnapshot(w http.ResponseWriter, t target, log outbound.Logger) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(t.Snapshot()); err != nil {
		log.LogError("permissions", "", err.Error())
	}
}
