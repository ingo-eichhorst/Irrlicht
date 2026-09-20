// endpoint_route.go implements issue #2002's shared route mechanism: the
// daemon's one authorized way to observe the provider endpoint a session's
// process is routed through. It is an input for #1994's resolver, not a
// billing verdict — no account read, no credential, no outbound request, and
// no change to how that resolver ranks evidence.
package processlifecycle

import (
	"net"
	"net/url"
	"strings"

	"irrlicht/core/domain/session"
)

// endpointEnvKeys are the env vars the endpoint-observation permission may
// read: provider base-URL overrides. Disjoint from launcherEnvKeys on
// purpose — ports.go's EnvOf doc requires a caller holding only one
// permission to never receive the other's keys, and this is the set that
// makes that true for the endpoint side (#2002 §1.1).
//
// ANTHROPIC_BASE_URL is Claude Code's documented override
// (https://code.claude.com/docs/en/settings) and the only adapter this
// package instruments for PID-based env capture today. Adding a second
// provider's override key here is new-provider integration, which #2002 §2
// excludes; endpointSourceKey below is named once so that stays a one-line
// change when it happens.
var endpointEnvKeys = map[string]struct{}{
	"ANTHROPIC_BASE_URL": {},
}

// endpointSourceKey is the sole key in endpointEnvKeys.
const endpointSourceKey = "ANTHROPIC_BASE_URL"

// ObserveRoute observes the provider endpoint pid's process is routed
// through, redacted before it is ever retained or logged. granted must be
// the CURRENT state of the endpoint-observation permission (EndpointName /
// PermissionKeyEndpointEnv) — when false, no read is attempted at all: the
// "denied" fixture row is "no read happens", not merely an absent answer.
//
// Always returns a non-nil, fully-populated observation. Status names
// exactly one of session.Route{Observed,Absent,Unreadable,Denied,Malformed}
// — #2002 §1's four distinct results, never a default a caller could read as
// "the default provider".
func ObserveRoute(pid int, granted bool) *session.RouteObservation {
	if !granted {
		return &session.RouteObservation{Status: session.RouteDenied}
	}
	env, err := osProc.EnvOf(pid, endpointEnvKeys)
	if err != nil {
		// Cannot look, not "looked and found nothing" — the same line
		// #1537 draws for WriterOf. Anchored here (rather than duplicated
		// per platform) so one mutation fixture covers every OS this
		// reader runs on.
		return &session.RouteObservation{Status: session.RouteUnreadable}
	}
	raw, ok := env[endpointSourceKey]
	if !ok || raw == "" {
		return &session.RouteObservation{Status: session.RouteAbsent}
	}
	source := "env:" + endpointSourceKey
	endpoint, local, ok := redactEndpoint(raw)
	if !ok {
		// raw may still carry userinfo — never returned, even here.
		return &session.RouteObservation{Status: session.RouteMalformed, Source: source}
	}
	return &session.RouteObservation{
		Status:   session.RouteObserved,
		Endpoint: endpoint,
		Source:   source,
		Local:    local,
	}
}

// redactEndpoint parses raw as an absolute URL and strips exactly the
// components #2002 §1.3 forbids retaining — user information, the query
// string, and the fragment — while keeping scheme, host (with port), and
// path. A path-bearing base URL survives as such and must never be called
// an "origin". ok is false when raw does not parse as a URL carrying both a
// scheme and a host; endpoint is then always "", so a malformed value's
// userinfo can never leak through this return.
func redactEndpoint(raw string) (endpoint string, local bool, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false, false
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), isLocalHost(u.Hostname()), true
}

// isLocalHost reports whether host is a loopback, private-network, or
// link-local address, OR the "localhost" name RFC 6761 §6.3 reserves to
// loopback (case-insensitively, and any name under the ".localhost" TLD it
// also reserves) — recognized by name, never by resolving it, so this stays
// a pure string/IP check with no network read. It proves ONLY that: never
// that inference runs locally, and never a zero cost — a local gateway can
// forward to a paid upstream service (#2002 §1.3, session.RouteObservation.Local's
// own doc). Any other host that isn't a literal IP (an ordinary DNS name)
// reports false rather than being resolved.
func isLocalHost(host string) bool {
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
