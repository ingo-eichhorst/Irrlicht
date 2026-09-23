// billing_attribution.go implements issue #2013's attribution rules: the
// derivation that keeps a GATEWAY, an UPSTREAM BILLER and a LOCAL RUNTIME
// three distinct outcomes instead of one confident guess.
//
// This file is rules only. Nothing in core/ calls ResolveBillingAttribution
// today, and that is the shape #2013 was scoped to: its body §4 says that
// without a consented AWS, Azure or Google Cloud account the ticket
// "delivers the attribution rules and fixtures, and no integration", and its
// triage comment settled §9 question 1 the same way ("Land this as a
// rules-and-fixtures ticket. Implement no cloud billing integration."). The
// one open maintainer gate is §9 question 3 — how "known gateway, unknown
// account" should READ in the UI — which is why no frontend consumes this
// yet. See docs/providers/cloud-attribution.md.
//
// # What this derivation may read
//
// Exactly two evidence inputs plus one explicitly-demoted hint:
//
//   - RouteObservation (#2002) — where the process was pointed. A route is
//     not a biller.
//   - RateLimitSnapshot (#1994) — what a provider reported about itself.
//     Only AttributionQualityConfirmed counts, matching
//     services.ProviderForSession, which this file deliberately does not
//     change.
//   - catalogHint — a LiteLLM-style litellm_provider string. Retained as
//     supporting evidence and never promoted to Provider (#1977 §3.1: "a
//     served model is also supporting evidence unless a validated route
//     establishes the billing relationship"). core/pkg/capacity/co2.go's
//     co2CoefficientsForModel already refuses to classify by the Family this
//     field derives, "inconsistent across hosting paths for the same model
//     family (e.g. Gemini shows up as 'gemini', 'vertex_ai-language-models',
//     or via Vertex/Bedrock aliases depending on how it's served)" — the same
//     unreliability, read at the same field, for a different reason.
//
// It reads no credential, makes no request, and imports nothing: the package
// list at the top of this file is empty on purpose.
package session

// Billing-attribution outcomes. These are the three distinctions #2013 §1
// exists to protect, plus the two states that say "not enough was observed".
//
// The set is closed and lives in Go rather than in data for the reason
// tools/onboarding-factory/internal/provider/schema.go gives for its own
// claim states: a state that could be invented in data is a state that could
// be invented to silence a finding.
const (
	// OutcomeUnobserved: no usable endpoint observation exists — the route
	// was denied, unreadable, malformed, or named no override at all. It is
	// never "the default provider" (session.RouteAbsent's doc comment draws
	// the same line).
	OutcomeUnobserved = "unobserved"

	// OutcomeDirect: a non-local endpoint was observed AND a provider
	// confirmed the billing relationship itself.
	//
	// It is NOT "the only outcome that carries a Provider", which an earlier
	// draft of this comment claimed and the code does not do. Outcome grades
	// the ROUTE and Quality grades the BILLER; they are two axes on purpose,
	// so a session whose route was never observed can still carry a
	// confirmed Provider from its own statusline snapshot. Measured:
	// ResolveBillingAttribution(nil, <confirmed anthropic snapshot>, "")
	// returns Outcome="unobserved" with Quality="confirmed" and
	// Provider="anthropic" — pinned by
	// TestResolveBillingAttribution_ConfirmedBillerSurvivesAnUnobservedRoute,
	// which exists because the first version of this file asserted the
	// invariant without one.
	OutcomeDirect = "direct"

	// OutcomeGateway: a non-local endpoint was observed and nothing
	// confirmed who was billed. This is the honest, partially-known result
	// #1977 §3.1 explicitly permits ("a known gateway with an unknown
	// billing account") — not a failure and not a fallback to the adapter's
	// usual provider.
	OutcomeGateway = "gateway"

	// OutcomeLocalOrPrivateEndpoint: the observed endpoint's host is a
	// loopback or private-network address. It means exactly that.
	// RouteObservation.Local's own doc comment and #1977 §3.3 both say what
	// it does NOT mean: not local inference, and not a zero charge — a local
	// gateway can forward to a paid upstream service.
	OutcomeLocalOrPrivateEndpoint = "local-or-private-endpoint"

	// OutcomeLocalRuntime: inference happened on this machine and no
	// upstream request left it.
	//
	// ResolveBillingAttribution NEVER returns this. Reaching it needs
	// evidence that nothing left the machine (#2013 §1.3's probe row 4), and
	// no observation Irrlicht makes today provides any: a loopback
	// RouteObservation is byte-identical for a forwarding gateway and a true
	// runtime, because RouteObservation carries no field that could differ
	// between them. Verified by running, not by reading —
	// TestResolveBillingAttribution_NeverInfersALocalRuntime drives every
	// combination of route status, locality and billing evidence and
	// requires this constant never to appear, and
	// TestLoopbackIsIndistinguishable_GatewayVersusRuntime asserts the two
	// observations compare equal. The constant exists so the outcome is
	// nameable the day something does establish it.
	OutcomeLocalRuntime = "local-runtime"
)

// Outcomes is the closed set, in "least observed first" order.
var Outcomes = []string{
	OutcomeUnobserved,
	OutcomeLocalOrPrivateEndpoint,
	OutcomeGateway,
	OutcomeDirect,
	OutcomeLocalRuntime,
}

// Attribution-quality values for BillingAttribution.Quality.
//
// Deliberately a SEPARATE vocabulary from RateLimitSnapshot.AttributionQuality
// (AttributionQualityConfirmed / AttributionQualityEstimated), which grades one
// stored snapshot's own stamp and is what services.ProviderForSession reads.
// This one grades a DERIVED result over two inputs and needs a value that
// vocabulary has no room for: "we know the route and not the biller". Reusing
// AttributionQualityEstimated for it would be wrong twice over — that value
// means "a heuristic produced this", and a gateway observation is not a
// heuristic.
const (
	// BillingQualityUnresolved: nothing established who was billed.
	BillingQualityUnresolved = "unresolved"

	// BillingQualityGatewayKnownAccountUnknown: the route is known and the
	// billing account is not. #1977 §3.1's "partially known result", kept as
	// its own value precisely so it cannot decay into either neighbour.
	BillingQualityGatewayKnownAccountUnknown = "gateway-known-account-unknown"

	// BillingQualityConfirmed: a provider reported the relationship itself,
	// with AttributionQualityConfirmed on the snapshot.
	BillingQualityConfirmed = "confirmed"
)

// BillingQualities is the closed set, weakest first.
var BillingQualities = []string{
	BillingQualityUnresolved,
	BillingQualityGatewayKnownAccountUnknown,
	BillingQualityConfirmed,
}

// Charge-evidence values for BillingAttribution.Charge.
//
// There are TWO values and there is no third. A "none"/"zero" member is
// absent by construction, because nothing Irrlicht observes can establish
// that a request cost nothing: the absence of a cloud credential is not that
// evidence and neither is a loopback address (#2013 §1.2). Pinned by
// TestChargeVocabularyHasNoZeroCharge, which fails if the set ever grows one.
const (
	// ChargeUnknown: no evidence about who paid, or whether anyone did.
	ChargeUnknown = "unknown"
	// ChargeProviderReported: a provider reported quota or balance for this
	// session itself, which is evidence that it billed the account.
	ChargeProviderReported = "provider-reported"
)

// Charges is the closed set.
var Charges = []string{ChargeUnknown, ChargeProviderReported}

// BillingAttribution is one derived reading of "who was billed for this
// session, and how sure are we". It is computed from evidence on demand and
// is never persisted — every field it reads already round-trips on
// SessionState.Route and SessionState.Metrics.RateLimit, so storing a second
// copy would create a stale one.
type BillingAttribution struct {
	// Outcome is one of the Outcome* constants. Always set.
	Outcome string `json:"outcome"`

	// Quality is one of the BillingQuality* constants. Always set.
	Quality string `json:"quality"`

	// Charge is one of the Charge* constants. Always set, and never a zero
	// charge — see the Charges doc comment.
	Charge string `json:"charge"`

	// Endpoint and RouteSource mirror the observation, populated only when
	// an endpoint was actually observed. Already redacted by
	// processlifecycle.redactEndpoint before it reaches RouteObservation;
	// this file adds nothing to them and strips nothing further.
	Endpoint    string `json:"endpoint,omitempty"`
	RouteSource string `json:"route_source,omitempty"`

	// RouteStatus is the observation's own status, carried through so a
	// caller can tell "denied" from "absent" from "malformed" without a
	// second lookup. Empty when no observation was supplied at all.
	RouteStatus string `json:"route_status,omitempty"`

	// Provider and AccountRef are the CONFIRMED billing identity, copied
	// from the snapshot only when it carries AttributionQualityConfirmed.
	// Empty means not confirmed — never "confirmed empty".
	Provider   string `json:"provider,omitempty"`
	AccountRef string `json:"account_ref,omitempty"`

	// Evidence names what established Provider, copied from the snapshot's
	// AttributionEvidence. An adapter name alone is never valid evidence.
	Evidence string `json:"evidence,omitempty"`

	// CatalogHint is the served-model catalog's provider string
	// (litellm_provider), retained as supporting evidence. It is NOT a
	// biller and is never copied into Provider — #1977 §3.1 forbids exactly
	// that promotion, and #2013 §1.1 names this file as where the temptation
	// is strongest. Guarded by
	// TestResolveBillingAttribution_CatalogHintNeverBecomesTheProvider and
	// by the committed mutation in
	// tools/lib/cloud-attribution-mutations_test.sh.
	CatalogHint string `json:"catalog_hint,omitempty"`
}

// ResolveBillingAttribution derives the billing attribution for one session
// from its route observation and its own quota snapshot.
//
// route or rl may be nil; the result is always fully populated, and the
// zero-evidence answer is the explicit {unobserved, unresolved, unknown}
// triple rather than a plausible default.
func ResolveBillingAttribution(route *RouteObservation, rl *RateLimitSnapshot, catalogHint string) BillingAttribution {
	a := BillingAttribution{
		Outcome: OutcomeUnobserved,
		Quality: BillingQualityUnresolved,
		Charge:  ChargeUnknown,
	}
	// Catalog metadata enters the result as a hint and stops there. This
	// single assignment is the whole of what the served-model catalog is
	// allowed to contribute.
	a.CatalogHint = catalogHint

	confirmed := hasConfirmedBiller(rl)
	if confirmed {
		a.Provider = rl.Provider
		a.AccountRef = rl.ConfirmedAccountRef
		a.Evidence = rl.AttributionEvidence
		a.Quality = BillingQualityConfirmed
		a.Charge = ChargeProviderReported
	}

	if route == nil {
		return a
	}
	a.RouteStatus = route.Status
	if route.Status != RouteObserved {
		// Denied, unreadable, absent and malformed are four different
		// reasons to have no endpoint, and none of them is an endpoint.
		// RouteStatus above keeps them apart for the caller.
		return a
	}
	a.Endpoint = route.Endpoint
	a.RouteSource = route.Source

	switch {
	case route.Local:
		// The endpoint is local or private. That is the entire finding.
		// Charge is left exactly as the billing evidence above set it —
		// ChargeUnknown when nothing confirmed a biller — because locality
		// is not evidence about cost in either direction.
		a.Outcome = OutcomeLocalOrPrivateEndpoint
	case confirmed:
		a.Outcome = OutcomeDirect
	default:
		a.Outcome = OutcomeGateway
		a.Quality = BillingQualityGatewayKnownAccountUnknown
	}
	return a
}

// hasConfirmedBiller reports whether rl establishes a billing provider for
// its own session. The bar is the same one services.ProviderForSession
// applies — AttributionQualityConfirmed and a non-empty Provider — and this
// file does not relax it. That function is a lock for #2013: it is unchanged
// by this ticket and its tests pass before and after.
func hasConfirmedBiller(rl *RateLimitSnapshot) bool {
	return rl != nil && rl.AttributionQuality == AttributionQualityConfirmed && rl.Provider != ""
}
