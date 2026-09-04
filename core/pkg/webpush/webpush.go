// Package webpush delivers Web Push messages end to end: it encrypts a
// payload to a browser subscription's keys (RFC 8291, using the aes128gcm
// content coding of RFC 8188), signs a VAPID JWT identifying this server to
// the push service (RFC 8292), and POSTs the result to the subscription's
// endpoint (RFC 8030). It is the delivery block of
// docs/mobile-notifications-arc42.md §5.1 — the relay's dispatcher decides
// what to send and to whom, and this package only gets it there. Policy
// (whether, when, how coalesced) lives in core/domain/notify, never here.
//
// The package is stdlib-only on purpose. It sits in the pkg/ leaf layer,
// where core/architecture_test.go already forbids adapters/ and application/
// imports, and carrying no third-party dependencies on top of that is what
// keeps it extractable to its own repository later.
//
// Encryption is not an option here and not a transport detail: RFC 8291 is
// mandatory for payload-bearing pushes, and it is what makes Apple's and
// Google's push services content-blind by construction
// (docs/mobile-notifications-arc42.md constraint C7). What those services do
// still see is the ciphertext LENGTH, so every record is padded to a fixed
// size before sealing — see DefaultPadTo and the §8.2 privacy table.
package webpush

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Subscription matches the browser's PushSubscription.toJSON() shape, so the
// relay can unmarshal a client's registration POST body into it directly.
type Subscription struct {
	Endpoint string           `json:"endpoint"`
	Keys     SubscriptionKeys `json:"keys"`
}

// SubscriptionKeys carries the two per-subscription values the browser minted
// (RFC 8291 §2): the user agent's P-256 ECDH public key as a 65-byte
// uncompressed point, and the 16-byte authentication secret. Both are
// base64url. Browsers emit the unpadded form, but padded input is accepted
// too: these strings arrive in an untrusted client POST, and push libraries
// disagree about padding while agreeing about everything else.
type SubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// ErrSubscriptionGone reports a push endpoint that no longer exists (HTTP 404
// or 410 from the push service). The caller prunes the subscription instead
// of retrying — the phone re-subscribes on its next open with its stored
// device token, and the gap is surfaced in the PWA health panel
// (docs/mobile-notifications-arc42.md §8.3), so a dead endpoint must never
// look like a transient delivery failure.
var ErrSubscriptionGone = errors.New("webpush: subscription gone")

// ErrPayloadTooLarge reports a payload that does not fit the padded record:
// the fixed record size (Options.PadTo) must hold the payload plus the
// one-byte RFC 8188 delimiter. Growing the record instead would leak payload
// size to the push service, which is the exact thing the fixed size exists to
// prevent (docs/mobile-notifications-arc42.md §8.2).
var ErrPayloadTooLarge = errors.New("webpush: payload exceeds padded record size")

// StatusError is any non-2xx push service answer other than the 404/410 pair
// that maps to ErrSubscriptionGone. The body is kept because push services
// put the actionable detail there (RFC 8030 §8.2's problem+json), and
// truncated because it is destined for a log line, not for parsing.
type StatusError struct {
	Code int
	Body string // response body, truncated to 256 bytes
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("webpush: push service answered %d: %s", e.Code, e.Body)
}

// decode validates and decodes both subscription keys. The error names the
// offending field: these values come from an untrusted client POST, and
// "invalid point" without a field name sends the operator diffing two opaque
// base64 blobs.
func (k SubscriptionKeys) decode() (uaPublic, auth []byte, err error) {
	uaPublic, err = decodeB64("subscription p256dh", k.P256dh)
	if err != nil {
		return nil, nil, err
	}
	// NewPublicKey is the arbiter of validity: it rejects anything that is
	// not a 65-byte uncompressed point on P-256, including the compressed
	// encodings some libraries produce and off-curve garbage.
	if _, err := ecdh.P256().NewPublicKey(uaPublic); err != nil {
		return nil, nil, fmt.Errorf("webpush: subscription p256dh is not a 65-byte uncompressed P-256 point (got %d bytes): %w", len(uaPublic), err)
	}
	auth, err = decodeB64("subscription auth", k.Auth)
	if err != nil {
		return nil, nil, err
	}
	if len(auth) != authSecretLen {
		return nil, nil, fmt.Errorf("webpush: subscription auth secret must be %d bytes, got %d", authSecretLen, len(auth))
	}
	return uaPublic, auth, nil
}

// decodeB64 accepts both padded and unpadded base64url. Standard base64
// ('+', '/') stays an error: the Web Push wire format is base64url
// end to end, and silently accepting the other alphabet would let a
// subscription work here and fail against every other Web Push library.
func decodeB64(field, s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("webpush: %s: invalid base64url: %w", field, err)
	}
	return b, nil
}
