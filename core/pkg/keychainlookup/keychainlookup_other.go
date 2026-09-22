//go:build !darwin

package keychainlookup

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupported is returned by Raw on every non-darwin build — macOS
// Keychain access is implemented for darwin only, matching where irrlicht's
// daemon actually runs today.
var ErrUnsupported = errors.New("keychainlookup: macOS Keychain access is only implemented on darwin")

func Raw(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "", ErrUnsupported
}
