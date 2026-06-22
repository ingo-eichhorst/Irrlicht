// Package backchannel provides the HTTP handler for the event→action rule set
// (issue #724). One resource, two verbs:
//
//	GET /api/v1/backchannel/rules  → current rules
//	PUT /api/v1/backchannel/rules  → replace the rule set
//
// Must be registered behind localhostOnly; the mutating verb additionally
// rejects cross-origin browser requests (Sec-Fetch-Site), since rules drive a
// live agent.
package backchannel

import (
	"encoding/json"
	"net/http"

	"irrlicht/core/domain/backchannel"
	"irrlicht/core/ports/outbound"
)

// rulesStore is the slice of *filesystem.BackchannelRulesStore the handler
// needs.
type rulesStore interface {
	Rules() []backchannel.Rule
	SetRules(rules []backchannel.Rule) error
}

type rulesBody struct {
	Rules []backchannel.Rule `json:"rules"`
}

// NewRulesHandler returns the GET/PUT handler for the backchannel rule set.
func NewRulesHandler(store rulesStore, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// fall through to the reply
		case http.MethodPut:
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			var body rulesBody
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if err := store.SetRules(body.Rules); err != nil {
				log.LogError("backchannel", "", err.Error())
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rulesBody{Rules: store.Rules()})
	}
}
