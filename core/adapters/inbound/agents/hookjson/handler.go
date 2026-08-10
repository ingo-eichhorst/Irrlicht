// handler.go holds the shape every hook-receiving adapter's constructor
// returns: the handler itself, plus the confiner it guards caller-supplied
// transcript paths with. The confiner is part of what a receiver IS, not a
// detail of how one is built (issue #1390).
//
// Why there is no longer a NewHookHandlerWithConfiner, and why the #1361
// contract lost an obligation along with it: see AGENTS.md, "Hook path
// confinement".
//
// The sibling counters in this package need nothing like this type:
// IgnoreUnknownEvent (#1364) and ObserveHookReceipt (#1368) keep package-level
// counts, so their contracts read package accessors and hold no handle on a
// handler. PathConfiner is per-instance because its roots are per-adapter, so
// it is the one that needs a way out — which is why HookHandler carries exactly
// one field and is not a bag of receiver internals.
package hookjson

import "net/http"

// HookHandler is a hook receiver: an http.Handler (and, via the embedded
// HandlerFunc, an ordinary func) together with the PathConfiner it enforces
// issue #1361 confinement with. Build one with NewHandler.
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
}

// NewHandler builds a receiver that serves through serve, handing it confiner
// on every request and publishing that same instance as Confiner.
//
// Constructing the pair here rather than at each adapter is the point: the
// property #1390 buys is that Confiner IS the confiner the request path uses,
// and three hand-written struct literals asserted that by convention. serve
// takes the confiner as a parameter rather than capturing one, so the correct
// wiring is also the shortest one and a fourth receiver inherits it.
func NewHandler(confiner *PathConfiner, serve func(*PathConfiner, http.ResponseWriter, *http.Request)) HookHandler {
	return HookHandler{
		HandlerFunc: func(w http.ResponseWriter, r *http.Request) { serve(confiner, w, r) },
		Confiner:    confiner,
	}
}
