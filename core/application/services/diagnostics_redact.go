package services

import (
	"regexp"
	"strings"
)

// tokenPatterns mask credential-shaped strings before they reach the
// diagnostics bundle (#736). The set is deliberately allow-listed to the three
// families the issue calls out — it is not an exhaustive secret scanner, and
// novel shapes can slip through. Each match is replaced wholesale with the
// REDACTED marker.
var tokenPatterns = []*regexp.Regexp{
	// OpenAI / Anthropic API keys: sk-…, sk-proj-…, sk-ant-… . The 16+ tail
	// avoids masking a bare "sk-" that happens to start a word.
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	// GitHub tokens: ghp_ (PAT), gho_ (OAuth), ghu_/ghs_ (app), ghr_ (refresh).
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
}

// bearerPattern masks the credential after a "Bearer " scheme while keeping the
// scheme word visible, so a reader can still see an Authorization header was
// present. Case-insensitive on the scheme; the token charset covers JWTs.
var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._-]+`)

// redactedMarker is what every masked secret collapses to.
const redactedMarker = "[REDACTED]"

// Redactor scrubs diagnostics content server-side: it rewrites the user's home
// directory to "~" and masks token-shaped strings. Construct one per bundle so
// the home prefix is captured once. The zero value is unusable — use
// NewRedactor.
type Redactor struct {
	home string
}

// NewRedactor returns a Redactor that rewrites home → "~". A home of "" or "/"
// disables path rewriting (token masking still applies).
func NewRedactor(home string) *Redactor {
	if home == "/" {
		home = ""
	}
	return &Redactor{home: strings.TrimRight(home, "/")}
}

// String scrubs a single string: home-path rewrite first, then token masking.
func (r *Redactor) String(s string) string {
	if r.home != "" {
		s = strings.ReplaceAll(s, r.home, "~")
	}
	for _, re := range tokenPatterns {
		s = re.ReplaceAllString(s, redactedMarker)
	}
	s = bearerPattern.ReplaceAllString(s, "${1}"+redactedMarker)
	return s
}

// Bytes scrubs a byte slice (logs, marshaled JSON) and returns a new slice.
func (r *Redactor) Bytes(b []byte) []byte {
	return []byte(r.String(string(b)))
}

// Argv scrubs every element of an argument vector, returning a new slice. A nil
// argv returns nil so callers can distinguish "unreadable" from "empty".
func (r *Redactor) Argv(argv []string) []string {
	if argv == nil {
		return nil
	}
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = r.String(a)
	}
	return out
}
