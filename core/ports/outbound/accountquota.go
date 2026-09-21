package outbound

import "context"

// accountquota.go declares the reviewed boundary issue #2003 exists to draw:
// one way to hold a provider credential, one way to reach a named account
// endpoint, and one way to classify what came back. It adds no provider — a
// later provider ticket (epic #1977's work package B, #2007 for Muse) supplies
// a CredentialResolver implementation, a FixedDestination and a way to
// interpret AccountQuotaResponse.Body. See core/pkg/capacity/remote.go for the
// repository's PRE-EXISTING outbound-HTTP shape (an unauthenticated public
// fetch with no credential and no destination allowlist) — this port is
// deliberately a second, stricter shape rather than a widening of that one,
// because every property below (fixed destinations, bounded read, redacted
// credential) is meaningless for a fetch that already has none of the
// authority a per-account credential carries.
//
// Deliberately free of net/http: a port describes WHAT a provider ticket may
// ask for, not HOW the request is built. Only the adapter package
// (core/adapters/outbound/accountquota) ever imports net/http, and only its
// transport ever calls Credential.Reveal() — see Credential's doc comment for
// why that single call site is load-bearing rather than a convention.

// Credential is an opaque secret value — a resolved API key, token, or
// session cookie. Its zero value is not a valid credential (Reveal returns
// "").
//
// String/GoString are overridden so that %v/%s/%+v on a Credential (or on a
// struct that embeds/holds one by value in an EXPORTED field) print a fixed
// redaction marker rather than the secret. This is a deliberate, narrow claim,
// not a general "never leaks" guarantee: Go's fmt package does not call
// String()/GoString() on a value reached through an UNEXPORTED struct field
// (reflect.Value.CanInterface() is false there), so a type that copies a
// Credential's revealed string into a plain `string` field — exported or not —
// defeats this entirely. The one thing that makes the claim hold in practice
// is that Reveal() has exactly one caller — the transport in
// core/adapters/outbound/accountquota, immediately before setting a request
// header. TestCredential_RevealHasOneCallSite (in that adapter package)
// checks this by grepping that package's own non-test sources, which is
// package-scoped, NOT codebase-wide (found by review, #2003): a future
// provider package gaining its own second Reveal() call site (for logging,
// say) would violate this comment's claim with nothing here to catch it.
// Every provider ticket is expected to route through this transport rather
// than building its own — see accountquota's package doc — so the intended
// invariant IS codebase-wide; only the enforcement is narrower than that
// today.
type Credential struct {
	secret string
}

// NewCredential wraps a resolved secret. Callers should hold the result, not
// the raw string, for as long as possible before the one Reveal() call that
// attaches it to a request.
func NewCredential(secret string) Credential { return Credential{secret: secret} }

// Reveal returns the raw secret. Callers outside the transport that calls
// this immediately before injecting it into a request header are a leak by
// definition, not by exception.
func (c Credential) Reveal() string { return c.secret }

// IsZero reports whether c carries no secret at all — the check a caller
// needing to refuse an unauthenticated request makes INSTEAD of comparing
// Reveal() to "", so that guard is not itself a second call site for the one
// grep in the adapter package counts (core/adapters/outbound/accountquota's
// TestCredential_RevealHasOneCallSite).
func (c Credential) IsZero() bool { return c.secret == "" }

// String implements fmt.Stringer so an accidental %s/%v never prints the
// secret.
func (c Credential) String() string { return "[redacted credential]" }

// GoString implements fmt.GoStringer so an accidental %#v (a common debug
// habit) never prints the secret either.
func (c Credential) GoString() string { return "[redacted credential]" }

// CredentialResolver resolves a provider's account credential from wherever
// it is stored. #2003 ships two locations — a file
// (core/adapters/outbound/accountquota.FileCredentialResolver) and the OS
// keychain (KeychainCredentialResolver, darwin-only; classified-unsupported
// elsewhere). A browser-profile resolver is the separate prov(browser)
// permission's concern (epic #1977), not this port's.
type CredentialResolver interface {
	Resolve(ctx context.Context) (Credential, error)
}

// AuthMethod describes how a resolved Credential is attached to an outbound
// request, kept as data rather than a func(*http.Request, ...) callback so
// this port never needs to import net/http. Every provider client surveyed
// for epic #1977 needs only a named header carrying the secret, optionally
// prefixed (a bearer scheme) — a future provider needing a materially
// different shape (a query parameter, a signed request) amends this type in a
// reviewable diff rather than smuggling a request-mutating closure across the
// port boundary.
type AuthMethod struct {
	// Header is the request header the credential is attached to, e.g.
	// "Authorization" or "X-Api-Key".
	Header string
	// Prefix is prepended to the revealed secret before it is set as the
	// header value, e.g. "Bearer " — "" for a raw header value.
	Prefix string
}

// FixedDestination names one reviewed outbound endpoint. A transport is
// constructed with the full set a provider is allowed to reach; Fetch refuses
// any key outside that set. This is what makes "a URL discovered in a session
// is never a destination" (issue #2003 §1.2) true by construction rather than
// by convention: the only URLs a transport instance can ever dial are the
// ones its constructor was given, at daemon-wiring time, by code a reviewer
// read.
type FixedDestination struct {
	// Key names this destination for AccountQuotaRequest.DestinationKey.
	Key string
	// URL is the fully-qualified https URL reviewed at code-review time.
	URL string
}

// AccountQuotaRequest is one poll attempt against a single fixed destination.
type AccountQuotaRequest struct {
	// DestinationKey must match a FixedDestination.Key the transport was
	// constructed with. Any other value is refused before a credential is
	// even read.
	DestinationKey string
	// Credential is attached to the request per Auth. The zero Credential
	// (Reveal() == "") is a caller error, not an unauthenticated request —
	// this port has no notion of an anonymous account-quota fetch.
	Credential Credential
	Auth       AuthMethod

	// SessionID is the CALLER's session identifier, carried for attribution
	// and logging only. It is NOT part of the poll's identity — the
	// daemon-wide poller (core/application/services.AccountPoller) dedupes
	// and caches by confirmed account, never by session, so this field must
	// never participate in a cache key (issue #2003 §1.4: "more sessions ...
	// must not multiply account API calls").
	SessionID string
}

// AccountQuotaResponse is a successful (2xx) fetch: the status code and the
// response body, already bounded by the transport's size ceiling.
type AccountQuotaResponse struct {
	StatusCode int
	Body       []byte
}

// QuotaFailureReason classifies why a fetch did not produce a usable
// AccountQuotaResponse. Every value here is safe to log and safe to publish
// to a client — none of them is, or is derived from, a response body or a
// credential.
type QuotaFailureReason string

const (
	// QuotaFailureUnknownDestination means the caller named a DestinationKey
	// the transport was not constructed with. No request was sent.
	QuotaFailureUnknownDestination QuotaFailureReason = "unknown_destination"
	// QuotaFailureRedirectRefused means the destination answered a redirect;
	// this transport's policy refuses every redirect rather than evaluate
	// whether one happens to stay same-host (issue #2003 §1.2's "explicit
	// redirect policy").
	QuotaFailureRedirectRefused QuotaFailureReason = "redirect_refused"
	// QuotaFailureResponseTooLarge means the body exceeded the transport's
	// read ceiling. The read itself stopped at the ceiling — it did not
	// buffer the whole body first and reject it after (see the adapter's
	// readBounded).
	QuotaFailureResponseTooLarge QuotaFailureReason = "response_too_large"
	// QuotaFailureTimeout means the request did not complete within the
	// transport's fixed timeout.
	QuotaFailureTimeout QuotaFailureReason = "timeout"
	// QuotaFailureAuthRejected means the destination answered 401 or 403 —
	// the credential exists but was rejected. The poller must never
	// translate this into a manufactured zero-usage observation (issue
	// #2003 §1.4).
	QuotaFailureAuthRejected QuotaFailureReason = "auth_rejected"
	// QuotaFailureHTTPStatus means any other non-2xx status.
	QuotaFailureHTTPStatus QuotaFailureReason = "http_status"
	// QuotaFailureNetwork means the request could not complete for a
	// transport-level reason not covered above (DNS, connection refused,
	// TLS, a cancelled context that isn't the timeout above).
	QuotaFailureNetwork QuotaFailureReason = "network"
	// QuotaFailureCredential means the credential itself could not be used —
	// CredentialResolver.Resolve failed (a missing/deleted file, an absent
	// keychain item, an unconfigured resolver), or it resolved to an empty
	// secret. Distinct from QuotaFailureNetwork (found by review, #2003):
	// this is a permanent misconfiguration a user needs to act on — "your
	// credential is missing", not "the network is down" — and a poller
	// should not apply the same retry-and-wait treatment to both.
	QuotaFailureCredential QuotaFailureReason = "credential"
	// QuotaFailureInternal means the fetch could not be completed due to a
	// fault in irrlicht's own code path (a panic recovered from a
	// provider-supplied CredentialResolver or AccountQuotaTransport
	// implementation), not a network or credential condition. Found by
	// review, #2003: core/application/services.AccountPoller's runDoFetch
	// recovers such a panic so it cannot leave an in-flight fetch's
	// followers permanently blocked, and classifies it distinctly so it is
	// never confused with an ordinary transient failure.
	QuotaFailureInternal QuotaFailureReason = "internal"
)

// AccountQuotaTransport is the single reviewed way irrlicht reaches a
// provider's account-quota endpoint over the network (issue #2003 §1.2).
// Implementations must never return an error whose text contains the
// response body or the credential.
type AccountQuotaTransport interface {
	Fetch(ctx context.Context, req AccountQuotaRequest) (AccountQuotaResponse, error)
}

// QuotaError is the error type every AccountQuotaTransport.Fetch failure is
// returned as. Detail is a short, safe, human-readable elaboration — never
// the response body and never a credential.
type QuotaError struct {
	Reason QuotaFailureReason
	Detail string
}

func (e *QuotaError) Error() string {
	if e.Detail == "" {
		return string(e.Reason)
	}
	return string(e.Reason) + ": " + e.Detail
}
