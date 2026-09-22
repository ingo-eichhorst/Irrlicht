package session

// Route-observation status values (issue #2002 §1). A session's provider
// endpoint is not always observable, and the reasons it isn't matter for
// #1994's resolver: an operator who denied the read looks nothing like a
// session with no override configured, and a value this package could not
// parse looks nothing like either. Collapsing any two of these into one
// answer is the defect the fixture table in #2002 §6 names directly.
const (
	// RouteObserved means an endpoint override was found and successfully
	// redacted; Endpoint and Source are populated.
	RouteObserved = "observed"
	// RouteAbsent means the source was read successfully and named none of
	// the keys this permission may look for — "no override found in this
	// source", never "the default provider" (#2002 §1.1 fixture table).
	RouteAbsent = "absent"
	// RouteUnreadable means the source itself could not be read (process
	// gone, permission denied, or — on darwin — a hardened-runtime process
	// hiding its env from the kernel entirely). Distinct from RouteAbsent:
	// this is "could not look", not "looked and found nothing".
	RouteUnreadable = "unreadable"
	// RouteDenied means the endpoint-observation permission was not
	// granted, so no read was attempted at all.
	RouteDenied = "denied"
	// RouteMalformed means a value was found for one of the keys but could
	// not be parsed as a URL with both a scheme and a host. Endpoint is left
	// empty on this status — a malformed value may still carry user
	// information, and #2002 §1.3 requires that never be retained.
	RouteMalformed = "malformed"
)

// RouteObservation is one attempt to observe the endpoint a session's
// process is routed through, via a provider base-URL override read from its
// environment (issue #2002). It is evidence for a later resolver (#1994) to
// weigh, not a billing verdict: a route is not a billing target, and this
// type carries no credential, no account identity, and no cost.
//
// Captured once, at first PID assignment, the same point session.Launcher is
// captured — see SessionState.Route and SessionState.Launcher.
type RouteObservation struct {
	// Status is one of the Route* constants above. Always set.
	Status string `json:"status"`

	// Endpoint is the redacted scheme://host[:port][/path] the override
	// named, populated only when Status == RouteObserved. User information,
	// any query string, and any fragment are stripped before this value is
	// ever retained or logged (#2002 §1.3) — a path-bearing base URL is kept
	// as such and is never called an "origin".
	Endpoint string `json:"endpoint,omitempty"`

	// Source names which environment variable produced Endpoint (e.g.
	// "env:ANTHROPIC_BASE_URL"), populated for RouteObserved and
	// RouteMalformed (the key was found; only its value failed to parse).
	Source string `json:"source,omitempty"`

	// Local reports that Endpoint's host is a loopback or private-network
	// address. It proves ONLY that: not local inference, and not a zero
	// cost — a local gateway can forward to a paid upstream service (#2002
	// §1.3). Meaningful only when Status == RouteObserved.
	Local bool `json:"local,omitempty"`
}
