// Package mistralvibe is issue #2012's provider ticket: the Mistral Vibe
// plan's quota, read through the browser-session route (epic #1977 work
// package E). It supplies the reviewed destination, the cookie selection, the
// credential built from a Chrome profile (core/adapters/outbound/chromecookies),
// the allowlist parser that turns the response into a
// session.RateLimitSnapshot, and the permission that covers all of it.
//
// Three Mistral products exist and only one is read here. The Vibe plan's
// quota is this package. Mistral's API usage (requests and EUR on
// admin.mistral.ai) and its prepaid credits are separate products this
// package never reaches, though CodexBar's fetcher reads all three.
//
// Nothing here dials. The one transport is accountquota.HTTPTransport, built
// by the daemon's wiring in core/cmd/irrlichd, and
// TestPackage_NeverBuildsATransport checks this package's production files
// cannot build one.
package mistralvibe

import outbound "irrlicht/core/ports/outbound"

// DestinationKey names the destination for outbound.AccountQuotaRequest and
// services.AccountQuotaKey.Scope.
const DestinationKey = "mistral-vibe-usage"

// UsageURL is the tRPC query Mistral's console makes for the Vibe usage
// figure, byte for byte as CodexBar b6e65a8 sends it
// (MistralUsageFetcher.swift:67) and as issue #2012's live probe requested
// it on 2026-11-21. The input parameter is the URL-encoded
// {"0":{"json":null,"meta":{"values":["undefined"],"v":1}}}.
const UsageURL = "https://console.mistral.ai/api-ui/trpc/billing.vibeUsage?batch=1&input=%7B%220%22%3A%7B%22json%22%3Anull%2C%22meta%22%3A%7B%22values%22%3A%5B%22undefined%22%5D%2C%22v%22%3A1%7D%7D%7D"

// CSRFHeader carries the csrftoken cookie's value, as CodexBar sends it
// (MistralUsageFetcher.swift:81).
const CSRFHeader = "X-CSRFToken"

// Destination is the reviewed GET with CodexBar's Accept header. The Cookie
// and X-CSRFToken headers travel with the credential, never here.
func Destination() outbound.FixedDestination {
	return outbound.FixedDestination{
		Key:     DestinationKey,
		URL:     UsageURL,
		Method:  "GET",
		Headers: map[string]string{"Accept": "*/*"},
	}
}

// Auth attaches the credential as the raw Cookie header and its companion
// (the csrftoken value, BuildCredential) as X-CSRFToken.
func Auth() outbound.AuthMethod {
	return outbound.AuthMethod{Header: "Cookie", CompanionHeader: CSRFHeader}
}
