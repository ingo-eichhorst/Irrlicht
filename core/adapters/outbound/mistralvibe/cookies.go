package mistralvibe

import (
	"errors"
	"fmt"
	"strings"

	"irrlicht/core/adapters/outbound/chromecookies"
	outbound "irrlicht/core/ports/outbound"
)

// CookieSelector is the whole of what this route reads from the browser:
// csrftoken and every ory_session_* cookie, on console.mistral.ai (host-only)
// or .mistral.ai (domain). It is CodexBar's allowlist
// (MistralUsageFetcher.swift:174-185) narrowed to the triage's two hosts; a
// .console.mistral.ai domain cookie, which a browser would also attach, is not
// selected. chromecookies.Read selects these rows in SQL and nothing else, so
// this is the one place the selection is decided: BuildCredential does not
// filter again, which is what lets tools/lib/mistralvibe-mutations_test.sh
// prove the selection by widening it.
var CookieSelector = chromecookies.Selector{
	Hosts:        []string{"console.mistral.ai", ".mistral.ai"},
	Names:        []string{csrfCookie},
	NamePrefixes: []string{"ory_session_"},
}

const csrfCookie = "csrftoken"

// ErrInvalidCookie: a selected cookie's name or value contains a byte that
// could end the cookie early and smuggle another one, or a header, into the
// request (RFC 6265 §4.1.1 cookie-octet and token rules). CodexBar refuses
// the same for the CSRF value (validatedVibeCSRFToken: ";", ",", CR, LF).
var ErrInvalidCookie = errors.New("mistralvibe: a selected cookie has a name or value that cannot be sent safely")

// BuildCredential turns the selected cookies into the request credential:
// a Cookie header holding exactly those cookies, in the order given (RFC 6265
// header order, chromecookies.Read), plus X-CSRFToken carrying the first
// csrftoken's value. It adds no cookie and drops none: what it is given is
// what is sent.
func BuildCredential(cookies []chromecookies.Cookie) (outbound.Credential, error) {
	var header strings.Builder
	csrf := ""
	for _, c := range cookies {
		if !isToken(c.Name) || !isCookieValue(c.Value()) {
			return outbound.Credential{}, fmt.Errorf("%w: cookie %q", ErrInvalidCookie, c.Name)
		}
		if header.Len() > 0 {
			header.WriteString("; ")
		}
		header.WriteString(c.Name)
		header.WriteByte('=')
		header.WriteString(c.Value())
		if c.Name == csrfCookie && csrf == "" {
			csrf = c.Value()
		}
	}
	if csrf == "" {
		return outbound.Credential{}, fmt.Errorf("%w: no %s", chromecookies.ErrCookieAbsent, csrfCookie)
	}
	return outbound.NewCredentialWithCompanion(header.String(), csrf), nil
}

// Bytes a cookie name (an RFC 7230 token) or value (RFC 6265 cookie-octets)
// may not contain, beyond controls, space and non-ASCII.
const (
	tokenSeparators = `()<>@,;:\"/[]?={}`
	valueForbidden  = `",;\`
)

func isToken(s string) bool       { return onlyAllowed(s, tokenSeparators) }
func isCookieValue(s string) bool { return onlyAllowed(s, valueForbidden) }

// onlyAllowed reports whether s is non-empty printable ASCII with no space
// and none of the bytes in forbidden.
func onlyAllowed(s, forbidden string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r >= 0x7f || strings.ContainsRune(forbidden, r)
	})
}
