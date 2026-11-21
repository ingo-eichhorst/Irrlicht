//go:build !darwin

package chromecookies

import "context"

// UserDataDir is not implemented off macOS: issue #2012 supports Google
// Chrome on macOS only, and says so rather than guessing another platform's
// layout.
func UserDataDir() (string, error) { return "", ErrNotImplemented }

// SafeStorageKey is not implemented off macOS (see UserDataDir).
func SafeStorageKey(context.Context) (Key, error) { return Key{}, ErrNotImplemented }
