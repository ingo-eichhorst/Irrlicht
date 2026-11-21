// Package chromecookies reads a few named cookies out of one Google Chrome
// profile on macOS (issue #2012). It is the browser-profile credential
// resolver of epic #1977 §4.3. It is not a transport and sends nothing:
// mistralvibe turns what it returns into an outbound.Credential, and only
// core/adapters/outbound/accountquota ever puts that on the wire.
//
// Supported: Google Chrome, macOS. Every other browser and platform is out of
// scope. UserDataDir and SafeStorageKey return ErrNotImplemented on any other
// GOOS, so a caller fails before it reads anything.
//
// The on-disk format below was read from Chromium source at
// 6d34471112e9339f8b2b6f8a9566e46d2142b75c (fetched 2026-11-21 with
// `gh api repos/chromium/chromium/contents/<path>?ref=<sha>`). It is not
// verified against a real Chrome profile on this machine: issue #2012's
// hard constraint forbade that read, so it is the maintainer's live
// acceptance step. The files read:
//
//   - chrome/common/chrome_paths_mac.mm: the user data dir is
//     ~/Library/Application Support/Google/Chrome.
//   - chrome/common/chrome_constants.h: "Local State", "Cookies" and
//     kInitialProfile = "Default".
//   - chrome/browser/profiles/profiles_state.cc, GetLastUsedProfileBaseName:
//     the last-used profile is Local State's profile.last_used, falling back
//     to "Default" when that is empty or names "System Profile".
//   - chrome/browser/net/profile_network_context_service.cc and
//     content/public/common/content_features.cc: the cookie file moves into
//     a Network/ subdirectory only under kTriggerNetworkDataMigration, which
//     is enabled by default on Windows alone. So on macOS it is
//     <profile>/Cookies.
//   - net/extras/sqlite/sqlite_persistent_cookie_store.cc: the cookies
//     schema, and that from meta version 24 a SHA-256 of host_key is
//     prepended to the plaintext before encryption.
//   - components/os_crypt/async/browser/keychain_key_provider.mm and
//     components/os_crypt/async/common/encryptor.cc: the "v10" prefix, and
//     PBKDF2-HMAC-SHA1 with salt "saltysalt", 1003 iterations, a 16-byte
//     key, AES-128-CBC and an IV of sixteen spaces.
//   - components/os_crypt/common/keychain_password_mac.mm: the Keychain
//     item is service "Chrome Safe Storage", account "Chrome".
//   - sql/statement.cc, ColumnTime: expires_utc counts microseconds since
//     1601-01-01 UTC.
package chromecookies

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Classified failures. Every error this package returns wraps exactly one of
// them, so a caller can say what went wrong without parsing text, and none
// of them ever carries a cookie value or key material.
var (
	// ErrNotImplemented: this GOOS has no Chrome reader (only darwin does).
	ErrNotImplemented = errors.New("chromecookies: only Google Chrome on macOS is supported")
	// ErrProfileMissing: Chrome's Local State or the profile directory it
	// names is absent or unreadable.
	ErrProfileMissing = errors.New("chromecookies: Chrome profile not found")
	// ErrStoreMissing: the profile has no Cookies database.
	ErrStoreMissing = errors.New("chromecookies: Chrome cookie database not found")
	// ErrStoreLocked: the Cookies database could not be read because another
	// process held a lock on it past sqlitero's 500ms busy timeout. Read at the
	// revision above, Chromium opens the cookie store with
	// set_exclusive_locking(false) (net/extras/sqlite/
	// sqlite_persistent_store_backend_base.cc), and its exclusive file lock is
	// Windows-only (sql/database.h), so a running Chrome on macOS is expected
	// to block a reader only during a write. Not observed on a real profile;
	// it is reported on its own so the live acceptance can tell it apart.
	ErrStoreLocked = errors.New("chromecookies: Chrome cookie database is locked")
	// ErrStoreUnreadable: the database opened but could not be queried
	// (corrupt, or a schema this package does not know).
	ErrStoreUnreadable = errors.New("chromecookies: Chrome cookie database unreadable")
	// ErrCookieAbsent: a cookie the Selector requires is missing or expired.
	// This is what a signed-out browser produces, and it is decided BEFORE
	// the Keychain is read.
	ErrCookieAbsent = errors.New("chromecookies: required cookie absent or expired")
	// ErrUnknownEncryption: a cookie value is not in a format this package
	// can decrypt and check: no "v10" prefix, bad padding, a host hash that
	// does not match, or a value stored both encrypted and in plaintext.
	ErrUnknownEncryption = errors.New("chromecookies: unknown cookie encryption")
	// ErrKeychain: reading the "Chrome Safe Storage" Keychain item failed:
	// denied, unanswered, or absent.
	ErrKeychain = errors.New("chromecookies: Chrome Safe Storage keychain read failed")
)

const (
	localStateFile = "Local State"
	cookiesFile    = "Cookies"
	initialProfile = "Default"
	systemProfile  = "System Profile"
)

// Profile is one Chrome profile directory.
type Profile struct {
	// Name is the directory's base name, e.g. "Default" or "Profile 1".
	Name string
	// Dir is the absolute directory.
	Dir string
}

// CookiesPath is the profile's cookie database.
func (p Profile) CookiesPath() string { return filepath.Join(p.Dir, cookiesFile) }

// localState is the one field this package reads from Local State.
type localState struct {
	Profile struct {
		LastUsed string `json:"last_used"`
	} `json:"profile"`
}

// LastUsedProfile resolves the profile Chrome itself would open: Local
// State's profile.last_used under userDataDir, or "Default" when that is
// empty or names the system profile (Chromium's GetLastUsedProfileBaseName).
// A name that is not a single directory entry is refused, so a tampered
// Local State cannot point this package outside userDataDir.
func LastUsedProfile(userDataDir string) (Profile, error) {
	data, err := os.ReadFile(filepath.Join(userDataDir, localStateFile))
	if err != nil {
		return Profile{}, fmt.Errorf("%w: reading Local State: %w", ErrProfileMissing, err)
	}
	var ls localState
	if err := json.Unmarshal(data, &ls); err != nil {
		return Profile{}, fmt.Errorf("%w: Local State is not valid JSON", ErrProfileMissing)
	}
	name := ls.Profile.LastUsed
	if name == "" || name == systemProfile {
		name = initialProfile
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return Profile{}, fmt.Errorf("%w: Local State names %q, which is not a profile directory", ErrProfileMissing, name)
	}
	dir := filepath.Join(userDataDir, name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return Profile{}, fmt.Errorf("%w: profile directory %q", ErrProfileMissing, name)
	}
	return Profile{Name: name, Dir: dir}, nil
}
