//go:build !darwin

package accountquota

import (
	"context"
	"errors"
)

// ErrKeychainUnsupported is returned by every KeychainCredentialResolver on a
// non-darwin build. #2003 ships a real keychain resolver for macOS only,
// matching where irrlicht's daemon actually runs today; a provider ticket
// that needs one elsewhere names this error rather than getting a silently
// empty credential.
var ErrKeychainUnsupported = errors.New("accountquota: keychain credential resolution is only implemented on darwin")

func platformKeychainLookup(_ context.Context, _, _ string) (string, error) {
	return "", ErrKeychainUnsupported
}
