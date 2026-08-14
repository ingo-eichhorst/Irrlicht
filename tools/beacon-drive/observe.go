package main

// The observer: a device, paired the way a phone pairs
// (docs/mobile-notifications-arc42.md §6.1), whose push endpoint is a
// listener in this process. Everything the relay decides for this run
// therefore arrives here as a real RFC 8291 push — encrypted to keys minted
// here, signed with the relay's VAPID identity, POSTed by the same
// webpush.Sender that talks to Apple.
//
// It exists because the rest of the run is otherwise unfalsifiable. The
// pushes a scenario provokes land on a phone that may be in another room, and
// on a relay with nothing paired they land nowhere at all — and "the policy
// engine decided nothing" is indistinguishable from "everything worked" when
// no one is looking. That is the failure mode tools/beacon-rig.sh's ground
// rule names: a check that cannot run must fail loudly rather than skip.
//
// A real phone paired to the same relay still gets every one of these pushes.
// The observer is an additional subscription, not a replacement for the
// device half of docs/beacon-device-test.md.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/notify"
)

const (
	// observerLabel names the device token in `irrlichtrelay token list`, so
	// an operator reading that list can tell this driver's pairing from a
	// phone's at a glance.
	observerLabel = "beacon-drive observer"

	// pushBodyLimit bounds a push body we accept. The relay pads every
	// record to webpush.DefaultPadTo (2 KiB) plus a 86-byte header and a GCM
	// tag, so anything near this is not one of ours.
	pushBodyLimit = 8 << 10

	// httpTimeout bounds each call to the relay's REST surface. All of them
	// are local and immediate; a hang is a failure, not something to wait out.
	httpTimeout = 10 * time.Second

	// overshootWindow is how long await keeps listening after the pushes it
	// was waiting for have arrived. Without it a scenario expecting one push
	// and receiving two returns the moment the first lands, and the second —
	// the §8.4 violation the scenario exists to catch — is graded as absent,
	// or reported as some other failure of the first. A second is far longer
	// than the whole local hub → engine → dispatch → POST path.
	overshootWindow = time.Second

	authSecretLen = 16 // RFC 8291 §2
	saltLen       = 16 // RFC 8188 §2.1
)

// observed is one delivered push: the plaintext payload plus the three
// unencrypted headers that carry policy (RFC 8030 §5.2–5.4), which is how a
// reader tells a collapse from a replacement without decrypting anything.
type observed struct {
	Elapsed time.Duration
	Topic   string
	TTL     string
	Urgency string
	Payload pushPayload
	Err     error // set when a delivery arrived but could not be read
}

// pushPayload is the relay's encrypted-side wire shape: the notify payload
// plus the Renotify flag the service worker needs to choose between a
// buzzing banner and a silent replacement. Declared with notify.Payload
// embedded, not copied field by field, so a payload change cannot leave this
// tool decoding a shape the relay no longer sends.
type pushPayload struct {
	notify.Payload
	Renotify bool `json:"renotify"`
}

func (o observed) String() string {
	head := fmt.Sprintf("+%-6s", fmt.Sprintf("%.1fs", o.Elapsed.Seconds()))
	if o.Err != nil {
		return head + " UNREADABLE: " + o.Err.Error()
	}
	var what string
	switch p := o.Payload; p.Kind {
	case notify.PushSession:
		what = fmt.Sprintf("session %s %s", p.State, p.SessionID)
	case notify.PushSummary:
		what = fmt.Sprintf("summary count=%d sessions=%v", p.Count, p.Sessions)
	case notify.PushDaemonDown, notify.PushDaemonUp:
		what = fmt.Sprintf("%s %s (%s)", p.Kind, p.DaemonID, p.DaemonLabel)
	default:
		what = string(p.Kind)
	}
	return fmt.Sprintf("%s %-52s topic=%s ttl=%s urgency=%s renotify=%t",
		head, what, o.Topic, o.TTL, o.Urgency, o.Payload.Renotify)
}

// observer holds the paired device, the keys its subscription was minted
// with, and the listener the relay POSTs to.
type observer struct {
	base   string // the relay's HTTP origin, derived from the ws URL
	client *http.Client

	deviceToken string
	daemonID    string // whose daemon_up counts as background — see partition
	priv        *ecdh.PrivateKey
	authSecret  []byte

	srv   *http.Server
	start time.Time

	mu  sync.Mutex
	got []observed
}

// newObserver pairs (or re-uses a pairing), starts the listener, and
// registers it as the device's subscription. Every failure names what the
// operator has to do about it: this runs before a scenario, so a refusal here
// costs nothing but a confusing one costs the whole test.
func newObserver(relayURL, clientToken string, st *driverState, start time.Time) (*observer, error) {
	base, err := httpBase(relayURL)
	if err != nil {
		return nil, err
	}
	o := &observer{base: base, client: &http.Client{Timeout: httpTimeout}, start: start, daemonID: st.DaemonID}

	if err := o.requirePushEnabled(); err != nil {
		return nil, err
	}
	if o.priv, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
		return nil, fmt.Errorf("minting the subscription's P-256 key: %w", err)
	}
	o.authSecret = make([]byte, authSecretLen)
	if _, err := rand.Read(o.authSecret); err != nil {
		return nil, fmt.Errorf("minting the subscription's auth secret: %w", err)
	}

	endpoint, err := o.listen()
	if err != nil {
		return nil, err
	}
	o.deviceToken = st.DeviceToken
	if o.deviceToken == "" {
		if clientToken == "" {
			return nil, fmt.Errorf("observing needs a relay token to pair with — pass -token, or -observe=false")
		}
		info("pairing a device — the same two steps a phone walks (§6.1)")
		if err := o.pair(clientToken, st); err != nil {
			return nil, err
		}
	}

	// A stored token that the relay no longer knows (a wiped rig, a revoke)
	// answers 401 to the registration; re-pair rather than making the
	// operator work out which of the two credentials went stale.
	if err := o.register(endpoint); err != nil {
		if !errors.Is(err, errUnauthorized) || clientToken == "" {
			return nil, err
		}
		info("the stored device token is no longer valid — pairing again")
		if err := o.pair(clientToken, st); err != nil {
			return nil, err
		}
		if err := o.register(endpoint); err != nil {
			return nil, err
		}
	}
	ok("observing as device %s — its subscription points at %s", st.DeviceTokenID, endpoint)
	return o, nil
}

// errUnauthorized marks the one status the caller acts on: a credential the
// relay does not accept, which is recoverable by pairing again.
var errUnauthorized = errors.New("unauthorized")

// requirePushEnabled refuses early on a relay that cannot push at all, with
// the relay's own reason. Under --auth off there is no VAPID identity and
// every push route is a 403 (docs/mobile-notifications-arc42.md §8.1), so a
// run there could drive transitions but never observe one.
func (o *observer) requirePushEnabled() error {
	var info struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	status, body, err := o.do(http.MethodGet, "/api/v1/push/info", "", nil)
	if err != nil {
		return fmt.Errorf("asking %s whether push is enabled: %w", o.base, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s answered %d to /api/v1/push/info — is it an irrlichtrelay?", o.base, status)
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return fmt.Errorf("decoding /api/v1/push/info: %w", err)
	}
	if !info.Enabled {
		return fmt.Errorf("%s has push disabled: %s", o.base, info.Reason)
	}
	return nil
}

// pair walks the same two steps a phone walks: the client token mints a
// single-use code in its workspace, and the code redeems for a device token
// in that same workspace. The workspace matters — dispatch only reaches
// subscriptions whose token resolves to the deciding workspace, which is the
// isolation boundary (§8.1), and the driver's own relay link uses the same
// client token, so both ends land in the same tenant by construction.
func (o *observer) pair(clientToken string, st *driverState) error {
	var mint struct {
		Code string `json:"code"`
	}
	status, body, err := o.do(http.MethodPost, "/api/v1/push/pairings", clientToken, nil)
	if err != nil {
		return fmt.Errorf("minting a pairing code: %w", err)
	}
	switch status {
	case http.StatusCreated:
	case http.StatusUnauthorized:
		return fmt.Errorf("the relay rejected the token when minting a pairing code — pass a valid -token")
	case http.StatusForbidden:
		return fmt.Errorf("the relay refuses to mint a pairing code: %s", strings.TrimSpace(string(body)))
	default:
		return fmt.Errorf("minting a pairing code answered %d: %s", status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &mint); err != nil || mint.Code == "" {
		return fmt.Errorf("the mint response carried no code: %s", strings.TrimSpace(string(body)))
	}

	var paired struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	req, _ := json.Marshal(map[string]string{"code": mint.Code, "label": observerLabel})
	status, body, err = o.do(http.MethodPost, "/api/v1/push/pair", "", req)
	if err != nil {
		return fmt.Errorf("redeeming the pairing code: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("redeeming the pairing code answered %d: %s", status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &paired); err != nil || paired.Token == "" {
		return fmt.Errorf("the pair response carried no device token: %s", strings.TrimSpace(string(body)))
	}
	o.deviceToken = paired.Token
	st.DeviceToken = paired.Token
	st.DeviceTokenID = paired.TokenID
	return nil
}

// register stores this run's endpoint and keys as the device's subscription,
// replacing whatever the previous run left — the same self-heal a phone
// performs on every open (§8.3), and the reason a stale endpoint from a
// crashed run cannot outlive the next one.
func (o *observer) register(endpoint string) error {
	if o.deviceToken == "" {
		return errors.New("no device token to register a subscription under")
	}
	body, err := json.Marshal(map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(o.priv.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(o.authSecret),
		},
	})
	if err != nil {
		return err
	}
	status, resp, err := o.do(http.MethodPost, "/api/v1/push/subscriptions", o.deviceToken, body)
	if err != nil {
		return fmt.Errorf("registering the observer's subscription: %w", err)
	}
	switch status {
	case http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return errUnauthorized
	default:
		return fmt.Errorf("registering the observer's subscription answered %d: %s", status, strings.TrimSpace(string(resp)))
	}
}

// listen starts the local push endpoint and returns its URL. The path is
// random for the same reason a push service's is: it is the subscription's
// capability (RFC 8030 §8.3), and the relay redacts it out of every log line
// and health response, which is easier to trust when it really is a secret.
func (o *observer) listen() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("opening the observer's push endpoint: %w", err)
	}
	var secret [8]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", fmt.Errorf("minting the endpoint path: %w", err)
	}
	path := "/push/" + hex.EncodeToString(secret[:])

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+path, o.handlePush)
	o.srv = &http.Server{Handler: mux, ReadHeaderTimeout: httpTimeout}
	go func() { _ = o.srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + path, nil
}

// handlePush records one delivery. It answers 201, which is what RFC 8030 §5
// has a push service return, so the relay's dispatcher records the delivery
// as successful exactly as it would against Apple.
//
// A body that will not decrypt is recorded rather than dropped: it means the
// relay sent something this tool cannot read, which is a finding, and the
// scenario's expectations then fail on it instead of reporting a silence
// that never happened.
func (o *observer) handlePush(w http.ResponseWriter, r *http.Request) {
	entry := observed{
		Elapsed: time.Since(o.start),
		Topic:   r.Header.Get("Topic"),
		TTL:     r.Header.Get("TTL"),
		Urgency: r.Header.Get("Urgency"),
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, pushBodyLimit))
	switch {
	case err != nil:
		entry.Err = fmt.Errorf("reading the push body: %w", err)
	default:
		plain, derr := decryptPush(body, o.priv, o.authSecret)
		if derr != nil {
			entry.Err = derr
		} else if jerr := json.Unmarshal(plain, &entry.Payload); jerr != nil {
			entry.Err = fmt.Errorf("decoding the payload %q: %w", truncate(string(plain), 120), jerr)
		}
	}
	o.mu.Lock()
	o.got = append(o.got, entry)
	o.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

// received returns every push delivered so far, oldest first.
func (o *observer) received() []observed {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]observed(nil), o.got...)
}

// graded returns the pushes this run's scenario is answerable for.
// Everything that decides or waits uses it — counting a background push
// toward a scenario's expectation makes await return before the push it was
// waiting for even exists, which reads as "the relay decided nothing".
func (o *observer) graded() []observed { g, _ := o.partition(); return g }

// partition splits off the one push a scenario provokes without asking for:
// a daemon_up naming this driver's own daemon.
//
// It is the §6.4 reconnect replacement. A previous run dropped the link, its
// 60 s grace expired into a "disconnected" banner, and this run's connect
// replaces that banner silently (Renotify=false) before the scenario has
// driven anything. Setting it aside cannot hide a decision these scenarios
// are about: the engine emits daemon_up ONLY where a disconnect banner is
// outstanding for that exact daemon, which is a fact about this driver's own
// previous run rather than about the transitions it is driving now. It is
// still printed, because a §8.4 decision nobody sees is the failure this
// whole driver exists to stop making.
func (o *observer) partition() (graded, background []observed) {
	for _, p := range o.received() {
		if p.Err == nil && p.Payload.Kind == notify.PushDaemonUp && p.Payload.DaemonID == o.daemonID {
			background = append(background, p)
			continue
		}
		graded = append(graded, p)
	}
	return graded, background
}

// await waits for at least n gradeable pushes — then one overshootWindow
// more, so an n+1th is seen — and returns what it has when the window closes.
// It never fails by itself: what "enough" means is the scenario's to decide,
// and a run that received MORE than expected has to be able to say so.
func (o *observer) await(ctx context.Context, n int, within time.Duration) []observed {
	deadline := time.Now().Add(within)
	for {
		got := o.graded()
		if len(got) >= n {
			o.quiet(ctx, overshootWindow)
			return o.graded()
		}
		if time.Now().After(deadline) {
			return got
		}
		select {
		case <-ctx.Done():
			return o.graded()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// quiet waits out a window in which the policy says nothing should arrive.
// There is no shortcut: a hold-down that was cancelled and one that has not
// fired yet look identical until the window has passed.
func (o *observer) quiet(ctx context.Context, d time.Duration) []observed {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
	return o.graded()
}

// close removes the subscription and stops the listener. Removing matters:
// left behind, it points at a port nothing is listening on, and every later
// push in this workspace — including the ones a real phone is waiting for —
// spends a delivery attempt and a failure line on it.
func (o *observer) close() {
	if o.deviceToken != "" {
		if _, _, err := o.do(http.MethodDelete, "/api/v1/push/subscriptions", o.deviceToken, nil); err != nil {
			info("could not remove the observer's subscription: %v", err)
		}
	}
	if o.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = o.srv.Shutdown(ctx)
	}
}

// do performs one call against the relay's REST surface.
func (o *observer) do(method, path, bearer string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, o.base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, pushBodyLimit))
	return resp.StatusCode, out, err
}

// httpBase turns the relay's WebSocket URL into its HTTP origin — the same
// origin serves the stream and the REST surface, so the driver needs exactly
// one address from its caller.
func httpBase(relayURL string) (string, error) {
	raw := strings.TrimSpace(relayURL)
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("relay url %q: %w", relayURL, err)
	}
	switch u.Scheme {
	case "ws", "http":
		u.Scheme = "http"
	case "wss", "https":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("relay url %q: scheme must be ws, wss, http or https", relayURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("relay url %q has no host", relayURL)
	}
	return u.Scheme + "://" + u.Host, nil
}
