package mistralvibe

import (
	"errors"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/outbound/chromecookies"
)

// cookiesWith writes values to a store and reads them back, selected by name, so
// BuildCredential's own validation can be driven with real Cookie values.
// chromecookies.Cookie has no exported constructor, which is the point: a
// value can only come out of a store.
func cookiesWith(t *testing.T, values map[string]string) []chromecookies.Cookie {
	t.Helper()
	store := filepath.Join(t.TempDir(), "Cookies")
	writePlainStore(t, store, values)
	b, _ := testBrowser(t, t.TempDir())
	sel := chromecookies.Selector{Hosts: []string{".mistral.ai"}}
	for name := range values {
		sel.Names = append(sel.Names, name)
	}
	got, err := chromecookies.Read(t.Context(), store, sel, fixtureNow, b.Key)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestBuildCredential_RefusesValuesThatCouldAddACookieOrHeader ports
// CodexBar's `rejects csrf values that could add cookies or headers`
// (MistralVibeUsageTests.swift) to every forwarded value: a ";" or ","
// would end the cookie and start another, CR/LF would start a header.
func TestBuildCredential_RefusesValuesThatCouldAddACookieOrHeader(t *testing.T) {
	for _, bad := range []string{"csrf; ory_session_secret=leak", "csrf\r\nX-Leak: value", "a,b", "has space", `quo"te`, `back\slash`} {
		for _, name := range []string{"csrftoken", "ory_session_x"} {
			values := map[string]string{"csrftoken": "ok", "ory_session_x": "ok"}
			values[name] = bad
			if _, err := BuildCredential(cookiesWith(t, values)); !errors.Is(err, ErrInvalidCookie) {
				t.Errorf("%s=%q: err = %v, want ErrInvalidCookie", name, bad, err)
			}
		}
	}
}

func TestBuildCredential_NeedsACSRFToken(t *testing.T) {
	if _, err := BuildCredential(cookiesWith(t, map[string]string{"ory_session_x": "s"})); !errors.Is(err, chromecookies.ErrCookieAbsent) {
		t.Fatalf("err = %v, want ErrCookieAbsent", err)
	}
}
