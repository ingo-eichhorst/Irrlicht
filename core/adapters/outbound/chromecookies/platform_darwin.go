//go:build darwin

package chromecookies

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"irrlicht/core/pkg/keychainlookup"
)

// Keychain item names, from components/os_crypt/common/keychain_password_mac.mm
// (kDefaultServiceName / kDefaultAccountName for a Google Chrome build).
const (
	safeStorageService = "Chrome Safe Storage"
	safeStorageAccount = "Chrome"
)

// keychainTimeout bounds one `security find-generic-password` run. Not a
// measured probe cost (docs/testing-contracts.md's "Probe costs in doc
// comments" convention): it is human time. The read can raise a macOS dialog
// the user has to read and answer (expected, because security is not on the
// item's access list; not yet observed for this item, which is the
// maintainer's live acceptance step). Muse's bound for the same kind of read is
// 15s (museaccountapi.keychainTimeout), against an answered dialog its own
// comment records at ~9.5s in one probe and 6s by hand.
// An unanswered dialog is killed at this bound and the failure is kept until
// the permission is granted again (KeyCache).
const keychainTimeout = 30 * time.Second

// UserDataDir is Google Chrome's user data directory under home
// (chrome_paths_mac.mm: ~/Library/Application Support/Google/Chrome).
func UserDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: resolving $HOME: %w", ErrProfileMissing, err)
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome"), nil
}

// SafeStorageKey reads the "Chrome Safe Storage" password from the login
// Keychain and derives the cookie key from it. Each call can raise a macOS
// dialog; callers go through a KeyCache.
func SafeStorageKey(ctx context.Context) (Key, error) {
	password, err := keychainlookup.Raw(ctx, safeStorageService, safeStorageAccount, keychainTimeout)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %w", ErrKeychain, err)
	}
	return DeriveKey(password)
}
