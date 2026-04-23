// Package sessions provides HTTP handlers for session-scoped operations.
package sessions

import (
	"net/http"

	"irrlicht/core/ports/outbound"
)

// FocusTarget is the interface the focus handler calls into. Satisfied by
// *services.FocusService without importing the services package.
type FocusTarget interface {
	RequestFocus(sessionID string) error
}

// NewFocusHandler returns an http.HandlerFunc that accepts
// POST /api/v1/sessions/{id}/focus and requests the given session's host
// terminal/IDE window to come to the foreground.
//
// Responses:
//   - 200: focus request broadcast (Swift app will activate the window)
//   - 400: malformed request (no session ID)
//   - 404: session not found or has no launcher information
//   - 405: method not allowed
func NewFocusHandler(target FocusTarget, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := r.PathValue("id")
		if sessionID == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}
		if err := target.RequestFocus(sessionID); err != nil {
			log.LogError("focus", sessionID, err.Error())
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
