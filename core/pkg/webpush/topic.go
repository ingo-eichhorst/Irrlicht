package webpush

import (
	"crypto/sha256"
	"encoding/base64"
)

// TopicHeader maps a logical collapse key to a valid Topic header value.
// RFC 8030 §5.4 restricts Topic to 1–32 characters of the base64url
// alphabet; a key that already satisfies that passes through unchanged, and
// anything else becomes base64url(sha256(key)) truncated to 32 characters.
// Session UUIDs — the collapse key the notification policy uses, one
// notification per session (docs/mobile-notifications-arc42.md §8.4) — are
// 36 characters, so they always take the hash path. Deterministic on
// purpose: the same session must collapse to the same Topic across
// messages, daemons and relay restarts, or "newer replaces older" silently
// stops replacing.
func TopicHeader(key string) string {
	if isValidTopic(key) {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

// isValidTopic reports whether s is already a legal Topic header value:
// 1–32 bytes, every one from the base64url alphabet (A–Z a–z 0–9 - _).
// Byte-wise on purpose — any non-ASCII input fails the alphabet check and
// takes the hash path.
func isValidTopic(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
