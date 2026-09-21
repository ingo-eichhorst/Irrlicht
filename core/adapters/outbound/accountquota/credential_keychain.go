package accountquota

import (
	"context"
	"fmt"

	outbound "irrlicht/core/ports/outbound"
)

// keychainLookup is the seam KeychainCredentialResolver.Resolve calls
// through. Darwin and non-darwin builds each provide a distinct
// platformKeychainLookup (credential_keychain_darwin.go,
// credential_keychain_other.go), so this file carries no //go:build tag and
// this package's tests can drive either shape via an injected lookup without
// needing a build-tagged test file of their own.
type keychainLookup func(ctx context.Context, service, account string) (string, error)

// KeychainCredentialResolver resolves a credential from the OS keychain's
// generic-password store. It is the second of the two credential locations
// issue #2003 §1.3 requires kept separate from the transport — a file
// resolver and a keychain resolver can both feed the same provider's
// transport unchanged. On any OS other than darwin, Resolve always fails with
// ErrKeychainUnsupported (credential_keychain_other.go) rather than silently
// returning no credential.
type KeychainCredentialResolver struct {
	// Service and Account identify the generic-password item, matching how
	// `security add-generic-password -s <Service> -a <Account>` would have
	// stored it.
	Service string
	Account string

	// lookup overrides the platform default — set only by this package's own
	// tests, never by a provider ticket, so a test never has to shell out to
	// a real keychain (which would touch the developer's actual login
	// keychain).
	lookup keychainLookup
}

// NewKeychainCredentialResolver returns a resolver for the given
// service/account pair, using the platform's real keychain lookup.
func NewKeychainCredentialResolver(service, account string) KeychainCredentialResolver {
	return KeychainCredentialResolver{Service: service, Account: account}
}

// Resolve implements outbound.CredentialResolver.
func (r KeychainCredentialResolver) Resolve(ctx context.Context) (outbound.Credential, error) {
	if r.Service == "" || r.Account == "" {
		return outbound.Credential{}, fmt.Errorf("accountquota: KeychainCredentialResolver needs both Service and Account")
	}
	lookup := r.lookup
	if lookup == nil {
		lookup = platformKeychainLookup
	}
	secret, err := lookup(ctx, r.Service, r.Account)
	if err != nil {
		return outbound.Credential{}, err
	}
	if secret == "" {
		return outbound.Credential{}, fmt.Errorf("accountquota: keychain item %s/%s resolved to an empty secret", r.Service, r.Account)
	}
	return outbound.NewCredential(secret), nil
}
