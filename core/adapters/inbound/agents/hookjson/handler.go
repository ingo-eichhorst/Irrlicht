// handler.go holds the shape every hook-receiving adapter's constructor
// returns: the handler itself, plus the two things it guards a request with —
// the confiner for caller-supplied transcript paths (issue #1390) and the
// consent it acts under (issue #1488). Both are part of what a receiver IS,
// not details of how one is built.
//
// Why there is no longer a NewHookHandlerWithConfiner, and why the #1361
// contract lost an obligation along with it: see AGENTS.md, "Hook path
// confinement".
//
// The sibling counters in this package need nothing like this type:
// IgnoreUnknownEvent (#1364) and ObserveHookReceipt (#1368) keep package-level
// counts, so their contracts read package accessors and hold no handle on a
// handler. PathConfiner is per-instance because its roots are per-adapter, and
// Consent because its key set is; those are the two that need a way out, which
// is why HookHandler carries exactly those two fields and is not a bag of
// receiver internals.
package hookjson

import "net/http"

// HookHandler is a hook receiver: an http.Handler (and, via the embedded
// HandlerFunc, an ordinary func) together with the PathConfiner it enforces
// issue #1361 confinement with and the Consent it acts under. Build one with
// NewHandler.
//
// The embedding keeps it a drop-in for a bare http.HandlerFunc — ServeHTTP is
// promoted, so a HookHandler satisfies http.Handler and goes straight into
// mux.Handle.
type HookHandler struct {
	http.HandlerFunc

	// Confiner is the confiner this handler guards transcript_path with — the
	// same instance the request path uses, never a second one built alongside
	// it. Its counters are the only evidence a refusal happened at all
	// (RejectPath logs and counts; the response is an ordinary 2xx), which is
	// why the contract reads them rather than trusting the status code.
	Confiner *PathConfiner

	// Consent is the set of permissions this handler acts under — again the
	// same value the request path consults. A contract wiring reads its Keys()
	// rather than restating them, so a receiver that honours a further
	// permission is covered by the existing wiring instead of by someone
	// remembering to add an arm (issue #1488).
	Consent Consent
}

// NewHandler builds a receiver that serves through serve, handing it consent
// and confiner on every request and publishing those same values.
//
// Constructing the triple here rather than at each adapter is the point: the
// property #1390 buys is that Confiner IS the confiner the request path uses,
// and #1488 extends it to Consent. Three hand-written struct literals asserted
// the first by convention. serve takes both as parameters rather than
// capturing them, so the correct wiring is also the shortest one and a fifth
// receiver inherits it.
//
// What this still does not stop — the same residue #1390 recorded — is a serve
// func that ignores what it is handed and builds its own. It cannot be typed
// away, and it is longer to write than the correct version.
func NewHandler(confiner *PathConfiner, consent Consent, serve func(Consent, *PathConfiner, http.ResponseWriter, *http.Request)) HookHandler {
	return HookHandler{
		HandlerFunc: func(w http.ResponseWriter, r *http.Request) { serve(consent, confiner, w, r) },
		Confiner:    confiner,
		Consent:     consent,
	}
}
