package museaccountapi

import outbound "irrlicht/core/ports/outbound"

// DestinationKey names this destination for outbound.AccountQuotaRequest and
// for core/application/services.AccountQuotaKey.Provider — one string used
// both ways, so a typo in either direction fails a compile-time reference
// rather than silently mismatching.
const DestinationKey = "muse-account-api"

// SubscriptionURL is the endpoint the pinned herdr-agent-quota source
// documents and issue #2007's deviation instructions name directly:
// POST https://api.meta.ai/muse-code/key. Reviewed here, once, per
// FixedDestination's own doc comment ("the only URLs a transport instance
// can ever dial are the ones its constructor was given, at daemon-wiring
// time, by code a reviewer read").
const SubscriptionURL = "https://api.meta.ai/muse-code/key"

// apiVersion is the value the pinned herdr source sends as x-api-version —
// the same value the Muse CLI itself sends for this call, per that source's
// own comment ("The API version the Muse Code CLI sends with the same
// request").
const apiVersion = "1.0.0"

// Destination returns the reviewed outbound.FixedDestination for Muse's
// account-quota endpoint: POST, a fixed empty-object body, and the static
// headers the pinned source sends alongside the Authorization header
// (Authorization itself is attached separately via AuthMethod, per
// outbound.AccountQuotaRequest.Auth — never duplicated here).
func Destination() outbound.FixedDestination {
	return outbound.FixedDestination{
		Key:    DestinationKey,
		URL:    SubscriptionURL,
		Method: "POST",
		Body:   []byte("{}"),
		Headers: map[string]string{
			"x-api-version": apiVersion,
			"Accept":        "application/json",
		},
	}
}

// Auth returns the outbound.AuthMethod for Muse: a Bearer-prefixed
// Authorization header, matching the pinned source's
// `Authorization: Bearer <access_token>`.
func Auth() outbound.AuthMethod {
	return outbound.AuthMethod{Header: "Authorization", Prefix: "Bearer "}
}
