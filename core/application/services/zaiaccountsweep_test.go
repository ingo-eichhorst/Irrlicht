package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/zaiaccountapi"
	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// zaiBody is the #2010 live probe's 13:16:20 response (redacted, verbatim
// from zaiaccountapi/testdata/live_probe_2026-11-21.json), with the
// five-hour percentage swapped in so two keys' readings are told apart.
func zaiBody(fiveHourPercent string) []byte {
	return []byte(`{"code":200,"success":true,"data":{"level":"lite","limits":[` +
		`{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":2,"remaining":1997,"percentage":` + fiveHourPercent + `,"nextResetTime":1795284966038},` +
		`{"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":10000,"currentValue":2,"remaining":9997,"percentage":1,"nextResetTime":1795871277984}]}}`)
}

// fakeZaiCredentials answers per settings file and counts every read.
type fakeZaiCredentials struct {
	mu    sync.Mutex
	reads int
	keys  map[string]string // settings file -> key
	err   error
}

func (f *fakeZaiCredentials) Read(route *session.RouteObservation) (zaiaccountapi.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return zaiaccountapi.Credential{}, f.err
	}
	key, ok := f.keys[route.SettingsFile]
	if !ok {
		return zaiaccountapi.Credential{}, &zaiaccountapi.CredentialUnavailableError{Reason: zaiaccountapi.ReasonNoKey}
	}
	return zaiaccountapi.Credential{Key: outbound.NewCredential(key), Fingerprint: "fp-" + key}, nil
}

func (f *fakeZaiCredentials) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func zaiSession(id, settingsFile string) *session.SessionState {
	return &session.SessionState{
		SessionID: id,
		Adapter:   "claude-code",
		Route: &session.RouteObservation{Status: session.RouteObserved, Endpoint: "https://api.z.ai/api/anthropic",
			Source: "settings:local", SettingsFile: settingsFile},
	}
}

// bodyPerKey answers each key with its own five-hour percentage.
func bodyPerKey(percent map[string]string) *fakeTransport {
	return &fakeTransport{fn: func(_ int, req outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: zaiBody(percent[req.Credential.Reveal()])}, nil
	}}
}

func newZaiTestSweeper(sessions sessionLister, creds ZaiCredentialReader, transport outbound.AccountQuotaTransport, granted func() bool) (*ZaiAccountSweeper, *fakeRateLimitWriter, *sweepLogger) {
	w := &fakeRateLimitWriter{}
	log := &sweepLogger{}
	return NewZaiAccountSweeper(ZaiAccountSweeperDeps{
		Sessions:    sessions,
		Writer:      w,
		Poller:      NewAccountPoller(),
		Credentials: creds,
		Transport:   transport,
		Granted:     granted,
		Log:         log,
		Interval:    time.Minute,
	}), w, log
}

func writesFor(writes []rateLimitWrite, id string) []rateLimitWrite {
	var out []rateLimitWrite
	for _, w := range writes {
		if w.sessionID == id {
			out = append(out, w)
		}
	}
	return out
}

// Only a top-level session whose OBSERVED route names api.z.ai is bound.
// The adapter name never qualifies a session, and neither does a model or a
// route that is unreadable or elsewhere.
func TestZaiAccountSweep_BindsOnlyOnAnObservedZaiRoute(t *testing.T) {
	creds := &fakeZaiCredentials{keys: map[string]string{"/p/.claude/settings.local.json": "k1"}}
	child := zaiSession("child", "/p/.claude/settings.local.json")
	child.ParentSessionID = "z1"
	unreadable := zaiSession("unreadable", "/p/.claude/settings.local.json")
	unreadable.Route.Status = session.RouteUnreadable
	lookalike := zaiSession("lookalike", "/p/.claude/settings.local.json")
	lookalike.Route.Endpoint = "https://api.z.ai.example.com/anthropic"
	anthropic := &session.SessionState{SessionID: "anthropic", Adapter: "claude-code",
		Route:   &session.RouteObservation{Status: session.RouteAbsent},
		Metrics: &session.SessionMetrics{ModelName: "glm-4.6"}}
	sessions := staticSessions{zaiSession("z1", "/p/.claude/settings.local.json"), child, unreadable, lookalike, anthropic}
	sw, w, _ := newZaiTestSweeper(sessions, creds, bodyPerKey(map[string]string{"k1": "1"}), alwaysGranted)

	sw.sweep(context.Background())

	writes := w.snapshot()
	if len(writes) != 1 || writes[0].sessionID != "z1" || writes[0].provider != session.ProviderZai || writes[0].snap == nil {
		t.Fatalf("writes = %+v, want exactly one zai snapshot, for z1", writes)
	}
	if creds.count() != 1 {
		t.Errorf("credential reads = %d, want 1 (only z1 is bound)", creds.count())
	}
}

// Without the grant no key is read and nothing is fetched; a zai snapshot
// written while it was granted is cleared.
func TestZaiAccountSweep_NotGrantedReadsNoKeyAndClears(t *testing.T) {
	creds := &fakeZaiCredentials{keys: map[string]string{"/p/s.json": "k1"}}
	transport := bodyPerKey(map[string]string{"k1": "1"})
	withSnap := zaiSession("z1", "/p/s.json")
	withSnap.Metrics = &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{Provider: session.ProviderZai}}
	sw, w, _ := newZaiTestSweeper(staticSessions{withSnap, zaiSession("z2", "/p/s.json")}, creds, transport, func() bool { return false })

	sw.sweep(context.Background())

	if creds.count() != 0 {
		t.Errorf("credential reads = %d without the grant, want 0", creds.count())
	}
	if transport.calls != 0 {
		t.Errorf("transport calls = %d without the grant, want 0", transport.calls)
	}
	writes := w.snapshot()
	if len(writes) != 1 || writes[0].sessionID != "z1" || writes[0].snap != nil {
		t.Fatalf("writes = %+v, want one clear for z1 only", writes)
	}
}

// A session whose key cannot be read gets no reading — never one under some
// other key — any it had is cleared, and the reason is logged once per
// streak.
func TestZaiAccountSweep_CredentialUnavailableClearsAndLogsOnce(t *testing.T) {
	creds := &fakeZaiCredentials{err: &zaiaccountapi.CredentialUnavailableError{Reason: zaiaccountapi.ReasonSettingsNotZai}}
	transport := bodyPerKey(nil)
	st := zaiSession("z1", "/p/s.json")
	st.Metrics = &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{Provider: session.ProviderZai}}
	sw, w, log := newZaiTestSweeper(staticSessions{st}, creds, transport, alwaysGranted)

	sw.sweep(context.Background())
	sw.sweep(context.Background())

	if transport.calls != 0 {
		t.Errorf("transport calls = %d, want 0 with no key", transport.calls)
	}
	if writes := w.snapshot(); len(writes) != 2 || writes[0].snap != nil {
		t.Errorf("writes = %+v, want a clear each tick (the listed copy still holds the old reading)", writes)
	}
	if len(log.info) != 1 || !strings.Contains(log.info[0], zaiaccountapi.ReasonSettingsNotZai) {
		t.Errorf("log = %v, want one line naming %s", log.info, zaiaccountapi.ReasonSettingsNotZai)
	}
}

// #2010 mutation 7: two sessions on DIFFERENT keys never share one poll or
// one reading, even though nothing distinguishes their accounts.
func TestZaiAccountSweep_DifferentKeysNeverShareAReading(t *testing.T) {
	creds := &fakeZaiCredentials{keys: map[string]string{"/a/s.json": "key-a", "/b/s.json": "key-b"}}
	transport := bodyPerKey(map[string]string{"key-a": "7", "key-b": "42"})
	sw, w, _ := newZaiTestSweeper(staticSessions{zaiSession("a", "/a/s.json"), zaiSession("b", "/b/s.json")}, creds, transport, alwaysGranted)

	sw.sweep(context.Background())

	if transport.calls != 2 {
		t.Fatalf("transport calls = %d, want 2: two keys are two polls (saw credentials %v)", transport.calls, transport.sawCreds)
	}
	writes := w.snapshot()
	for id, want := range map[string]float64{"a": 7, "b": 42} {
		ws := writesFor(writes, id)
		if len(ws) != 1 || ws[0].snap == nil || ws[0].snap.Windows[0].UsedPercent != want {
			t.Errorf("session %s writes = %+v, want its own key's reading (%v%%)", id, ws, want)
		}
	}
}

// Two sessions on the SAME key share one poll and one reading.
func TestZaiAccountSweep_SameKeySharesOnePoll(t *testing.T) {
	creds := &fakeZaiCredentials{keys: map[string]string{"/a/s.json": "key-a", "/b/s.json": "key-a"}}
	transport := bodyPerKey(map[string]string{"key-a": "7"})
	sw, w, _ := newZaiTestSweeper(staticSessions{zaiSession("a", "/a/s.json"), zaiSession("b", "/b/s.json")}, creds, transport, alwaysGranted)

	sw.sweep(context.Background())

	if transport.calls != 1 {
		t.Fatalf("transport calls = %d, want 1 for one key", transport.calls)
	}
	if n := len(w.snapshot()); n != 2 {
		t.Errorf("writes = %d, want both sessions to get the shared reading", n)
	}
}

// A failed poll with no earlier success publishes nothing — never a zero.
func TestZaiAccountRefresh_NeverPublishesZeroOnFailure(t *testing.T) {
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureAuthRejected, Detail: "status 401"}
	}}
	cred := zaiaccountapi.Credential{Key: outbound.NewCredential("k"), Fingerprint: "fp"}
	p := NewAccountPoller()
	for i := 0; i < 2; i++ { // the second call lands inside the failure backoff: no error, no value
		snap, err := ZaiAccountRefresh(context.Background(), p, transport, alwaysGranted, "z1", cred)
		if snap != nil || err == nil {
			t.Fatalf("call %d: got %+v, %v; want (nil, error)", i, snap, err)
		}
	}
}

// A failure after a success serves that reading, keeping its original time
// and marked with the failure.
func TestZaiAccountRefresh_StaleReadingCarriesFailure(t *testing.T) {
	fail := false
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		if fail {
			return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureNetwork, Detail: "x"}
		}
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: zaiBody("1")}, nil
	}}
	clock := time.Unix(1795270580, 0)
	p := NewAccountPoller()
	p.now = func() time.Time { return clock }
	cred := zaiaccountapi.Credential{Key: outbound.NewCredential("k"), Fingerprint: "fp"}

	first, err := ZaiAccountRefresh(context.Background(), p, transport, alwaysGranted, "z1", cred)
	if err != nil || first == nil {
		t.Fatalf("first refresh = %+v, %v", first, err)
	}
	fail = true
	clock = clock.Add(pollFreshWindow + time.Second)
	stale, err := ZaiAccountRefresh(context.Background(), p, transport, alwaysGranted, "z1", cred)
	if err == nil || stale == nil {
		t.Fatalf("stale refresh = %+v, %v; want the old reading and an error", stale, err)
	}
	if stale.RetrievalFailure == "" || stale.SampledAt != first.SampledAt {
		t.Errorf("stale = %+v, want RetrievalFailure set and SampledAt %d kept", stale, first.SampledAt)
	}
}

// Revoking the permission drops every Z.ai poll key: Revoke deletes by the
// Provider ZaiAccountQuotaKey stamps.
func TestZaiAccountQuotaKey_RevokeMatchesItsProvider(t *testing.T) {
	transport := bodyPerKey(map[string]string{"k": "1"})
	p := NewAccountPoller()
	cred := zaiaccountapi.Credential{Key: outbound.NewCredential("k"), Fingerprint: "fp"}
	if _, err := ZaiAccountRefresh(context.Background(), p, transport, alwaysGranted, "z1", cred); err != nil {
		t.Fatal(err)
	}
	p.Revoke(session.ProviderZai)
	if _, err := ZaiAccountRefresh(context.Background(), p, transport, alwaysGranted, "z1", cred); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 2 {
		t.Fatalf("transport calls = %d, want 2: Revoke(zai) must have dropped the cached reading", transport.calls)
	}
}

func TestZaiAccountRefresh_RefusesAnUnresolvedCredential(t *testing.T) {
	_, err := ZaiAccountRefresh(context.Background(), NewAccountPoller(), bodyPerKey(nil), alwaysGranted, "z1", zaiaccountapi.Credential{})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
