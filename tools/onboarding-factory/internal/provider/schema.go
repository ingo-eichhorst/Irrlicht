// Package provider models replaydata/providers/ — the BILLING-PRODUCT catalog,
// which is a different thing from the agent × scenario matrix internal/matrix
// and internal/shard model.
//
// The separation is the point (#1977 §8, issue #2008 §1.5): an agent adapter
// is a CLI Irrlicht observes, a provider is a product someone is billed for,
// and the two do not have a one-to-one relationship in either direction. Muse
// is the worked example — the coding-agent adapter is named "muse", its
// billing provider is "meta", and the account-API pseudo-adapter that holds
// the consent row is named "muse-account-api". A model with one name per
// provider would be wrong for the first provider that has two destinations,
// so this schema keeps those identifiers in separate fields.
//
// Everything here is extracted from two integrations that already shipped, as
// #1977 §8 requires ("First prove the contracts with real integrations. Then
// extract."): the native statusline/transcript quota route (claudecode, codex)
// and the Muse account API (#2007).
//
// # Why there is no quota field
//
// The Muse account API returns 200 with {is_subs_active, subs_tier_name} and
// NO usage field at all — measured, and recorded in
// core/adapters/outbound/museaccountapi/testdata/live_probe_2026-09-20.json,
// whose raw_top_level_keys list contains no subs_usage key. A schema with a
// required (or even an optional-but-central) quota block would express that
// provider as an incomplete one. So quota is not a field: it is ONE AXIS among
// four in Capabilities, and a provider whose quota axis is
// "source-specific-unavailable" is as complete as one whose quota axis is
// "live-verified". Two tests pin it: TestVerifyAcceptsAProviderWithNoQuota in
// this package, on a committed fixture, and
// TestTheRealMetaManifestHasNoQuotaAndValidates in cmd/of, on the manifest
// that ships.
//
// # Vocabulary
//
// The claim states are the four already committed in docs/providers/catalog.md
// (#2006) — unassessed, fixture-verified, live-verified,
// source-specific-unavailable. They are deliberately NOT internal/matrix's
// maturity ladder (planned/alpha/beta/stable), which grades an agent adapter's
// onboarding: reusing that ladder here would invite exactly the agent-versus-
// provider conflation #1977 §8 forbids. Note that the token "unassessed" also
// exists in internal/matrix as Disposition("unassessed"); the two are separate
// universes and this package never aliases one to the other.
package provider

import "slices"

// SchemaVersion is the only manifest schema version this package reads. A
// manifest declaring anything else is a finding rather than a best-effort
// parse, because the field set is closed and a future version's extra keys
// would otherwise be reported as unknown keys instead of as a version skew.
const SchemaVersion = 1

// Claim states — the four provider result states from docs/providers/catalog.md.
const (
	// ClaimUnassessed: nothing has looked. Requires no evidence.
	ClaimUnassessed = "unassessed"
	// ClaimFixtureVerified: a committed fixture exercises the axis.
	ClaimFixtureVerified = "fixture-verified"
	// ClaimLiveVerified: a dated live probe or a real recording exercised the
	// axis against the provider itself.
	ClaimLiveVerified = "live-verified"
	// ClaimSourceUnavailable: the route was reached and the axis is NOT
	// available through it. This is a positive finding about a negative
	// result, not a gap — #1977 §5: "A missing account or failed probe means
	// the check was blocked or failed. It does not prove that the provider has
	// no quota source." So it still has to name its evidence.
	ClaimSourceUnavailable = "source-specific-unavailable"
)

// ClaimStates is the closed set, in ladder order for the first three. It is
// defined in Go rather than in data for the reason internal/matrix gives for
// its own trait table: a state that could be invented in data is a state that
// could be invented to silence a finding.
var ClaimStates = []string{
	ClaimUnassessed, ClaimFixtureVerified, ClaimLiveVerified, ClaimSourceUnavailable,
}

// IsValidClaim reports whether v is one of the four states.
func IsValidClaim(v string) bool { return slices.Contains(ClaimStates, v) }

// claimRank orders the three LADDER states only. ClaimSourceUnavailable is
// off-ladder on purpose — "the route reports no quota" is not a weaker version
// of "the route reports quota", so it is compared by its evidence requirement
// instead of by rank (see Status).
func claimRank(v string) int {
	switch v {
	case ClaimUnassessed:
		return 0
	case ClaimFixtureVerified:
		return 1
	case ClaimLiveVerified:
		return 2
	}
	return -1
}

// Evidence kinds.
const (
	// EvidenceLiveProbe: a dated, redacted capture of a real request to the
	// provider (the Muse live probe fixture).
	EvidenceLiveProbe = "live-probe"
	// EvidenceRecording: a committed recording of a real agent session that
	// carries the field in question (a codex transcript's rate_limits).
	EvidenceRecording = "recording"
	// EvidenceFixture: a committed fixture that is not itself a capture of a
	// live interaction — a shape fixture built from a reviewed wire format.
	EvidenceFixture = "fixture"
	// EvidenceSource: a source file, in this repository or a pinned upstream
	// one, read at a revision. docs/providers/catalog.md is explicit that this
	// is an evidence-QUALITY label and never a result state on its own.
	EvidenceSource = "source"
)

// EvidenceKinds is the closed set.
var EvidenceKinds = []string{
	EvidenceLiveProbe, EvidenceRecording, EvidenceFixture, EvidenceSource,
}

// liveEvidenceKinds are the kinds that can earn ClaimLiveVerified. #1977 §11.11
// — "Provider status cannot claim live verification from a fixture alone" — is
// this variable.
var liveEvidenceKinds = []string{EvidenceLiveProbe, EvidenceRecording}

// Observation strategies — #1977 §4's source table, which is a different axis
// from the credential resolver below (§8: "A browser credential resolver is
// not itself a quota transport").
const (
	StrategyNativeHook       = "native-hook"       // the agent posts it to us (claudecode statusline)
	StrategyNativeTranscript = "native-transcript" // we read it out of the agent's transcript (codex)
	StrategyAccountAPI       = "account-api"       // we call the provider's account endpoint (muse)
	StrategyAgentStore       = "agent-store"       // an agent-owned SQLite/JSON store
	StrategyBrowserSession   = "browser-session"   // an account API reached with browser-held credentials
)

// Strategies is the closed set.
var Strategies = []string{
	StrategyNativeHook, StrategyNativeTranscript, StrategyAccountAPI,
	StrategyAgentStore, StrategyBrowserSession,
}

// Authentication methods — the axis #1977 §8 keeps separate from the
// credential LOCATION. Muse's binary carries a payment_tier enum whose values
// (subscription / stored_api_key / env_api_key) show why: an OAuth-
// authenticated account can still be billed pay-as-you-go, so the auth method
// does not determine the billing product.
const (
	AuthNone        = "none"
	AuthOAuthBearer = "oauth-bearer"
	AuthAPIKey      = "api-key"
	AuthCookie      = "cookie"
)

// AuthMethods is the closed set.
var AuthMethods = []string{AuthNone, AuthOAuthBearer, AuthAPIKey, AuthCookie}

// Credential resolver kinds — the LOCATION axis.
const (
	ResolverNone     = "none" // the strategy needs no credential of its own
	ResolverFile     = "file"
	ResolverKeychain = "keychain"
	ResolverBrowser  = "browser-profile"
)

// ResolverKinds is the closed set.
var ResolverKinds = []string{ResolverNone, ResolverFile, ResolverKeychain, ResolverBrowser}

// Implementation kinds — #1977 §8 and issue #2008 §1.4: "A manifest can add
// recognition metadata, or configure an existing reviewed strategy. A new
// authentication flow, parser or transport needs implementation and tests."
const (
	// ImplCodeBacked: a reviewed package implements this route. Verify
	// requires the package directory and every named mutation fixture to
	// exist, so the claim cannot be made by JSON alone.
	ImplCodeBacked = "code-backed"
	// ImplDataOnly: recognition metadata over an ALREADY-reviewed strategy,
	// with no new code. Status renders the difference.
	ImplDataOnly = "data-only"
)

// ImplKinds is the closed set.
var ImplKinds = []string{ImplCodeBacked, ImplDataOnly}

// Fixture kinds.
const (
	FixtureResponse = "response" // a redacted provider response body
	FixtureShape    = "shape"    // a wire-shape fixture built from a reviewed source
)

// FixtureKinds is the closed set.
var FixtureKinds = []string{FixtureResponse, FixtureShape}

// Axes are the four capability axes every manifest declares, in render order.
// The set is closed and lives in Go for the same reason ClaimStates does. Every
// manifest must declare EXACTLY these four — a manifest that could omit an axis
// could omit the one that is inconvenient, which is the failure mode
// "unassessed" exists to express instead.
var Axes = []Axis{
	{ID: "billing_mode", Title: "identifies the billed product or plan the account is on"},
	{ID: "quota_windows", Title: "reports usage windows with a percentage and a reset"},
	{ID: "balance", Title: "reports a currency or credit balance"},
	{ID: "account_identity", Title: "returns a stable provider account reference"},
}

// Axis is one capability dimension.
type Axis struct {
	ID    string
	Title string
}

// AxisIDs returns the four axis ids in render order.
func AxisIDs() []string {
	ids := make([]string, 0, len(Axes))
	for _, a := range Axes {
		ids = append(ids, a.ID)
	}
	return ids
}

// IsValidAxis reports whether id names one of the four axes.
func IsValidAxis(id string) bool {
	return slices.ContainsFunc(Axes, func(a Axis) bool { return a.ID == id })
}

// Manifest is replaydata/providers/<id>/manifest.json.
//
// The field set is CLOSED: Verify decodes twice, once into this struct and
// once into a map, and reports any key that is not named here. That is the
// same shape validateScenarioEntry uses for the scenario catalog, and it is
// what stops a manifest from carrying a field nothing reads.
type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name"`
	Comment       []string `json:"_comment,omitempty"`

	// Products are the billing products this provider sells, with the account
	// or quota scope each one applies to. A provider can have several
	// (#1977 §5), which is why this is a list and not a pair of fields.
	Products []Product `json:"products"`

	// Observation is the transport axis: how Irrlicht sees anything at all.
	Observation Observation `json:"observation"`

	// Authentication is the method axis, separate from CredentialResolvers.
	Authentication Authentication `json:"authentication"`

	// CredentialResolvers is the location axis. Muse's file credential and its
	// Keychain credential feed ONE transport — #1977 §8's worked example, and
	// the reason this is a list under a single Observation.
	CredentialResolvers []CredentialResolver `json:"credential_resolvers"`

	// Platforms names the GOOS values the route works on.
	Platforms []string `json:"platforms"`

	// Redaction is the shared obligation from #2003/#2007 that is checkable at
	// the data layer: every committed fixture's top-level keys must be inside
	// AllowedFields. A fixture carrying a field outside the allowlist is a
	// finding, which is how the redaction contract keeps being exercised by
	// data long after the integration's own Go tests stop being read.
	Redaction Redaction `json:"redaction"`

	Fixtures []Fixture `json:"fixtures"`

	// Capabilities maps each of the four Axes to a claim. Exactly four entries.
	Capabilities map[string]Capability `json:"capabilities"`

	// Evidence is the pool capability claims cite by id.
	Evidence []EvidenceEntry `json:"evidence"`
}

// Product is one billing product.
//
// QuotaScope says what the PRODUCT bills against — #1977 §7 files it under
// billing identity, next to the product and the confirmed account reference.
// It is not what Irrlicht currently manages to key a poll on: Muse's poller
// falls back to a session-scoped key because the probe found no stable
// provider account id, and that degradation belongs in the account_identity
// axis (where meta's manifest records it), not in the product's scope.
type Product struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	QuotaScope string `json:"quota_scope"`
}

// Observation carries the transport axis and the implementation contract.
type Observation struct {
	Strategy string `json:"strategy"`
	// DestinationKeys are the reviewed destination identifiers this route
	// uses. For Muse this is the one FixedDestination key
	// ("muse-account-api"); for a native route it is empty, because nothing is
	// dialled. Kept separate from Manifest.ID because AccountPoller.Revoke
	// keys on the PROVIDER while the transport keys on the DESTINATION.
	DestinationKeys []string       `json:"destination_keys"`
	Implementation  Implementation `json:"implementation"`
}

// Implementation is #2008 §1.4's narrower contract, expressed as data Verify
// can check without importing core: a code-backed route names a package
// directory, a permission, and its mutation fixtures, and every one of those
// paths must exist.
type Implementation struct {
	Kind string `json:"kind"`
	// Package is a repo-relative directory. Empty for data-only.
	Package string `json:"package,omitempty"`
	// PermissionName is the adapter/pseudo-adapter identity that holds the
	// consent row. Empty when the route needs no permission of its own (a
	// native route runs inside the agent adapter's existing read permission).
	PermissionName string `json:"permission_name,omitempty"`
	// PermissionKey is the agent.Agent permission key, when there is one.
	PermissionKey string `json:"permission_key,omitempty"`
	// Tests are repo-relative paths to the test files that exercise this
	// route. #1977 §8 says a new authentication flow, parser or transport
	// "needs implementation AND tests", so a code-backed manifest names both
	// and Verify requires both to exist. This is the field that stops a
	// manifest from being a licence to skip code.
	Tests []string `json:"tests,omitempty"`
	// MutationFixtures are repo-relative paths to the committed mutation
	// fixtures that guard this route, where it has any. Optional: the native
	// statusline and transcript routes predate that convention, and inventing
	// a fixture name to satisfy a schema is the opposite of evidence.
	MutationFixtures []string `json:"mutation_fixtures,omitempty"`
}

// Authentication is the method axis.
type Authentication struct {
	Method string `json:"method"`
	// Header and Prefix mirror outbound.AuthMethod when there is one.
	Header string `json:"header,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

// CredentialResolver is one credential location.
type CredentialResolver struct {
	Kind     string `json:"kind"`
	Location string `json:"location,omitempty"`
	// Format says what is AT that location. Muse needed its own resolvers
	// precisely because accountquota's generic ones wrap a plain secret while
	// Muse's credential is JSON in both locations.
	Format string `json:"format,omitempty"`
}

// Redaction is the allowlist a committed fixture's top-level keys must sit in.
type Redaction struct {
	AllowedFields []string `json:"allowed_fields"`
	Note          string   `json:"note,omitempty"`
}

// Fixture is a committed input/output fixture, by path relative to the
// provider directory.
type Fixture struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
}

// Capability is one axis's claim plus the evidence ids backing it.
type Capability struct {
	Claim    string   `json:"claim"`
	Evidence []string `json:"evidence,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// EvidenceEntry is one citation. Ref is a repo-relative path that must exist —
// an evidence entry naming a file that is not there is the "cannot look"
// case, and Verify reports it rather than skipping the claim it supports.
type EvidenceEntry struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Date string `json:"date"`
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}
