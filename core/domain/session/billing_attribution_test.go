package session

import (
	"slices"
	"strings"
	"testing"
)

// confirmedSnapshot is the shape claudecode/statusline.go and codex/parser.go
// stamp: a provider that reported its own quota, with
// AttributionQualityConfirmed.
func confirmedSnapshot() *RateLimitSnapshot {
	return &RateLimitSnapshot{
		Provider:            ProviderAnthropic,
		ConfirmedAccountRef: "acct-123",
		AttributionEvidence: "claude_code_statusline",
		AttributionQuality:  AttributionQualityConfirmed,
	}
}

// TestResolveBillingAttribution_CatalogHintNeverBecomesTheProvider is guard 1
// of issue #2013 §7: litellm_provider is catalog metadata about where a model
// CAN be served, never evidence of who billed a request (#1977 §3.1). The
// mutation that reddens it is committed in
// tools/lib/cloud-attribution-mutations_test.sh.
func TestResolveBillingAttribution_CatalogHintNeverBecomesTheProvider(t *testing.T) {
	// The exact confusion #2013 §1.1 names: the model catalog says this
	// model is served on Bedrock, and nothing has confirmed a biller.
	got := ResolveBillingAttribution(
		&RouteObservation{Status: RouteObserved, Endpoint: "https://bedrock-runtime.us-east-1.amazonaws.com", Source: "env:ANTHROPIC_BASE_URL"},
		nil,
		"bedrock",
	)
	if got.Provider != "" {
		t.Errorf("Provider = %q, want %q — a catalog hint is not a biller", got.Provider, "")
	}
	if got.CatalogHint != "bedrock" {
		t.Errorf("CatalogHint = %q, want %q — the hint is retained as supporting evidence", got.CatalogHint, "bedrock")
	}
	if got.Quality != BillingQualityGatewayKnownAccountUnknown {
		t.Errorf("Quality = %q, want %q", got.Quality, BillingQualityGatewayKnownAccountUnknown)
	}

	// And it must not override a real one either: a confirmed Anthropic
	// snapshot stays Anthropic even when the catalog says "bedrock".
	withEvidence := ResolveBillingAttribution(
		&RouteObservation{Status: RouteObserved, Endpoint: "https://api.anthropic.com", Source: "env:ANTHROPIC_BASE_URL"},
		confirmedSnapshot(),
		"bedrock",
	)
	if withEvidence.Provider != ProviderAnthropic {
		t.Errorf("Provider = %q, want %q — confirmed evidence decides, not the catalog", withEvidence.Provider, ProviderAnthropic)
	}
}

// TestResolveBillingAttribution_LoopbackIsNeverARuntimeOrAZeroCharge is guard
// 2 of #2013 §7. A loopback endpoint proves the endpoint is local. It proves
// nothing about where inference ran and nothing about cost (#1977 §3.3,
// RouteObservation.Local's own doc comment).
func TestResolveBillingAttribution_LoopbackIsNeverARuntimeOrAZeroCharge(t *testing.T) {
	got := ResolveBillingAttribution(
		&RouteObservation{Status: RouteObserved, Endpoint: "http://127.0.0.1:4000", Source: "env:ANTHROPIC_BASE_URL", Local: true},
		nil,
		"",
	)
	if got.Outcome != OutcomeLocalOrPrivateEndpoint {
		t.Errorf("Outcome = %q, want %q — a loopback address is an endpoint finding, not an inference-location one",
			got.Outcome, OutcomeLocalOrPrivateEndpoint)
	}
	if got.Outcome == OutcomeLocalRuntime {
		t.Errorf("Outcome = %q: a local gateway can forward to a paid upstream service", got.Outcome)
	}
	if got.Charge != ChargeUnknown {
		t.Errorf("Charge = %q, want %q — the absence of a cloud credential is not evidence of a zero charge",
			got.Charge, ChargeUnknown)
	}
	if got.Quality != BillingQualityUnresolved {
		t.Errorf("Quality = %q, want %q", got.Quality, BillingQualityUnresolved)
	}
}

// TestResolveBillingAttribution_GatewayKeepsTheAccountUnknown is guard 4 of
// #2013 §7: a known gateway with an unknown billing account is a result
// #1977 §3.1 explicitly permits, and collapsing it into a confirmed
// attribution is the failure this pins.
func TestResolveBillingAttribution_GatewayKeepsTheAccountUnknown(t *testing.T) {
	got := ResolveBillingAttribution(
		&RouteObservation{Status: RouteObserved, Endpoint: "https://gateway.example.com/v1", Source: "env:ANTHROPIC_BASE_URL"},
		nil,
		"",
	)
	if got.Outcome != OutcomeGateway {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeGateway)
	}
	if got.Quality != BillingQualityGatewayKnownAccountUnknown {
		t.Errorf("Quality = %q, want %q — the partially-known result must survive", got.Quality, BillingQualityGatewayKnownAccountUnknown)
	}
	if got.Quality == BillingQualityConfirmed {
		t.Error("a gateway observation with no billing evidence read as confirmed")
	}
	if got.Provider != "" || got.AccountRef != "" {
		t.Errorf("Provider/AccountRef = %q/%q, want empty — nothing confirmed either", got.Provider, got.AccountRef)
	}
	if got.Charge != ChargeUnknown {
		t.Errorf("Charge = %q, want %q", got.Charge, ChargeUnknown)
	}
	if got.Endpoint != "https://gateway.example.com/v1" {
		t.Errorf("Endpoint = %q — the known half of the result must be preserved too", got.Endpoint)
	}
}

// TestResolveBillingAttribution_NeverInfersALocalRuntime drives every
// combination of route status, locality and billing evidence and requires
// OutcomeLocalRuntime never to appear. This is the executable form of the
// claim in that constant's doc comment: no observation Irrlicht makes today
// establishes that nothing left the machine (#2013 §1.3's probe row 4).
func TestResolveBillingAttribution_NeverInfersALocalRuntime(t *testing.T) {
	statuses := []string{RouteObserved, RouteAbsent, RouteUnreadable, RouteDenied, RouteMalformed}
	hints := []string{"", "bedrock", "vertex_ai-language-models", "ollama", "openai"}
	snapshots := []*RateLimitSnapshot{nil, {}, confirmedSnapshot(), {Provider: ProviderOpenAI}}

	seen := 0
	for _, st := range statuses {
		for _, local := range []bool{false, true} {
			for _, hint := range hints {
				for _, rl := range snapshots {
					got := ResolveBillingAttribution(
						&RouteObservation{Status: st, Endpoint: "http://127.0.0.1:1234", Local: local}, rl, hint)
					seen++
					if got.Outcome == OutcomeLocalRuntime {
						t.Fatalf("status=%s local=%v hint=%q: Outcome = %q, which no observation can establish",
							st, local, hint, got.Outcome)
					}
					if !slices.Contains(Outcomes, got.Outcome) {
						t.Fatalf("status=%s local=%v hint=%q: Outcome %q is outside the closed set", st, local, hint, got.Outcome)
					}
					if !slices.Contains(BillingQualities, got.Quality) {
						t.Fatalf("status=%s local=%v hint=%q: Quality %q is outside the closed set", st, local, hint, got.Quality)
					}
					if !slices.Contains(Charges, got.Charge) {
						t.Fatalf("status=%s local=%v hint=%q: Charge %q is outside the closed set", st, local, hint, got.Charge)
					}
				}
			}
		}
	}
	// A table that silently shrank to nothing would pass every assertion
	// above: absence of a finding and inability to look must not look alike.
	if want := len(statuses) * 2 * len(hints) * len(snapshots); seen != want {
		t.Fatalf("drove %d combinations, want %d — the table did not run", seen, want)
	}
}

// TestLoopbackIsIndistinguishable_GatewayVersusRuntime is the probe #2013 §6
// rows 3 and 4 ask for, run as a comparison rather than as a live endpoint.
// A local gateway forwarding to a paid upstream and a true local runtime
// produce the SAME observation, because RouteObservation carries no field
// that could differ between them. That is why OutcomeLocalRuntime is
// unreachable from a route observation — and why the honest answer for a
// loopback endpoint is "local or private endpoint", not either of the two
// things it might be.
func TestLoopbackIsIndistinguishable_GatewayVersusRuntime(t *testing.T) {
	// Case A: LiteLLM on :4000 forwarding to a paid Bedrock account.
	forwardingGateway := &RouteObservation{
		Status: RouteObserved, Endpoint: "http://127.0.0.1:4000", Source: "env:ANTHROPIC_BASE_URL", Local: true,
	}
	// Case B: a true local runtime on the same port, nothing leaving the box.
	trueRuntime := &RouteObservation{
		Status: RouteObserved, Endpoint: "http://127.0.0.1:4000", Source: "env:ANTHROPIC_BASE_URL", Local: true,
	}
	if *forwardingGateway != *trueRuntime {
		t.Fatalf("the two observations differ (%+v vs %+v) — if a field now separates them, "+
			"OutcomeLocalRuntime may become reachable and this test should say how",
			*forwardingGateway, *trueRuntime)
	}
	if ResolveBillingAttribution(forwardingGateway, nil, "") != ResolveBillingAttribution(trueRuntime, nil, "") {
		t.Fatal("identical observations produced different attributions")
	}
}

// TestChargeVocabularyHasNoZeroCharge pins the absence #2013 §1.2 depends on:
// there is no "billed nothing" value, because nothing Irrlicht observes can
// establish one.
func TestChargeVocabularyHasNoZeroCharge(t *testing.T) {
	if len(Charges) != 2 {
		t.Fatalf("Charges = %v, want exactly two values", Charges)
	}
	for _, c := range Charges {
		for _, banned := range []string{"none", "zero", "free", "unbilled"} {
			if strings.Contains(c, banned) {
				t.Errorf("Charges contains %q, which reads as a proven zero charge", c)
			}
		}
	}
}

// TestResolveBillingAttribution_UnobservedRoutes keeps #2002's four
// non-observed statuses apart: none of them is "the default provider", and
// each is carried through on RouteStatus rather than collapsed.
func TestResolveBillingAttribution_UnobservedRoutes(t *testing.T) {
	for _, st := range []string{RouteAbsent, RouteUnreadable, RouteDenied, RouteMalformed} {
		got := ResolveBillingAttribution(&RouteObservation{Status: st, Source: "env:ANTHROPIC_BASE_URL"}, nil, "anthropic")
		if got.Outcome != OutcomeUnobserved {
			t.Errorf("status %s: Outcome = %q, want %q", st, got.Outcome, OutcomeUnobserved)
		}
		if got.RouteStatus != st {
			t.Errorf("status %s: RouteStatus = %q, want it carried through", st, got.RouteStatus)
		}
		if got.Endpoint != "" {
			t.Errorf("status %s: Endpoint = %q, want empty", st, got.Endpoint)
		}
		if got.Provider != "" {
			t.Errorf("status %s: Provider = %q, want empty", st, got.Provider)
		}
	}

	// No observation at all is still the explicit zero-evidence triple.
	none := ResolveBillingAttribution(nil, nil, "")
	if none.Outcome != OutcomeUnobserved || none.Quality != BillingQualityUnresolved || none.Charge != ChargeUnknown {
		t.Errorf("nil inputs = %+v, want {unobserved, unresolved, unknown}", none)
	}
	if none.RouteStatus != "" {
		t.Errorf("RouteStatus = %q, want empty when no observation was supplied", none.RouteStatus)
	}
}

// TestResolveBillingAttribution_ConfirmedBillerSurvivesAnUnobservedRoute
// pins the two-axis design: Outcome grades the ROUTE, Quality grades the
// BILLER, and a session whose route was never observed still carries a
// provider that reported its own quota. The original suite covered
// (nil, nil) but never nil-route-plus-confirmed, which let a doc comment
// asserting "the only outcome that carries a Provider" survive.
func TestResolveBillingAttribution_ConfirmedBillerSurvivesAnUnobservedRoute(t *testing.T) {
	for name, route := range map[string]*RouteObservation{
		"no observation at all": nil,
		"denied":                {Status: RouteDenied},
		"unreadable":            {Status: RouteUnreadable},
		"absent":                {Status: RouteAbsent},
		"malformed":             {Status: RouteMalformed, Source: "env:ANTHROPIC_BASE_URL"},
	} {
		got := ResolveBillingAttribution(route, confirmedSnapshot(), "")
		if got.Outcome != OutcomeUnobserved {
			t.Errorf("%s: Outcome = %q, want %q — the route is what was not observed", name, got.Outcome, OutcomeUnobserved)
		}
		if got.Provider != ProviderAnthropic {
			t.Errorf("%s: Provider = %q, want %q — an unobserved route does not erase the biller's own report",
				name, got.Provider, ProviderAnthropic)
		}
		if got.Quality != BillingQualityConfirmed {
			t.Errorf("%s: Quality = %q, want %q", name, got.Quality, BillingQualityConfirmed)
		}
		if got.Charge != ChargeProviderReported {
			t.Errorf("%s: Charge = %q, want %q", name, got.Charge, ChargeProviderReported)
		}
		if got.Endpoint != "" {
			t.Errorf("%s: Endpoint = %q, want empty — no endpoint was observed", name, got.Endpoint)
		}
	}
}

// TestResolveBillingAttribution_DirectBiller is the one outcome that carries
// a provider: a non-local endpoint plus a provider that reported its own
// quota.
func TestResolveBillingAttribution_DirectBiller(t *testing.T) {
	got := ResolveBillingAttribution(
		&RouteObservation{Status: RouteObserved, Endpoint: "https://api.anthropic.com", Source: "env:ANTHROPIC_BASE_URL"},
		confirmedSnapshot(),
		"anthropic",
	)
	if got.Outcome != OutcomeDirect {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeDirect)
	}
	if got.Quality != BillingQualityConfirmed {
		t.Errorf("Quality = %q, want %q", got.Quality, BillingQualityConfirmed)
	}
	if got.Charge != ChargeProviderReported {
		t.Errorf("Charge = %q, want %q", got.Charge, ChargeProviderReported)
	}
	if got.Provider != ProviderAnthropic || got.AccountRef != "acct-123" || got.Evidence != "claude_code_statusline" {
		t.Errorf("identity = %q/%q/%q, want anthropic/acct-123/claude_code_statusline",
			got.Provider, got.AccountRef, got.Evidence)
	}
}

// TestResolveBillingAttribution_UnstampedSnapshotIsNotEvidence pins the bar
// this file shares with services.ProviderForSession: an unstamped or
// estimated snapshot establishes nothing, so a route observation beside it is
// still only a gateway.
func TestResolveBillingAttribution_UnstampedSnapshotIsNotEvidence(t *testing.T) {
	cases := map[string]*RateLimitSnapshot{
		"unstamped":       {Provider: ProviderAnthropic},
		"estimated":       {Provider: ProviderAnthropic, AttributionQuality: AttributionQualityEstimated},
		"confirmed-empty": {AttributionQuality: AttributionQualityConfirmed},
	}
	for name, rl := range cases {
		got := ResolveBillingAttribution(
			&RouteObservation{Status: RouteObserved, Endpoint: "https://gateway.example.com"}, rl, "")
		if got.Provider != "" {
			t.Errorf("%s: Provider = %q, want empty", name, got.Provider)
		}
		if got.Quality != BillingQualityGatewayKnownAccountUnknown {
			t.Errorf("%s: Quality = %q, want %q", name, got.Quality, BillingQualityGatewayKnownAccountUnknown)
		}
	}
}
