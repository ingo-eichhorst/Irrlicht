// Package museaccountapi is issue #2007's provider ticket: the first real
// implementation on top of #2003's accountquota port
// (core/ports/outbound/accountquota.go) and transport
// (core/adapters/outbound/accountquota). It supplies Muse's
// outbound.CredentialResolver, its outbound.FixedDestination, and the
// redaction-boundary parser that turns a raw POST
// https://api.meta.ai/muse-code/key response into a
// core/domain/session.RateLimitSnapshot.
//
// This package deliberately does NOT reuse accountquota's own
// FileCredentialResolver/KeychainCredentialResolver: both wrap a PLAIN
// secret string, but Muse's credential is JSON-structured in both locations
// it can live — see CredentialResolver's doc comment. accountquota.go's own
// doc comment says a provider whose file carries structure "extracts the
// field itself and wraps the extracted string in outbound.NewCredential,
// never passing the raw file through" — this package follows that for BOTH
// its file route and its keychain route, and never unwraps a
// Credential's secret anywhere (grepped by
// TestCredentialResolver_NeverCallsReveal in this package's own tests): the
// port's Credential doc comment states its unwrap method has exactly one
// intended caller — accountquota's own transport, immediately before setting
// a request header — and this package only ever constructs a Credential, never
// unwraps one.
package museaccountapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	outbound "irrlicht/core/ports/outbound"
)

// KeychainService and KeychainAccount name the generic-password item Muse's
// own CLI writes for a `storage: "keychain"` login. Verified against the
// pinned herdr-agent-quota source (src/providers/muse.rs,
// read_keychain_access_token: `security find-generic-password -s
// ai.meta.dev.credentials -a meta -w`) — Research, not a local live probe,
// same caveat issue #2007's own §1.1 gives every herdr citation.
const (
	KeychainService = "ai.meta.dev.credentials"
	KeychainAccount = "meta"
)

// authFile is the subset of ~/.config/muse/auth.json this resolver reads.
// Verified structurally against THIS machine's real file on 2026-09-20 (key
// names and value TYPES only — os.WriteFile/ReadFile of the actual secret
// value never happened in that inspection, and no value crossed into any
// commit): the local file has `providers.meta.mechanism` = "oauth",
// `.storage` = "keychain", `.obtained_via`, `.api_base_url`,
// `.user_full_name`, `.user_email` — and NO `access_token` key at all,
// confirming the herdr source's own claim that a keychain-storage login
// keeps the token out of the file entirely.
type authFile struct {
	Providers struct {
		Meta struct {
			Mechanism   string `json:"mechanism"`
			Storage     string `json:"storage"`
			AccessToken string `json:"access_token"`
		} `json:"meta"`
	} `json:"providers"`
}

// keychainBlob is the JSON shape `security find-generic-password -w` prints
// for this item — not a bare secret string like accountquota's generic
// KeychainCredentialResolver assumes (verified against the herdr source,
// which parses the keychain command's stdout as JSON and reads
// `.access_token` from it).
type keychainBlob struct {
	AccessToken string `json:"access_token"`
}

// keychainRawLookup returns the RAW keychain item value (still JSON, not yet
// unwrapped) for service/account. platformKeychainRawLookup
// (credential_keychain_darwin.go / credential_keychain_other.go) is the real
// implementation; this package's own tests inject a fake so no test ever
// shells out to a real keychain.
type keychainRawLookup func(ctx context.Context, service, account string) (string, error)

// CredentialResolver implements outbound.CredentialResolver for Muse. It
// reads Path (the resolved ~/.config/muse/auth.json) to learn the login
// mechanism and storage location per #2007's high-level design ("file
// credential route first, Keychain only if proven necessary"): this
// machine's own auth.json makes that proof directly — storage: "keychain",
// no access_token field — so BOTH routes are implemented here, gated by
// what the file itself declares, rather than by speculation. See this
// package's doc comment for why it does not delegate to accountquota's
// generic resolvers.
type CredentialResolver struct {
	// Path resolves the absolute path to ~/.config/muse/auth.json (or
	// $MUSE_AUTH_PATH / $XDG_CONFIG_HOME/muse/auth.json — mirroring the
	// order the pinned herdr source's own auth_path() uses).
	Path func() (string, error)

	// lookup overrides the platform keychain lookup — set only by this
	// package's own tests.
	lookup keychainRawLookup
}

// NewCredentialResolver returns a resolver reading auth.json at the path
// path resolves.
func NewCredentialResolver(path func() (string, error)) CredentialResolver {
	return CredentialResolver{Path: path}
}

// Resolve implements outbound.CredentialResolver.
func (r CredentialResolver) Resolve(ctx context.Context) (outbound.Credential, error) {
	if r.Path == nil {
		return outbound.Credential{}, fmt.Errorf("museaccountapi: CredentialResolver has no Path resolver")
	}
	path, err := r.Path()
	if err != nil {
		return outbound.Credential{}, fmt.Errorf("museaccountapi: resolving auth.json path: %w", err)
	}
	// os.ReadFile's error embeds the path, never file content — safe to
	// wrap directly, mirroring accountquota.FileCredentialResolver's own
	// comment on this point.
	data, err := os.ReadFile(path)
	if err != nil {
		return outbound.Credential{}, fmt.Errorf("museaccountapi: reading auth.json: %w", err)
	}
	var auth authFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return outbound.Credential{}, fmt.Errorf("museaccountapi: auth.json is not valid JSON")
	}

	// Only an OAuth login has a subscription (herdr's own
	// credentials_from_auth: an api_key login bills the Model API instead
	// and has no account-quota windows to report) — mismatching this is a
	// credential-shape problem the resolver reports, not a transport one.
	if auth.Providers.Meta.Mechanism != "oauth" {
		return outbound.Credential{}, fmt.Errorf(
			"museaccountapi: %s has no oauth login for Muse (mechanism=%q)", path, auth.Providers.Meta.Mechanism)
	}

	if auth.Providers.Meta.Storage != "keychain" {
		token := strings.TrimSpace(auth.Providers.Meta.AccessToken)
		if token == "" {
			return outbound.Credential{}, fmt.Errorf("museaccountapi: %s has no access_token", path)
		}
		return outbound.NewCredential(token), nil
	}

	lookup := r.lookup
	if lookup == nil {
		lookup = platformKeychainRawLookup
	}
	raw, err := lookup(ctx, KeychainService, KeychainAccount)
	if err != nil {
		return outbound.Credential{}, fmt.Errorf("museaccountapi: keychain lookup: %w", err)
	}
	var blob keychainBlob
	if err := json.Unmarshal([]byte(raw), &blob); err != nil {
		return outbound.Credential{}, fmt.Errorf("museaccountapi: keychain item %s/%s is not the expected JSON shape", KeychainService, KeychainAccount)
	}
	token := strings.TrimSpace(blob.AccessToken)
	if token == "" {
		return outbound.Credential{}, fmt.Errorf("museaccountapi: keychain item %s/%s has no access_token", KeychainService, KeychainAccount)
	}
	return outbound.NewCredential(token), nil
}
