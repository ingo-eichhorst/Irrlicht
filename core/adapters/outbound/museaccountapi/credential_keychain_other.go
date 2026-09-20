//go:build !darwin

package museaccountapi

import (
	"context"
	"errors"
)

// ErrKeychainUnsupported mirrors accountquota.ErrKeychainUnsupported: Muse's
// keychain route is implemented for macOS only, matching where irrlicht's
// daemon actually runs today.
var ErrKeychainUnsupported = errors.New("museaccountapi: keychain credential resolution is only implemented on darwin")

func platformKeychainRawLookup(_ context.Context, _, _ string) (string, error) {
	return "", ErrKeychainUnsupported
}
