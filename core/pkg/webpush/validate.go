package webpush

import (
	"fmt"
	"net/url"
)

// Validate reports whether the subscription is deliverable as posted: an
// absolute http(s) endpoint with a host, and browser-minted keys that decode
// (a 65-byte uncompressed P-256 point, a 16-byte auth secret). The relay's
// registry validates a registration at the door with this, so a malformed
// PushSubscription is a 400 naming the offending field instead of a stored
// dud whose failure surfaces only at dispatch time, when the phone has long
// stopped looking (docs/mobile-notifications-arc42.md §8.3). It runs the
// same key decode Send performs, so "accepted here" and "sendable later"
// cannot drift apart.
func (s Subscription) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return fmt.Errorf("webpush: subscription endpoint: %w", err)
	}
	// http stays legal alongside https: the dev loop subscribes against the
	// daemon on 127.0.0.1, a secure context without TLS
	// (docs/mobile-notifications-arc42.md §7).
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webpush: subscription endpoint must be an absolute http(s) URL, got %q", s.Endpoint)
	}
	if u.Host == "" {
		return fmt.Errorf("webpush: subscription endpoint %q has no host", s.Endpoint)
	}
	_, _, err = s.Keys.decode()
	return err
}
