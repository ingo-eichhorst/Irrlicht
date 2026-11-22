// Package zaiaccountapi is Z.ai's GLM Coding Plan quota integration (issue
// #2010, epic #1977 §10 work package F): the one reviewed destination, the
// allowlist parser that is also the redaction boundary, the credential read
// from the Claude Code settings file that routed a session to Z.ai, and the
// consent declaration covering both.
//
// The coding plan is a separate product from Z.ai's general API
// (/api/paas/v4, prepaid bundles). Nothing here reads that product, and no
// usage from it is ever presented as the plan's allowance.
package zaiaccountapi

import (
	"net/url"
	"strings"

	outbound "irrlicht/core/ports/outbound"
)

// DestinationKey names this destination for outbound.AccountQuotaRequest and
// for services.AccountQuotaKey.Scope.
const DestinationKey = "zai-account-api"

// Host is the only host a session's route may name for it to be bound to
// the Z.ai quota, and the host of QuotaURL.
const Host = "api.z.ai"

// QuotaURL is the coding plan's quota endpoint. It comes from CodexBar
// b6e65a8 (Sources/CodexBarCore/Resources/Plugins/zai.js:34), the pinned
// source in docs/providers/catalog.md, and answered 200 to the #2010 live
// probe on 2026-11-21 with a coding-plan key as a Bearer token.
const QuotaURL = "https://" + Host + "/api/monitor/usage/quota/limit"

// Destination returns the reviewed outbound.FixedDestination: a bodyless GET.
// Authorization is attached separately through Auth.
func Destination() outbound.FixedDestination {
	return outbound.FixedDestination{
		Key:     DestinationKey,
		URL:     QuotaURL,
		Method:  "GET",
		Headers: map[string]string{"Accept": "application/json"},
	}
}

// Auth is the probe's `Authorization: Bearer <key>`.
func Auth() outbound.AuthMethod {
	return outbound.AuthMethod{Header: "Authorization", Prefix: "Bearer "}
}

// IsZaiRoute reports whether a redacted route endpoint
// (session.RouteObservation.Endpoint) names Host exactly, compared on the
// parsed hostname — never a substring, so api.z.ai.example.com and
// example.com/api.z.ai do not qualify.
func IsZaiRoute(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), Host)
}
