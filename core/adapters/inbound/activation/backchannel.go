package activation

import (
	"encoding/json"
	"net/http"

	"irrlicht/core/adapters/outbound/httputil"
	"irrlicht/core/ports/outbound"
)

// Toggle is the slice of a persisted boolean store the handler needs: read and
// set a default-OFF master toggle. Satisfied by *filesystem.BackchannelStore
// and *filesystem.RelayControlStore.
type Toggle interface {
	Enabled() bool
	SetEnabled(enabled bool) error
}

// NewToggleHandler returns a GET/POST/DELETE handler over a default-OFF toggle,
// replying {"<field>": bool}. One resource, three verbs (mirroring task-eta):
//
//	GET    → current state
//	POST   → enable
//	DELETE → disable
//
// Must be registered behind localhostOnly. The mutating verbs additionally
// reject cross-origin browser requests (Sec-Fetch-Site) so a webpage the user
// visits cannot flip control on via a safelisted POST to loopback.
func NewToggleHandler(toggle Toggle, field string, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// fall through to the state reply
		case http.MethodPost, http.MethodDelete:
			if httputil.IsCrossOriginBrowserRequest(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if err := toggle.SetEnabled(r.Method == http.MethodPost); err != nil {
				log.LogError("activation", "", err.Error())
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{field: toggle.Enabled()})
	}
}

// NewBackchannelHandler is the backchannel master-toggle at
// /api/v1/activation/backchannel (issue #724).
func NewBackchannelHandler(toggle Toggle, log outbound.Logger) http.HandlerFunc {
	return NewToggleHandler(toggle, "backchannel_enabled", log)
}
