// Package activation provides the HTTP handler for irrlicht-managed
// instruction-file activation (issue #558). One resource, three verbs:
//
//	GET    /api/v1/activation/task-eta  → current consent state
//	POST   /api/v1/activation/task-eta  → consent + install the managed block
//	DELETE /api/v1/activation/task-eta  → revoke + remove the managed block
//
// Since issue #577 this endpoint is a thin alias over the permission
// wizard's claude-code/instructions permission — the PermissionService owns
// the consent state and the install/uninstall effects; this route only
// keeps the macOS Settings toggle's wire shape stable.
//
// The route must be registered behind localhostOnly — it rewrites a
// sensitive user file (~/.claude/CLAUDE.md) and must not be reachable from
// the LAN when the daemon binds 0.0.0.0.
package activation

import (
	"encoding/json"
	"net/http"

	"irrlicht/core/adapters/outbound/httputil"
	services "irrlicht/core/application/services"
	"irrlicht/core/ports/outbound"
)

// consentTarget is the slice of *services.PermissionService the handler
// needs: reading and answering one permission.
type consentTarget interface {
	Granted(agentName, key string) bool
	Answer(answers []services.PermissionAnswer) error
}

// state is the response body — the legacy activation wire shape the macOS
// Settings toggle decodes.
type state struct {
	TaskEtaEnabled bool `json:"task_eta_enabled"`
}

// NewHandler returns the task-eta activation handler aliasing the
// agentName/permKey permission.
func NewHandler(target consentTarget, agentName, permKey string, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// fall through to the state reply
		case http.MethodPost, http.MethodDelete:
			// localhostOnly is not enough for the mutating verbs: a safelisted
			// cross-origin POST from a webpage the user visits reaches loopback
			// without a CORS preflight and would rewrite ~/.claude/CLAUDE.md.
			if httputil.IsCrossOriginBrowserRequest(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			answer := services.PermissionAnswer{
				Agent:      agentName,
				Permission: permKey,
				Grant:      r.Method == http.MethodPost,
			}
			if err := target.Answer([]services.PermissionAnswer{answer}); err != nil {
				log.LogError("activation", "", err.Error())
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state{TaskEtaEnabled: target.Granted(agentName, permKey)})
	}
}
