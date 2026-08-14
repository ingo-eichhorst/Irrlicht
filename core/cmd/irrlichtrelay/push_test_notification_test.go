package main

// The "send a test notification" endpoint (docs/mobile-notifications-arc42.md
// §8.3's "Doubt" row, risk 11). Two properties are the whole reason it
// exists, and every test here grades one of them: it travels the SAME
// delivery path a real notification takes, and it answers with the outcome
// SYNCHRONOUSLY. A diagnostic that shortcuts proves nothing about the path it
// diagnoses, and one that fires and forgets leaves the 4xx it was built to
// surface in the relay log, where nobody is looking — which is exactly how a
// VAPID JWT with no `sub` claim survived until a code review found it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"irrlicht/core/cmd/irrlichtrelay/push"
	"irrlicht/core/domain/notify"
	"irrlicht/core/pkg/webpush"
)

// fixedPushBodySize is the on-the-wire size of EVERY encrypted push this
// relay sends: webpush.DefaultPadTo (2048) plus the aes128gcm header and GCM
// tag. arc42 §8.2 promises Apple and Google see one size whatever the
// payload, and core/pkg/webpush pins the equality across payload sizes —
// here it pins that the diagnostic pays the same padding as everything else,
// rather than being recognizable by its length.
const fixedPushBodySize = 2150

// testNotifyResponse is the wire shape of POST /api/v1/push/test. Every
// failure carries `error`, whatever its status, so one field answers "why
// not" for a refusal and for a push service's 4xx alike.
type testNotifyResponse struct {
	Delivered        bool   `json:"delivered"`
	EndpointHost     string `json:"endpoint_host"`
	At               int64  `json:"at"`
	Error            string `json:"error"`
	SubscriptionGone bool   `json:"subscription_gone"`
}

// testNotifyEnv is the observer's full dispatch chain served over HTTP: the
// mux runServe builds, with the same observer wired as the hub's push hook
// and as the test endpoint's send path.
type testNotifyEnv struct {
	*observerEnv
	srv *httptest.Server
}

func newTestNotifyEnv(t *testing.T, seeds ...deviceSeed) *testNotifyEnv {
	t.Helper()
	env := newObserverEnv(t, seeds...)
	srv := httptest.NewServer(buildMux(newHubWithAuth(env.store, nil, defaultLimits()), env.store, env.svc, env.obs))
	t.Cleanup(srv.Close)
	return &testNotifyEnv{observerEnv: env, srv: srv}
}

// sendTest POSTs the endpoint as the named device and decodes the answer.
func (e *testNotifyEnv) sendTest(label string) (int, testNotifyResponse, []byte) {
	e.t.Helper()
	status, body := doPush(e.t, "POST", e.srv.URL+"/api/v1/push/test", e.tokens[label], nil)
	var resp testNotifyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		e.t.Fatalf("test-notification response is not JSON: %v (%s)", err, body)
	}
	return status, resp, body
}

func phoneSeed(endpoint string) deviceSeed {
	return deviceSeed{label: "phone", workspace: "acme", endpoint: endpoint}
}

// TestTestNotificationRequiresADeviceToken: the route is device-token authed
// like every other subscription route (docs/mobile-notifications-arc42.md
// §8.1). Unauthenticated it would let any internet caller buzz a stranger's
// phone and, worse, read back whether that phone's subscription is alive.
func TestTestNotificationRequiresADeviceToken(t *testing.T) {
	env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/abc"))
	for _, bearer := range []string{"", "not-a-token"} {
		status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/test", bearer, nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("test notification with bearer %q = %d, want 401: %s", bearer, status, body)
		}
	}
	if n := len(env.sender.sends()); n != 0 {
		t.Fatalf("an unauthenticated request reached the sender: %d send(s)", n)
	}
}

// TestTestNotificationWithoutASubscriptionIsRefused: a phone with no
// registered delivery address must be told so, not answered "sent". Silent
// success on the one endpoint whose entire job is answering doubt is the
// §8.3 house rule inverted.
func TestTestNotificationWithoutASubscriptionIsRefused(t *testing.T) {
	env := newTestNotifyEnv(t, deviceSeed{label: "phone", workspace: "acme"}) // paired, never subscribed

	status, resp, body := env.sendTest("phone")
	if status != http.StatusConflict {
		t.Fatalf("test notification without a subscription = %d, want 409: %s", status, body)
	}
	if resp.Delivered {
		t.Fatalf("a refusal reported delivered=true: %s", body)
	}
	if !strings.Contains(strings.ToLower(resp.Error), "subscri") {
		t.Fatalf("refusal %q does not say the phone has no subscription", resp.Error)
	}
	if n := len(env.sender.sends()); n != 0 {
		t.Fatalf("a phone with no address still reached the sender: %d send(s)", n)
	}
}

// TestTestNotificationReportsTheSendOutcome is the endpoint's reason for
// existing: the verdict arrives in the RESPONSE, not only in the relay log.
// A push service's 4xx — the shape of every defect this feature has had that
// only fails on contact with a real one — must reach the person holding the
// phone.
func TestTestNotificationReportsTheSendOutcome(t *testing.T) {
	t.Run("a successful send answers 200 delivered", func(t *testing.T) {
		env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/ok"))
		status, resp, body := env.sendTest("phone")
		if status != http.StatusOK {
			t.Fatalf("test notification = %d, want 200: %s", status, body)
		}
		if !resp.Delivered {
			t.Fatalf("a successful send reported delivered=false: %s", body)
		}
		if resp.EndpointHost != "push.example" {
			t.Fatalf("endpoint_host = %q, want push.example", resp.EndpointHost)
		}
		if resp.At != env.clk.now().Unix() {
			t.Fatalf("at = %d, want the send time %d", resp.At, env.clk.now().Unix())
		}
		st, ok := env.svc.DeliveryStatus(env.tokenIDs["phone"])
		if !ok || !st.OK {
			t.Fatalf("delivery health after a test send = %+v ok=%v, want a recorded success", st, ok)
		}
	})

	t.Run("a push service failure is an error, never a 200", func(t *testing.T) {
		env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/bad"))
		env.sender.fail["https://push.example/v2/bad"] = &webpush.StatusError{Code: 403, Body: "VAPID token missing sub"}

		status, resp, body := env.sendTest("phone")
		if status == http.StatusOK {
			t.Fatalf("a failed send answered 200 — the failure never reaches the person looking: %s", body)
		}
		if status != http.StatusBadGateway {
			t.Fatalf("failed send = %d, want 502: %s", status, body)
		}
		if resp.Delivered {
			t.Fatalf("a failed send reported delivered=true: %s", body)
		}
		if !strings.Contains(resp.Error, "403") || !strings.Contains(resp.Error, "sub") {
			t.Fatalf("error %q does not carry what the push service said", resp.Error)
		}
		if entries := env.svc.Subscriptions(); len(entries) != 1 {
			t.Fatalf("a transient failure pruned the subscription: %+v", entries)
		}
	})

	t.Run("a gone subscription is pruned and says so", func(t *testing.T) {
		env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/gone"))
		env.sender.fail["https://push.example/v2/gone"] = fmt.Errorf("https://push.example answered 410: %w", webpush.ErrSubscriptionGone)

		status, resp, body := env.sendTest("phone")
		if status != http.StatusBadGateway {
			t.Fatalf("gone subscription = %d, want 502: %s", status, body)
		}
		if !resp.SubscriptionGone {
			t.Fatalf("a pruned subscription is not reported as gone, so the app cannot advise a re-subscribe: %s", body)
		}
		if entries := env.svc.Subscriptions(); len(entries) != 0 {
			t.Fatalf("gone subscription not pruned: %+v", entries)
		}
	})
}

// TestTestNotificationNeverEchoesTheEndpointPath: the endpoint's path is the
// subscription's capability secret (RFC 8030 §8.3) and must not leave the
// relay, not even to the phone that owns it — the same rule the health
// endpoint follows. A failure message is where it would ride out, because a
// transport error renders the whole request URL.
func TestTestNotificationNeverEchoesTheEndpointPath(t *testing.T) {
	const endpoint = "https://push.example/v2/super-secret-capability"
	env := newTestNotifyEnv(t, phoneSeed(endpoint))
	env.sender.fail[endpoint] = fmt.Errorf("Post %q: connection refused", endpoint)

	_, _, body := env.sendTest("phone")
	if strings.Contains(string(body), "super-secret-capability") {
		t.Fatalf("the response carries the subscription's capability secret: %s", body)
	}
	if !strings.Contains(string(body), "push.example") {
		t.Fatalf("the response dropped the host too, which is what makes an outage diagnosable: %s", body)
	}
}

// TestTestNotificationCarriesItsOwnKindAndCollapseKey pins the payload the
// worker composes from. A test notification must not collapse with a real
// session's banner (same `tag`/`Topic` would replace it), and it must not
// enter the ledger the badge counts — it names no session.
func TestTestNotificationCarriesItsOwnKindAndCollapseKey(t *testing.T) {
	env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/abc"))
	if status, _, body := env.sendTest("phone"); status != http.StatusOK {
		t.Fatalf("test notification = %d: %s", status, body)
	}

	sends := env.sender.sends()
	if len(sends) != 1 {
		t.Fatalf("got %d sends, want exactly 1", len(sends))
	}
	payload := decodePayload(t, sends[0].payload)
	if payload["kind"] != "test" {
		t.Fatalf("payload kind = %v, want test", payload["kind"])
	}
	if payload["session_id"] != nil {
		t.Fatalf("a test notification names a session (%v) — it would fold into the ledger and count toward the badge", payload["session_id"])
	}
	if sends[0].opts.Topic == "summary" || strings.HasPrefix(sends[0].opts.Topic, "daemon:") || sends[0].opts.Topic == "" {
		t.Fatalf("Topic %q collapses with a real notification's", sends[0].opts.Topic)
	}
	if sends[0].opts.TTL <= 0 {
		t.Fatalf("TTL = %v: a test notification with no TTL is dropped by a push service whenever the phone is asleep", sends[0].opts.TTL)
	}
}

// TestTestNotificationAppliesNoPolicy: it is a direct dispatch, not a
// transition (docs/mobile-notifications-arc42.md §8.4 decides transitions).
// Routing it through the engine would give the diagnostic a hold-down, a
// cooldown and a burst window — every one of which can answer "sent" while
// nothing leaves the relay.
func TestTestNotificationAppliesNoPolicy(t *testing.T) {
	env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/abc"))
	if status, _, body := env.sendTest("phone"); status != http.StatusOK {
		t.Fatalf("test notification = %d: %s", status, body)
	}
	env.obs.mu.Lock()
	engines := len(env.obs.engines)
	env.obs.mu.Unlock()
	if engines != 0 {
		t.Fatalf("the test notification created %d policy engine(s) — it went through §8.4 rather than around it", engines)
	}
}

// TestTestNotificationIsRateLimitedPerDevice: the route is authenticated and
// reaches only the caller's own phone, so it is neither an amplifier nor a
// way to reach anybody else — but one tap is one POST from the relay's IP to
// Apple or Google, and the state someone is in when they reach for this
// button is "tapping it repeatedly because nothing arrived".
func TestTestNotificationIsRateLimitedPerDevice(t *testing.T) {
	env := newTestNotifyEnv(t,
		phoneSeed("https://push.example/v2/abc"),
		deviceSeed{label: "other", workspace: "acme", endpoint: "https://push.example/v2/other"},
	)
	if status, _, body := env.sendTest("phone"); status != http.StatusOK {
		t.Fatalf("first test notification = %d: %s", status, body)
	}
	status, resp, body := env.sendTest("phone")
	if status != http.StatusTooManyRequests {
		t.Fatalf("second test notification = %d, want 429: %s", status, body)
	}
	if resp.Error == "" {
		t.Fatalf("a 429 with no reason leaves the app guessing: %s", body)
	}

	// Per device, not global: another phone's diagnostic is not collateral.
	if status, _, body := env.sendTest("other"); status != http.StatusOK {
		t.Fatalf("a second device was rate-limited by the first: %d %s", status, body)
	}

	// And it drains.
	env.clk.advance(push.TestNotificationInterval)
	if status, _, body := env.sendTest("phone"); status != http.StatusOK {
		t.Fatalf("test notification after the interval = %d, want 200: %s", status, body)
	}
	if n := len(env.sender.sends()); n != 3 {
		t.Fatalf("got %d sends, want 3 (two allowed for the phone, one for the other device)", n)
	}
}

// TestTestNotificationTravelsTheProductionPath is the property the whole
// endpoint rests on: the diagnostic goes out through the production
// webpush.Sender — real VAPID signing, real RFC 8291 encryption, real
// headers, real padding — so what it proves is what a real notification
// would do. A shortcut here (composing a request by hand, a second sender,
// an unsigned POST) is a diagnostic that passes while the path it reports on
// is broken, which is the failure the missing `sub` claim actually was.
func TestTestNotificationTravelsTheProductionPath(t *testing.T) {
	var got struct {
		auth     string
		encoding string
		ttl      string
		topic    string
		urgency  string
		body     []byte
	}
	ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		got.encoding = r.Header.Get("Content-Encoding")
		got.urgency = r.Header.Get("Urgency")
		got.ttl = r.Header.Get("TTL")
		got.topic = r.Header.Get("Topic")
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ps.Close)

	env := newTestNotifyEnv(t, phoneSeed(ps.URL+"/wpush/v1/device"))
	// The production seam, built exactly as runServe builds it.
	env.obs.sender = newRelayPushSender(env.svc, "")

	status, resp, body := env.sendTest("phone")
	if status != http.StatusOK || !resp.Delivered {
		t.Fatalf("production-path test notification = %d %s", status, body)
	}

	// RFC 8292 §3.3: "vapid t=<jwt>, k=<public key>", and the JWT carries the
	// `sub` claim Apple Web Push and Mozilla autopush reject a push without.
	if !strings.HasPrefix(got.auth, "vapid t=") || !strings.Contains(got.auth, ", k=") {
		t.Fatalf("Authorization %q is not an RFC 8292 VAPID credential — the test push did not go through the production sender", got.auth)
	}
	_, rest, _ := strings.Cut(got.auth, "t=")
	jwt, _, _ := strings.Cut(rest, ",")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("VAPID token %q is not a three-part JWS", jwt)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding JWT claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("JWT claims are not JSON: %v (%s)", err, raw)
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		t.Fatalf("the test notification's VAPID JWT carries no sub claim (%v) — this is the exact defect the button exists to surface (RFC 8292 §2.1)", claims)
	}

	// RFC 8291: the body is ciphertext, padded to the fixed size §8.2
	// promises, and never the plaintext payload.
	if got.encoding != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q, want aes128gcm (RFC 8188)", got.encoding)
	}
	if strings.Contains(string(got.body), "test") {
		t.Fatalf("the request body carries readable payload text — it was not encrypted")
	}

	// The RFC 8030 headers a real notification carries. Urgency in particular
	// is not decoration: without it the diagnostic would be the only send the
	// relay makes with no Urgency at all, so a push service could treat it
	// differently from the thing it is supposed to be standing in for.
	if got.urgency != string(webpush.UrgencyHigh) {
		t.Fatalf("Urgency = %q, want %q — the diagnostic must carry the header shape a real notification does (RFC 8030 §5.3)",
			got.urgency, webpush.UrgencyHigh)
	}
	if got.ttl == "" || got.topic == "" {
		t.Fatalf("TTL = %q, Topic = %q — both are part of the shape being diagnosed", got.ttl, got.topic)
	}
	if len(got.body) != fixedPushBodySize {
		t.Fatalf("body is %d bytes, want the fixed %d every push uses (arc42 §8.2)", len(got.body), fixedPushBodySize)
	}
	if got.ttl == "" || got.topic == "" {
		t.Fatalf("TTL = %q Topic = %q, want the RFC 8030 headers a real push carries", got.ttl, got.topic)
	}
}

// TestTestPayloadVersionMatchesTheEngines locks the two copies of the
// payload version together. sw.js refuses to compose anything from a payload
// whose `v` it does not know, and notify's own payloadVersion is unexported
// — so the relay spells it a second time, and a bump on one side alone would
// leave every phone showing "update the app page" for the one notification
// whose whole job is to prove delivery works.
func TestTestPayloadVersionMatchesTheEngines(t *testing.T) {
	eng := notify.New(notify.Config{})
	now := time.Unix(1_700_000_000, 0)
	eng.Handle(notify.Event{Kind: notify.EventSessionUpdate, SessionID: "s-1", State: notify.StateWorking}, now)
	pushes := eng.Handle(notify.Event{Kind: notify.EventSessionUpdate, SessionID: "s-1", State: notify.StateWaiting}, now)
	if len(pushes) != 1 {
		t.Fatalf("expected one session push to read the engine's version from, got %d", len(pushes))
	}
	if got := testNotificationPayload(now).Version; got != pushes[0].Payload.Version {
		t.Fatalf("test payload v=%d, engine payload v=%d — sw.js checks one number", got, pushes[0].Payload.Version)
	}
}

// TestTestNotificationCancelledRequestIsNotASuccess: whatever the sender
// hands back, only a nil error is a delivery.
func TestTestNotificationCancelledRequestIsNotASuccess(t *testing.T) {
	env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/abc"))
	env.sender.fail["https://push.example/v2/abc"] = context.Canceled

	status, resp, body := env.sendTest("phone")
	if status == http.StatusOK || resp.Delivered {
		t.Fatalf("a cancelled send reported success: %d %s", status, body)
	}
}

// ctxProbeSender records whether the context it was handed can be cancelled
// at all — context.Background().Done() is nil, a request's context's is not.
type ctxProbeSender struct{ cancellable bool }

func (s *ctxProbeSender) Send(ctx context.Context, _ webpush.Subscription, _ []byte, _ webpush.Options) error {
	s.cancellable = ctx.Done() != nil
	return nil
}

// TestTestNotificationRidesTheRequestContext is what makes the rule below
// reachable in production: the send takes the REQUEST's context, so a phone
// that hangs up mid-send cancels it. Handing the sender a background context
// instead would leave the relay POSTing for a client that is gone, and would
// make the cancellation rule dead code that no mutation could redden.
func TestTestNotificationRidesTheRequestContext(t *testing.T) {
	env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/abc"))
	probe := &ctxProbeSender{}
	env.obs.sender = probe

	if status, _, body := env.sendTest("phone"); status != http.StatusOK {
		t.Fatalf("test notification = %d: %s", status, body)
	}
	if !probe.cancellable {
		t.Fatal("the send was handed a context that can never be cancelled — a phone that hangs up leaves the relay POSTing for nobody")
	}
}

// TestTestNotificationCancelledSendLeavesDeliveryHealthAlone is the one
// behaviour a synchronous send adds over the dispatcher's async one, and it
// inherits the async rule rather than inventing its own: a cancellation is
// not a delivery verdict. The user closed the app mid-send; recording that
// as a failure would have the §8.3 panel report "the last attempt failed"
// about a subscription that is fine — and the panel is exactly the surface
// somebody consults when they already suspect something is wrong.
func TestTestNotificationCancelledSendLeavesDeliveryHealthAlone(t *testing.T) {
	env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/abc"))
	// A prior success, so "health untouched" has something to preserve
	// rather than being an absence any wiring would satisfy.
	env.svc.SetDeliveryStatus(env.tokenIDs["phone"], push.DeliveryStatus{At: 1, OK: true})
	env.sender.fail["https://push.example/v2/abc"] = context.Canceled

	entry, ok := env.svc.SubscriptionOf(env.tokenIDs["phone"])
	if !ok {
		t.Fatal("no subscription to send to")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v := env.obs.sendTestNotification(ctx, entry); v.ok {
		t.Fatal("a send that never left reported success")
	}

	st, ok := env.svc.DeliveryStatus(env.tokenIDs["phone"])
	if !ok || st.At != 1 || !st.OK {
		t.Fatalf("a cancelled send overwrote delivery health: %+v (ok=%v)", st, ok)
	}
}

// TestTestNotificationUsesTheDispatchersOwnSender asserts the property the
// endpoint rests on by IDENTITY, not by shape. Its sibling above decodes what
// a push service received and finds correct VAPID, encryption and padding —
// but a second, correctly-configured sender produces byte-identical output,
// so that test stays green for a diagnostic that has quietly stopped sharing
// the dispatcher's path. Only the object can tell those apart: the fake here
// is the dispatcher's sender, so a diagnostic that builds its own reaches
// nothing and the count stays at zero.
//
// This is the assertion #1453's rule asks for — an arm that fails for THIS
// obligation rather than incidentally. Giving sendTestNotification its own
// sender makes the shape test fail on "no such host", which is a different
// claim than "the diagnostic left the production path".
func TestTestNotificationUsesTheDispatchersOwnSender(t *testing.T) {
	env := newTestNotifyEnv(t, phoneSeed("https://push.example/v2/identity"))
	before := len(env.sender.sends())

	status, resp, body := env.sendTest("phone")
	if status != http.StatusOK {
		t.Fatalf("test notification: status %d, want 200 (%s)", status, body)
	}
	if !resp.Delivered {
		t.Fatalf("test notification not delivered: %s", body)
	}

	sends := env.sender.sends()
	if len(sends) != before+1 {
		t.Fatalf("the dispatcher's sender saw %d send(s), want one more than %d — the diagnostic went out through a sender of its own, so what it proves is not what a real notification does",
			len(sends), before)
	}
	if got := sends[len(sends)-1].sub.Endpoint; got != "https://push.example/v2/identity" {
		t.Fatalf("the dispatcher's sender was handed %q, want the caller's own subscription", got)
	}
}
