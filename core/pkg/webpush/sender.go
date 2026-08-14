package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Urgency is the RFC 8030 §5.3 Urgency header value: it lets the push
// service defer low-priority messages on battery-constrained devices. Only
// the two levels the relay's notification policy distinguishes are named
// here; the header admits "very-low" and "low" too, which nothing in
// docs/mobile-notifications-arc42.md §8.4 emits.
type Urgency string

const (
	UrgencyHigh   Urgency = "high"
	UrgencyNormal Urgency = "normal"
)

// Options are the per-message delivery knobs. The zero value is a sane
// default: TTL 0 (deliver now or drop — valid per RFC 8030 §5.2), no Topic,
// no Urgency, DefaultPadTo padding.
type Options struct {
	// TTL bounds how long the push service may hold an undelivered
	// message. Rounded UP to whole seconds — rounding down would turn a
	// sub-second TTL into 0, "deliver immediately or not at all", which
	// is stricter than anything the caller asked for. The TTL header is
	// always sent: RFC 8030 §5.2 makes it mandatory, and 0 is a valid
	// value, not an absence.
	TTL time.Duration
	// Topic is the logical collapse key ("newer replaces older" at the
	// push service, RFC 8030 §5.4). Mapped through TopicHeader, so any
	// string is safe here — session UUIDs included. Empty sends no Topic
	// header.
	Topic string
	// Urgency is sent verbatim when non-empty.
	Urgency Urgency
	// PadTo overrides the fixed plaintext record size (payload +
	// delimiter + zero padding). 0 means DefaultPadTo. Every message a
	// relay sends should use one size — a per-message size is the length
	// leak the padding exists to close (arc42 §8.2).
	PadTo int
}

// Sender pushes messages on behalf of one VAPID identity.
type Sender struct {
	Key     *VAPIDKey
	Subject string       // VAPID sub claim ("mailto:…" or "https://…"); "" omits it
	Client  *http.Client // nil = a shared default with a 10s timeout

	// now is the clock behind the JWT exp claim, injectable in tests;
	// nil means time.Now.
	now func() time.Time
}

// defaultClient bounds every request a Sender without its own Client makes.
// A push POST that hangs must not wedge the relay's dispatch loop, and 10s
// is far beyond any healthy push service's response time.
var defaultClient = &http.Client{Timeout: 10 * time.Second}

// Send encrypts payload to sub (RFC 8291), signs a VAPID JWT for the
// endpoint's origin (RFC 8292), and POSTs (RFC 8030). nil on any 2xx.
// 404 and 410 wrap ErrSubscriptionGone — the caller prunes the
// subscription. Any other non-2xx is a *StatusError.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opts Options) error {
	if s.Key == nil {
		return errors.New("webpush: Sender has no VAPID key")
	}
	uaPublic, auth, err := sub.Keys.decode()
	if err != nil {
		return err
	}
	origin, err := endpointOrigin(sub.Endpoint)
	if err != nil {
		return err
	}

	padTo := opts.PadTo
	if padTo == 0 {
		padTo = DefaultPadTo
	}
	// Fresh ephemeral key and salt per message (RFC 8291 §3.1, RFC 8188
	// §2.1): reusing either across messages would derive the same
	// key/nonce pair for different plaintexts, which is fatal for GCM.
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("webpush: ephemeral key: %w", err)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("webpush: salt: %w", err)
	}
	body, err := encrypt(ephemeral, salt, uaPublic, auth, payload, padTo)
	if err != nil {
		return err
	}

	jwt, err := s.Key.signJWT(origin, s.Subject, s.timeNow())
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webpush: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.FormatInt(ttlSeconds(opts.TTL), 10))
	if opts.Topic != "" {
		req.Header.Set("Topic", TopicHeader(opts.Topic))
	}
	if opts.Urgency != "" {
		req.Header.Set("Urgency", string(opts.Urgency))
	}
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.Key.PublicKey())

	client := s.Client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// The error already names the endpoint's host; wrapping keeps
		// context.Canceled / DeadlineExceeded reachable via errors.Is.
		return fmt.Errorf("webpush: POST: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// Drain (bounded) so the connection is reusable.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		// RFC 8030 §7.3: 410 means the subscription no longer exists;
		// services answer 404 for the same condition in practice. The
		// error names the origin, never the full endpoint — the path is
		// the subscription's capability secret (RFC 8030 §8.3) and this
		// string is headed for a log.
		return fmt.Errorf("webpush: %s answered %d: %w", origin, resp.StatusCode, ErrSubscriptionGone)
	default:
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return &StatusError{Code: resp.StatusCode, Body: string(detail)}
	}
}

func (s *Sender) timeNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ttlSeconds rounds up to whole seconds and clamps negatives to 0. See
// Options.TTL for why the rounding direction matters.
func ttlSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Second - 1) / time.Second)
}

// endpointOrigin reduces the push endpoint URL to its origin — the aud value
// RFC 8292 §2 prescribes (scheme://host[:port], never the full URL; see
// signJWT for why the path must not appear).
func endpointOrigin(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("webpush: endpoint URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("webpush: endpoint URL %q has no scheme or host", endpoint)
	}
	host := u.Host
	// RFC 6454 §6.2's origin serialization omits a scheme-default port, so
	// an endpoint spelled with an explicit :443 (a proxy or hand-normalized
	// URL — browsers never emit one) must not yield an aud a strictly
	// validating push service would reject. Hostname() strips IPv6
	// brackets; put them back.
	if p := u.Port(); (u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80") {
		host = u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return u.Scheme + "://" + host, nil
}
