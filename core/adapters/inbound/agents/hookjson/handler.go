// handler.go holds the shape every hook-receiving adapter's constructor
// returns: the handler itself, plus the confiner it guards caller-supplied
// transcript paths with.
//
// The confiner is part of what a receiver IS, not a detail of how one is built
// (issue #1390). Before this type each adapter exported a second, test-shaped
// constructor — NewHookHandlerWithConfiner and friends — for the single reason
// that the #1361 contract has to read RejectionCount() off the instance the
// handler actually closed over, and a closure hides it. Three receivers carried
// that seam, and the contract then grew a sixth obligation whose whole job was
// policing the API the same PR had introduced: proving that the DEFAULT
// constructor confines too, since the test had been exercising the other one.
//
// Returning the confiner alongside the handler dissolves that. There is one
// exported constructor per receiver, and the counter the contract reads is
// obtained FROM the handler under test — so "this handler confines" and "the
// counter I am reading belongs to this handler" stop being two facts that could
// disagree. That is what retired obligation 6, rather than the split merely
// being gone.
//
// Note the sibling counters in this package need nothing like it:
// IgnoreUnknownEvent (#1364) and ObserveHookReceipt (#1368) keep package-level
// counts, so their contracts read package accessors and hold no handle on a
// handler. PathConfiner is per-instance because its roots are per-adapter, so
// it is the one that needs a way out. That asymmetry is the reason this type
// carries exactly one field and is not a bag of receiver internals.
package hookjson

import "net/http"

// HookHandler is a hook receiver: an http.Handler (and, via the embedded
// HandlerFunc, an ordinary func) together with the PathConfiner it enforces
// issue #1361 confinement with.
//
// The embedding is what keeps this a drop-in for the bare http.HandlerFunc the
// constructors used to return — ServeHTTP is promoted, so a HookHandler
// satisfies http.Handler and goes straight into mux.Handle, and h.HandlerFunc
// is still callable directly where a raw func is genuinely wanted.
type HookHandler struct {
	http.HandlerFunc

	// Confiner is the confiner this handler guards transcript_path with — the
	// same instance the request path uses, never a second one built alongside
	// it. Its counters are the only evidence a refusal happened at all
	// (RejectPath logs and counts; the response is an ordinary 2xx), which is
	// why the contract reads them rather than trusting the status code.
	Confiner *PathConfiner
}
