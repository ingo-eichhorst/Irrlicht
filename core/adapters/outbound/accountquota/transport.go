// Package accountquota is the adapter half of issue #2003's account-quota
// boundary: the port (core/ports/outbound/accountquota.go) says what a
// provider ticket may ask for; this package is the only place that builds a
// real net/http request, reads a real filesystem credential, or shells out to
// the OS keychain to do it.
//
// core/pkg/capacity/remote.go is the repository's PRE-EXISTING outbound-HTTP
// adapter — read before touching this one. It is a public, unauthenticated,
// unbounded GET against one hardcoded URL with a 30s client timeout and a
// plain io.ReadAll; sharing code with it was considered and rejected, because
// every property this package adds (a destination allowlist, an explicit
// redirect refusal, a bounded read, a credential that must never reach an
// error) is a response to carrying a per-account SECRET, which remote.go's
// fetch never does. Widening remote.go's fetchAndCache to take these
// parameters would make its own callers (the LiteLLM pricing refresh) pay for
// guards their request shape can never trigger.
package accountquota

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	outbound "irrlicht/core/ports/outbound"
)

const (
	// requestTimeout bounds one Fetch end to end. Not a measured probe cost
	// (docs/testing-contracts.md's "Probe costs in doc comments" convention) —
	// a deliberate ceiling generous enough for a slow account-quota endpoint
	// on a loaded network while still failing well before a session-facing
	// caller would consider the daemon hung.
	requestTimeout = 10 * time.Second

	// responseSizeCeiling bounds one Fetch's response body. 1 MiB matches
	// hookjson's own inbound bound (docs/testing-contracts.md's readBody) —
	// chosen for the same reason: these are small JSON documents, and a
	// ceiling this size is never reached by a legitimate account-quota
	// response but stops an adversarial or misbehaving endpoint from handing
	// an unbounded body to a long-lived daemon.
	responseSizeCeiling = 1 << 20 // 1 MiB
)

// errRedirectRefused is returned by refuseAllRedirects and detected in Fetch
// to classify a redirected response as QuotaFailureRedirectRefused rather
// than a generic network error. It carries no request/response state — only
// its identity matters.
var errRedirectRefused = errors.New("account-quota transport refuses every redirect")

// refuseAllRedirects is this transport's entire redirect policy (issue #2003
// §1.2's "explicit redirect policy"): every redirect is refused, regardless
// of whether it happens to stay on the same host. Refuse-all is simpler than
// a same-host check and strictly stronger — url.URL.Host includes the port,
// so two distinct loopback test servers are already "cross-host" by that
// measure, and a same-host comparison buys nothing a fixed, single-URL
// destination doesn't already guarantee.
func refuseAllRedirects(_ *http.Request, _ []*http.Request) error {
	return errRedirectRefused
}

// HTTPTransport is the reviewed outbound.AccountQuotaTransport implementation.
// Construct one per provider (or share one across providers that need no
// per-provider client settings) with the fixed destination set that
// provider's ticket reviewed.
type HTTPTransport struct {
	destinations map[string]*url.URL
	client       *http.Client
}

// NewHTTPTransport builds a transport restricted to destinations. Every URL
// must be a fully-qualified https URL; a plain http URL would carry a
// credential header in cleartext and is refused rather than silently allowed
// for a "local test" convenience — tests use an httptest.Server, whose https
// TLS test server (httptest.NewTLSServer) this package's own tests use for
// exactly that reason.
func NewHTTPTransport(destinations []outbound.FixedDestination) (*HTTPTransport, error) {
	return newHTTPTransport(destinations, http.DefaultTransport)
}

// newHTTPTransport is NewHTTPTransport with an injectable RoundTripper — the
// seam this package's own tests use to add a loopback-only dial guard (never
// to weaken the redirect/timeout policy, which stays fixed here) without
// exporting a test-only constructor.
func newHTTPTransport(destinations []outbound.FixedDestination, rt http.RoundTripper) (*HTTPTransport, error) {
	parsed, err := parseDestinations(destinations)
	if err != nil {
		return nil, err
	}
	return &HTTPTransport{
		destinations: parsed,
		client: &http.Client{
			Timeout:       requestTimeout,
			Transport:     rt,
			CheckRedirect: refuseAllRedirects,
		},
	}, nil
}

// parseDestinations validates and parses a reviewed destination set. Split
// out of newHTTPTransport so this package's own tests can build an
// *http.Client with settings newHTTPTransport does not expose (a short
// timeout, to provoke QuotaFailureTimeout without a real 10s wait) while
// still going through the same validation every production destination set
// does.
func parseDestinations(destinations []outbound.FixedDestination) (map[string]*url.URL, error) {
	if len(destinations) == 0 {
		return nil, fmt.Errorf("accountquota: at least one destination is required")
	}
	parsed := make(map[string]*url.URL, len(destinations))
	for _, d := range destinations {
		if d.Key == "" {
			return nil, fmt.Errorf("accountquota: destination has an empty Key")
		}
		if _, exists := parsed[d.Key]; exists {
			return nil, fmt.Errorf("accountquota: duplicate destination key %q", d.Key)
		}
		u, err := url.Parse(d.URL)
		if err != nil {
			return nil, fmt.Errorf("accountquota: destination %q: %w", d.Key, err)
		}
		if u.Scheme != "https" {
			return nil, fmt.Errorf("accountquota: destination %q must be https, got %q", d.Key, u.Scheme)
		}
		if u.Host == "" {
			return nil, fmt.Errorf("accountquota: destination %q has no host", d.Key)
		}
		parsed[d.Key] = u
	}
	return parsed, nil
}

// Fetch implements outbound.AccountQuotaTransport.
func (t *HTTPTransport) Fetch(ctx context.Context, req outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
	dest, ok := t.destinations[req.DestinationKey]
	if !ok {
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{
			Reason: outbound.QuotaFailureUnknownDestination,
			Detail: fmt.Sprintf("no reviewed destination named %q", req.DestinationKey),
		}
	}
	if req.Credential.IsZero() {
		// QuotaFailureCredential, not QuotaFailureNetwork (found by review,
		// #2003): an empty credential is a resolver problem the caller must
		// fix, not a transport condition worth the poller's retry backoff.
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{
			Reason: outbound.QuotaFailureCredential,
			Detail: "empty credential — refusing to send an unauthenticated request",
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dest.String(), nil)
	if err != nil {
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureNetwork, Detail: "building request"}
	}
	header := req.Auth.Header
	if header == "" {
		header = "Authorization"
	}
	// The ONE call site: Reveal() is invoked here and nowhere else in this
	// package (TestCredential_RevealHasOneCallSite in this package's own
	// tests greps the package source for it, so a second call site fails
	// that test rather than being trusted on this comment alone).
	httpReq.Header.Set(header, req.Auth.Prefix+req.Credential.Reveal())

	resp, err := t.client.Do(httpReq)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && errors.Is(urlErr.Err, errRedirectRefused) {
			return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureRedirectRefused, Detail: "destination answered a redirect"}
		}
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &urlErr) && urlErr.Timeout()) {
			return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureTimeout, Detail: "request did not complete in time"}
		}
		// Deliberately not %v/%w on err below the QuotaError boundary: a
		// net/url.Error's Error() string embeds the request URL (which is
		// fine — that's OUR destination, not attacker-controlled) but some
		// transport errors can embed proxy/auth text. Only a fixed, safe
		// description crosses out of this function.
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureNetwork, Detail: "request failed"}
	}
	defer resp.Body.Close()

	// Status is classified BEFORE the body is ever read (found by review,
	// #2003): the status code is already known from the response headers,
	// and a non-2xx response's body carries nothing this transport needs.
	// Reading it first meant an auth-rejected response with an oversized or
	// mid-stream-broken body was misclassified as response_too_large or
	// network instead of auth_rejected — losing exactly the signal
	// QuotaFailureAuthRejected's doc comment says the poller must act on
	// specially. The body is still drained (bounded, errors ignored) purely
	// for HTTP connection-reuse hygiene, never inspected.
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		drainIgnoringErrors(resp.Body)
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{
			Reason: outbound.QuotaFailureAuthRejected,
			Detail: fmt.Sprintf("status %d", resp.StatusCode),
		}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		drainIgnoringErrors(resp.Body)
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{
			Reason: outbound.QuotaFailureHTTPStatus,
			Detail: fmt.Sprintf("status %d", resp.StatusCode),
		}
	}

	body, sizeErr := readBounded(resp.Body)
	if sizeErr != nil {
		return outbound.AccountQuotaResponse{}, sizeErr
	}

	return outbound.AccountQuotaResponse{StatusCode: resp.StatusCode, Body: body}, nil
}

// drainIgnoringErrors reads (and discards) up to responseSizeCeiling bytes of
// a non-2xx response body purely so the underlying connection can be reused
// by net/http's transport — never to inspect the content, and never allowed
// to affect the classification already decided by the status code. Errors
// are deliberately ignored: a read failure here says nothing about the
// (already known) auth/status outcome.
func drainIgnoringErrors(r io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, responseSizeCeiling))
}

// readBounded reads r up to responseSizeCeiling+1 bytes and reports
// QuotaFailureResponseTooLarge if more than responseSizeCeiling bytes were
// present. The +1/io.LimitReader pairing is load-bearing, not decorative:
// bounding the READ (not just checking len(data) after an unbounded read) is
// what stops an adversarial endpoint from making this daemon buffer an
// arbitrarily large body before the ceiling is ever consulted — the +1 exists
// only so a body of EXACTLY the ceiling size (a legitimate response) is not
// mistaken for an oversized one truncated at the boundary.
func readBounded(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, responseSizeCeiling+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, &outbound.QuotaError{Reason: outbound.QuotaFailureNetwork, Detail: "reading response body"}
	}
	if len(data) > responseSizeCeiling {
		return nil, &outbound.QuotaError{Reason: outbound.QuotaFailureResponseTooLarge, Detail: "response exceeded the size ceiling"}
	}
	return data, nil
}
