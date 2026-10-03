// balance_coverage.go is issue #2009's binding rule for a prepaid balance:
// which session's spend a CreditsSnapshot may be presented as covering.
//
// This file is rules only, the same shape as billing_attribution.go (#2013):
// nothing in core/ calls BalanceCovers yet, because #2009's required live
// probe could not run (no account access was available to the run that wrote
// this — see the #2009 PR body), so no balance poller is wired to a session.
// The rule exists first so the first integration that does attach a balance
// to a session has exactly one predicate to go through.
//
// # Why a balance needs a binding rule at all
//
// A balance is a property of an account or of a key, never of a model or a
// route. OpenRouter is the worked example (#2009 §1.2, #1977 §1): one
// endpoint serves two billing arrangements — OpenRouter's own credits, and
// "bring your own key", where the request is billed to the user's own
// upstream provider account
// (https://openrouter.ai/docs/guides/overview/auth/byok). The OpenRouter
// credits balance describes only the first. Presenting it beside a BYOK
// request would tell the user how much money is left on an account that is
// not paying for what they are looking at.
//
// It reads no credential, makes no request, and imports nothing.
package session

// Billing arrangements for BalanceUse.Arrangement. The set is closed; ""
// (the zero value) means the arrangement was not established, and reads as
// "not covered" — never as the provider's usual arrangement.
const (
	// BillingArrangementProviderCredits: the request was billed against the
	// provider's own prepaid credits or balance — the only arrangement a
	// provider-reported balance can describe.
	BillingArrangementProviderCredits = "provider-credits"
	// BillingArrangementBYOK: the request went through a gateway with the
	// user's own upstream key, so the upstream provider billed it. The
	// gateway's credits balance does not describe it.
	BillingArrangementBYOK = "byok"
)

// BillingArrangements is the closed set.
var BillingArrangements = []string{BillingArrangementProviderCredits, BillingArrangementBYOK}

// Balance scopes for RateLimitSnapshot.QuotaScope on a snapshot carrying
// Credits: what the balance is a balance OF.
const (
	// BalanceScopeAccount: the balance belongs to a provider account;
	// RateLimitSnapshot.ConfirmedAccountRef names it.
	BalanceScopeAccount = "account"
	// BalanceScopeAPIKey: the balance (or limit) belongs to one API key;
	// RateLimitSnapshot.QuotaID names it.
	BalanceScopeAPIKey = "api_key"
)

// BalanceUse describes one use of a provider that a balance might cover —
// in practice, a session's requests. Every field is evidence the caller
// established about the USE, not about the balance.
type BalanceUse struct {
	// Provider is the confirmed billing provider of the use.
	Provider string
	// Arrangement is one of the BillingArrangement* constants, or "" when
	// it was not established.
	Arrangement string
	// Scope is a BalanceScope* constant and ScopeRef the account or key
	// reference the use was confirmed to bill against.
	Scope    string
	ScopeRef string
}

// BalanceScopeRef returns the account or key reference a balance snapshot is
// bound to, or "" when it is bound to nothing confirmed. A balance whose
// scope is unrecognised is bound to nothing: a validator that cannot read
// its input checks more, never less.
func BalanceScopeRef(s *RateLimitSnapshot) string {
	if s == nil {
		return ""
	}
	switch s.QuotaScope {
	case BalanceScopeAccount:
		return s.ConfirmedAccountRef
	case BalanceScopeAPIKey:
		return s.QuotaID
	default:
		return ""
	}
}

// BalanceCovers reports whether the prepaid balance in bal may be presented
// as covering use. It FAILS CLOSED on every axis:
//
//   - bal must carry Credits and be a confirmed observation for a named
//     provider (the bar services.ProviderForSession and
//     hasConfirmedBiller already apply);
//   - use must be billed against that same provider's own credits —
//     BillingArrangementBYOK and an unestablished arrangement both fail;
//   - both sides must name the same scope kind and the same non-empty
//     scope reference. A balance bound to no confirmed account or key covers
//     no use at all, rather than every use of its provider: two sessions on
//     two different keys must never be shown one key's balance.
//
// Each of the last two rules has a committed mutation fixture
// (tools/lib/balance-coverage-byok-mutations_test.sh and
// tools/lib/balance-coverage-scope-mutations_test.sh) that removes it and
// requires TestBalanceCovers to go red.
func BalanceCovers(bal *RateLimitSnapshot, use BalanceUse) bool {
	if bal == nil || bal.Credits == nil {
		return false
	}
	if bal.AttributionQuality != AttributionQualityConfirmed || bal.Provider == "" {
		return false
	}
	if use.Provider != bal.Provider {
		return false
	}
	if use.Arrangement != BillingArrangementProviderCredits {
		return false
	}
	ref := BalanceScopeRef(bal)
	return ref != "" && use.Scope == bal.QuotaScope && use.ScopeRef == ref
}
