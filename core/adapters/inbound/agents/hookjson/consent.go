// consent.go holds the receiver-side consent types the JSON-hook adapters
// share. Their HookTarget interfaces and payload structs deliberately stay
// per-adapter — those differ in shape and in intent (Codex, for instance, does
// not decode session_id at all, because a Codex session shares its id with
// every child) and merging them would couple the adapters to each other's
// event sets rather than to a common contract.
package hookjson

import "slices"

// ConsentGranter reports whether the user has granted a permission (issue
// #570). Satisfied by *services.PermissionService. Hooks installed by a
// pre-consent daemon keep firing until the prompt is answered, so a hook
// receiver drops payloads while its permission is pending or denied.
type ConsentGranter interface {
	Granted(agentName, key string) bool
}

// Consent is the set of permissions ONE hook receiver acts under, bound to the
// granter that answers for them and to the adapter they belong to.
//
// # Why this type exists at all
//
// #1361 made a receiver confine caller-supplied paths, #1389 welded the
// confinement to the decode so it could not be skipped, and #1390 published the
// confiner on the handler so the contract graded the object the daemon builds.
// Consent is the same three moves for the OTHER thing a receiver owes: the #570
// permission its work happens under.
//
// #1475 made contracttesting.AssertPermissionGated key-aware, so "gated on the
// RIGHT permission" became assertable. It did not make it unforgettable: a
// receiver must be wired once per permission it honours, and nothing failed if
// an author wired only one arm — copilot's receiver reached #1475 with no
// contract wiring at all, correct by its author's care rather than by anything
// that would have caught its absence (issue #1488).
//
// Naming the keys HERE removes both acts of memory at once, because the set
// becomes a property of the receiver rather than of the test that grades it:
//
//   - NewHandler takes a Consent, so a receiver that names no permission does
//     not compile. That is a type guarantee and needs no tripwire policing it,
//     the same trade #1390 made for the confiner and #1479 for armT.fixtures.
//   - HookHandler publishes it, so a contract wiring DERIVES the keys instead
//     of listing them. A receiver that later honours a third permission grows a
//     third contract arm by existing, not by being remembered.
//   - DecodeConfined refuses to decode while any DECLARED key is ungranted, so
//     a receiver that declares a permission and then forgets to check it drops
//     the payload rather than dispatching. That is the #1466 shape, and it is
//     the one direction a declaration alone would leave open.
//
// # What it deliberately does NOT do
//
// It does not run the checks for the receiver, and it does not know their
// ORDER. That order is the contract (see claudecode's admitHookRequest) and it
// is load-bearing in two directions at once: the channel key has to be checked
// BEFORE ObserveHookReceipt so a consent-denied request counts no receipt,
// and the transcript key AFTER it so a hooks-granted / transcripts-denied
// install is not falsely reported dead by #1368's liveness watchdog. Folding
// the sequence in here would put those two obligations — plus the receipt, plus
// the response code — in a function whose callers cannot see them, which is the
// shape #1364's IgnoreUnknownEvent avoids by taking no ResponseWriter. The
// receiver keeps writing the sequence; this type only makes the SET
// unforgettable and re-checks it at the chokepoint.
//
// # Zero value, and why it is a value type rather than a pointer
//
// The zero Consent declares nothing and grants nothing: DecodeConfined fails
// closed on it, loudly, and consent_test.go commits that mutation. It is a
// value rather than a pointer because it has no mutable state to share — unlike
// PathConfiner, whose per-instance rejection counters are the whole reason the
// handler must publish the same instance the request path uses. A value also
// leaves exactly ONE degenerate spelling to guard (Consent{}) instead of two
// (nil and the zero struct).
type Consent struct {
	granter ConsentGranter
	agent   string
	keys    []string
}

// RequireConsent declares the permissions a receiver acts under.
//
// key is positional and more is variadic, so "at least one permission" is
// enforced by the signature rather than by a check: RequireConsent(g, name) is
// a compile error, and there is no spelling of a keyless receiver that builds.
//
// A nil granter answers granted for every DECLARED key. That is the existing
// test-only ungated shape every receiver constructor documents (`a nil gate
// means no gating`), preserved verbatim rather than changed here — production
// wiring in registerHookRoutes always passes the PermissionService. Note what
// it is not: it is not a way to reach an UNDECLARED key, which stays false
// however the granter answers.
func RequireConsent(granter ConsentGranter, agentName, key string, more ...string) Consent {
	keys := make([]string, 0, 1+len(more))
	keys = append(keys, key)
	keys = append(keys, more...)
	return Consent{granter: granter, agent: agentName, keys: keys}
}

// Keys returns the permissions this receiver declared, in declaration order.
//
// This is what a contract wiring reads instead of restating the list: the set
// under test is then taken off the receiver the daemon builds, so it cannot
// drift from what production actually honours (issue #1488). The copy is
// deliberate — a caller that sorted or truncated the returned slice would edit
// the receiver's own declaration.
func (c Consent) Keys() []string {
	return slices.Clone(c.keys)
}

// Granted reports whether one DECLARED permission is currently granted.
//
// An undeclared key is false, always, and that is the load-bearing half. A
// receiver that checks a permission it never declared would otherwise gate
// itself on something the published set does not mention — so the contract
// would derive one set while the request path consulted another, which is the
// two-objects-that-disagree failure #1390 removed for the confiner. Answering
// false makes such a receiver drop everything, which is loud on its first test
// rather than silent for its whole life.
func (c Consent) Granted(key string) bool {
	if !c.declares(key) {
		return false
	}
	if c.granter == nil {
		return true
	}
	return c.granter.Granted(c.agent, key)
}

// declares reports whether key is in the declared set.
func (c Consent) declares(key string) bool {
	return slices.Contains(c.keys, key)
}

// ungranted returns the first declared permission that is not currently
// granted, and whether there was one. DecodeConfined uses it as the backstop:
// see this file's header for why the receiver still runs its own checks in its
// own order, and decode.go for what the backstop catches that they cannot.
func (c Consent) ungranted() (string, bool) {
	for _, k := range c.keys {
		if !c.Granted(k) {
			return k, true
		}
	}
	return "", false
}
