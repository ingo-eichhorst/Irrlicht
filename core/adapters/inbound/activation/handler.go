// Package activation provides the HTTP handler for irrlicht-managed
// instruction-file activation (issue #558). One resource, three verbs:
//
//	GET    /api/v1/activation/task-eta  → current consent state
//	POST   /api/v1/activation/task-eta  → consent + install the managed block
//	DELETE /api/v1/activation/task-eta  → revoke + remove the managed block
//
// The route must be registered behind localhostOnly — it rewrites a
// sensitive user file (~/.claude/CLAUDE.md) and must not be reachable from
// the LAN when the daemon binds 0.0.0.0.
package activation

import (
	"encoding/json"
	"net/http"

	services "irrlicht/core/application/services"
	"irrlicht/core/ports/outbound"
)

// activationTarget is the interface the handler calls into. Satisfied by
// *services.ActivationService without importing it at construction sites.
type activationTarget interface {
	Status() services.ActivationState
	Enable() (services.ActivationState, error)
	Disable() (services.ActivationState, error)
}

// NewHandler returns the task-eta activation handler.
func NewHandler(target activationTarget, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			state services.ActivationState
			err   error
		)
		switch r.Method {
		case http.MethodGet:
			state = target.Status()
		case http.MethodPost, http.MethodDelete:
			// localhostOnly is not enough for the mutating verbs: a
			// safelisted cross-origin POST from any webpage the user visits
			// reaches loopback without a CORS preflight and would rewrite
			// ~/.claude/CLAUDE.md. Browsers stamp Sec-Fetch-Site; reject the
			// cross-origin values. Non-browser clients (the macOS URLSession
			// client, curl) omit the header → allowed.
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if r.Method == http.MethodPost {
				state, err = target.Enable()
			} else {
				state, err = target.Disable()
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			log.LogError("activation", "", err.Error())
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}
}
