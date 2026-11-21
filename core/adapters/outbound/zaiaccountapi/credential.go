package zaiaccountapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// The settings-file env keys a credential read may return. The base URL is
// re-read so the key is used only while the file that holds it still routes
// to Z.ai.
const (
	keyBaseURL   = "ANTHROPIC_BASE_URL"
	keyAuthToken = "ANTHROPIC_AUTH_TOKEN"
	keyAPIKey    = "ANTHROPIC_API_KEY"
)

var credentialKeys = map[string]struct{}{keyBaseURL: {}, keyAuthToken: {}, keyAPIKey: {}}

// EnvFileReader returns the named env keys from a Claude Code settings file
// — claudesettings.ReadEnv, injected by the daemon's wiring so this outbound
// package does not import an inbound one.
type EnvFileReader func(path string, keys map[string]struct{}) (map[string]string, error)

// Reasons a CredentialUnavailableError carries.
const (
	// ReasonRouteNotFromSettings: the route came from the process
	// environment. A key that exists only there is a non-goal (#2010
	// triage): it is never read, so no quota is shown rather than a quota
	// from some other key.
	ReasonRouteNotFromSettings = "route-not-from-settings"
	// ReasonSettingsUnreadable: the route's settings file is gone or does
	// not parse any more.
	ReasonSettingsUnreadable = "settings-file-unreadable"
	// ReasonSettingsNotZai: the route's settings file no longer names
	// api.z.ai, so its key may belong to another provider now.
	ReasonSettingsNotZai = "settings-file-no-longer-routes-to-zai"
	// ReasonNoKey: the file sets neither ANTHROPIC_AUTH_TOKEN nor
	// ANTHROPIC_API_KEY.
	ReasonNoKey = "no-key-in-settings-file"
)

// CredentialUnavailableError is the classified "credential unavailable"
// result a Z.ai-routed session gets when no key can be read for it.
type CredentialUnavailableError struct{ Reason string }

func (e *CredentialUnavailableError) Error() string {
	return "zaiaccountapi: credential unavailable: " + e.Reason
}

// Credential is a resolved key and the fingerprint the poll is keyed by.
type Credential struct {
	Key outbound.Credential
	// Fingerprint identifies the key within this daemon's lifetime only:
	// see Fingerprinter. It is credential identity, not account identity.
	Fingerprint string
}

// Fingerprinter derives a key's poll identity: HMAC-SHA256 under a random
// salt drawn once per daemon. The salt is never persisted or logged, so a
// fingerprint cannot be matched against a key outside this process, and the
// key itself is never retained by it.
type Fingerprinter struct{ salt []byte }

// NewFingerprinter draws a fresh 32-byte salt.
func NewFingerprinter() (*Fingerprinter, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("zaiaccountapi: drawing the fingerprint salt: %w", err)
	}
	return &Fingerprinter{salt: salt}, nil
}

func (f *Fingerprinter) fingerprint(key string) string {
	mac := hmac.New(sha256.New, f.salt)
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// CredentialReader reads the key for a Z.ai-routed session.
type CredentialReader struct {
	read EnvFileReader
	fp   *Fingerprinter
}

// NewCredentialReader builds a reader over read and fp.
func NewCredentialReader(read EnvFileReader, fp *Fingerprinter) *CredentialReader {
	return &CredentialReader{read: read, fp: fp}
}

// Read returns the key from the settings file route names
// (session.RouteObservation.SettingsFile) and from nowhere else:
// env.ANTHROPIC_AUTH_TOKEN, falling back to env.ANTHROPIC_API_KEY, and only
// while that file's own ANTHROPIC_BASE_URL still names api.z.ai. The caller
// checks the permission before calling.
//
// Not reconstructed: a higher-precedence settings layer that sets a token
// without a base URL would be the token Claude Code sends, and this reads
// the route file's instead. No probe has exercised that layout.
func (r *CredentialReader) Read(route *session.RouteObservation) (Credential, error) {
	if route == nil || route.SettingsFile == "" {
		return Credential{}, &CredentialUnavailableError{Reason: ReasonRouteNotFromSettings}
	}
	env, err := r.read(route.SettingsFile, credentialKeys)
	if err != nil {
		return Credential{}, &CredentialUnavailableError{Reason: ReasonSettingsUnreadable}
	}
	if !IsZaiRoute(env[keyBaseURL]) {
		return Credential{}, &CredentialUnavailableError{Reason: ReasonSettingsNotZai}
	}
	key := env[keyAuthToken]
	if key == "" {
		key = env[keyAPIKey]
	}
	if key == "" {
		return Credential{}, &CredentialUnavailableError{Reason: ReasonNoKey}
	}
	return Credential{Key: outbound.NewCredential(key), Fingerprint: r.fp.fingerprint(key)}, nil
}
