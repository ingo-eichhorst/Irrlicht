package mistralvibe

import (
	"context"
	"time"

	"irrlicht/core/adapters/outbound/chromecookies"
	outbound "irrlicht/core/ports/outbound"
)

// Browser reads the Mistral session out of the Chrome profile Chrome last
// used. It is the browser-profile half of this route; the daemon builds one
// and calls Session once per sweep tick.
type Browser struct {
	// UserDataDir resolves Chrome's user data directory
	// (chromecookies.UserDataDir in production, a temp dir in tests).
	UserDataDir func() (string, error)
	// Key returns the cookie key. The daemon passes its
	// chromecookies.KeyCache.Key, so the Keychain is read once per grant.
	Key func(context.Context) (chromecookies.Key, error)
	// Now is the clock cookie expiry is judged by; nil means time.Now.
	Now func() time.Time
}

// Session resolves which Chrome profile is in use. It reads Chrome's Local
// State file and nothing else: no cookie, and never the Keychain.
func (b Browser) Session() (Session, error) {
	dir, err := b.UserDataDir()
	if err != nil {
		return Session{}, err
	}
	p, err := chromecookies.LastUsedProfile(dir)
	if err != nil {
		return Session{}, err
	}
	return Session{browser: b, profile: p}, nil
}

// Session is one Chrome profile's Mistral session. It is the
// outbound.CredentialResolver the poller resolves when, and only when, it
// actually fetches.
type Session struct {
	browser Browser
	profile chromecookies.Profile
}

// Account is the account context a reading belongs to: the Chrome profile,
// by directory. It is the only identity this route has. The response names
// no account (issue #2012's live probe), and the binding to the vibe CLI's
// account is the user's assertion when they grant the permission, so two
// profiles are two contexts that never share a reading, and one profile is
// one context however its cookies rotate. A cookie value is never part of
// it: TestSession_AccountIsTheProfileNeverACookie rotates every cookie and
// requires the same answer, without the Keychain being read.
func (s Session) Account() string {
	return "chrome-profile:" + s.profile.Dir
}

// Resolve implements outbound.CredentialResolver: it reads the selected
// cookies from this profile and builds the request credential from them.
func (s Session) Resolve(ctx context.Context) (outbound.Credential, error) {
	cookies, err := chromecookies.Read(ctx, s.profile.CookiesPath(), CookieSelector, s.now(), s.browser.Key)
	if err != nil {
		return outbound.Credential{}, err
	}
	return BuildCredential(cookies)
}

func (s Session) now() time.Time {
	if s.browser.Now != nil {
		return s.browser.Now()
	}
	return time.Now()
}
