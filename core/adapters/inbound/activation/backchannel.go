package activation

import (
	"encoding/json"
	"net/http"

	"irrlicht/core/ports/outbound"
)

// backchannelToggle is the slice of *filesystem.BackchannelStore the handler
// needs: read and set the master toggle.
type backchannelToggle interface {
	Enabled() bool
	SetEnabled(enabled bool) error
}

// backchannelState is the response body the macOS Settings toggle decodes.
type backchannelState struct {
	BackchannelEnabled bool `json:"backchannel_enabled"`
}

// NewBackchannelHandler returns the handler for the backchannel master-toggle
// (issue #724). One resource, three verbs, mirroring the task-eta activation
// shape:
//
//	GET    /api/v1/activation/backchannel  → current state
//	POST   /api/v1/activation/backchannel  → enable
//	DELETE /api/v1/activation/backchannel  → disable
//
// Must be registered behind localhostOnly. The mutating verbs additionally
// reject cross-origin browser requests (Sec-Fetch-Site) so a webpage the user
// visits cannot flip control on via a safelisted POST to loopback.
func NewBackchannelHandler(toggle backchannelToggle, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// fall through to the state reply
		case http.MethodPost, http.MethodDelete:
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if err := toggle.SetEnabled(r.Method == http.MethodPost); err != nil {
				log.LogError("backchannel", "", err.Error())
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backchannelState{BackchannelEnabled: toggle.Enabled()})
	}
}
