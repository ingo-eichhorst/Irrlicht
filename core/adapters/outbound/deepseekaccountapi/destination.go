// Package deepseekaccountapi is issue #2009's DeepSeek half: the reviewed
// destination and the redaction-boundary parser for DeepSeek's prepaid
// balance endpoint, on top of #2003's accountquota port
// (core/ports/outbound/accountquota.go).
//
// What this package deliberately does NOT contain, and why (recorded in the
// #2009 PR body as the blocked probe):
//
//   - No credential resolver. The run that wrote this package could not
//     inspect where or how the DeepSeek key is stored on a real machine, so a
//     resolver would be written against a guessed format.
//   - No permission declaration, no poller wiring, no session sweep. With no
//     resolver there is nothing to consent to; a consent row that gated
//     nothing would read to a user as a capability that exists.
//
// So no production code path constructs a transport for Destination() today
// (TestDeepSeekDestination_NeverDialsInTheOrdinarySuite checks that by
// scanning this package's non-test sources). The one file that does is the
// live-probe program, behind the live_probe build tag.
//
// DeepSeek the billing provider (session.ProviderDeepSeek) is not the dsh
// coding-agent adapter (core/adapters/inbound/agents/dsh): dsh can be pointed
// at DeepSeek or at any other provider, so nothing here reads or depends on
// that adapter.
package deepseekaccountapi

import outbound "irrlicht/core/ports/outbound"

// DestinationKey names this destination for outbound.AccountQuotaRequest.
const DestinationKey = "deepseek-account-api"

// BalanceURL is DeepSeek's documented balance endpoint: `GET /user/balance`
// (https://api-docs.deepseek.com/api/get-user-balance/) on the documented
// base URL `https://api.deepseek.com` (https://api-docs.deepseek.com/). Both
// pages read on 2026-10-03.
const BalanceURL = "https://api.deepseek.com/user/balance"

// Destination returns the reviewed outbound.FixedDestination for DeepSeek's
// balance endpoint: a plain GET with no body.
func Destination() outbound.FixedDestination {
	return outbound.FixedDestination{
		Key:     DestinationKey,
		URL:     BalanceURL,
		Method:  "GET",
		Headers: map[string]string{"Accept": "application/json"},
	}
}

// Auth returns the outbound.AuthMethod for DeepSeek: the bearer header the
// documented curl example sends (`-H "Authorization: Bearer
// ${DEEPSEEK_API_KEY}"`, https://api-docs.deepseek.com/, read 2026-10-03).
func Auth() outbound.AuthMethod {
	return outbound.AuthMethod{Header: "Authorization", Prefix: "Bearer "}
}
