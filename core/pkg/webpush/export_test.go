package webpush

import "time"

// Encrypt exposes the deterministic encryption seam to the external test
// package: the RFC 8291 Appendix A known-answer test has to supply the
// example's fixed ephemeral key and salt, and the round-trip property test
// has to control every input for reproducibility. Send is the only
// production caller and always wraps it with fresh randomness.
var Encrypt = encrypt

// SetNow injects the clock behind the JWT exp claim.
func (s *Sender) SetNow(now func() time.Time) { s.now = now }

// EndpointOrigin exposes the aud derivation so its boundary cases (default
// ports, IPv6 hosts) can be pinned without a live server on those ports.
var EndpointOrigin = endpointOrigin

// DefaultClient exposes the nil-Client fallback so a test can pin its
// bounded timeout — the one property of it a Send-level test cannot reach
// without a deliberately stalled server.
var DefaultClient = defaultClient
