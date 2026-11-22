package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/zaiaccountapi"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func TestZaiAccountAPI_SweeperIsBuiltWhenThePartsAre(t *testing.T) {
	api := zaiAccountAPIEffects(e2eLog{}, services.NewAccountPoller())
	if api.transport == nil || api.credentials == nil {
		t.Fatal("zaiAccountAPIEffects built no transport or credential reader for the fixed Z.ai destination")
	}
	if api.sweeper(nil, nil, nil, e2eLog{}) == nil {
		t.Fatal("sweeper() returned nil with its parts built; the daemon would start no sweep")
	}
}

func TestZaiAccountAPI_NoSweeperWithoutParts(t *testing.T) {
	if got := newZaiAccountAPI(services.NewAccountPoller(), nil, nil).sweeper(nil, nil, nil, e2eLog{}); got != nil {
		t.Fatal("sweeper() built a sweep with nothing to poll through")
	}
}

// loopbackOnlyDials replaces http.DefaultTransport — what
// accountquota.NewHTTPTransport sends through — with a clone whose dialer
// refuses every non-loopback address and records it, for the rest of the
// test. Its proxy is cleared too: a loopback proxy would otherwise be
// dialled and carry the request off the machine. No test in this package
// calls t.Parallel, so no other test runs while the swap is in place.
type loopbackOnlyDials struct {
	mu      sync.Mutex
	refused []string
	allowed int
}

var errDialRefusedForTest = errors.New("test dial guard: non-loopback address refused")

func installLoopbackOnlyDials(t *testing.T) *loopbackOnlyDials {
	t.Helper()
	g := &loopbackOnlyDials{}
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	rt.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			g.refused = append(g.refused, addr)
			return nil, errDialRefusedForTest
		}
		g.allowed++
		return dialer.DialContext(ctx, network, addr)
	}
	prev := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = prev })
	return g
}

func (g *loopbackOnlyDials) snapshot() ([]string, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.refused...), g.allowed
}

type fakeSessionList []*session.SessionState

func (s fakeSessionList) ListAll() ([]*session.SessionState, error) { return s, nil }

type zaiTestWriter struct {
	mu     sync.Mutex
	writes int
}

func (w *zaiTestWriter) SetProviderRateLimit(string, string, *session.RateLimitSnapshot) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	return true
}

// TestZaiAccountAPI_ProductionPathNeverLeavesLoopback drives the daemon's
// PRODUCTION Z.ai parts — zaiAccountAPIEffects' transport to the reviewed
// api.z.ai destination and its settings-file credential reader — through
// one granted sweep of a session routed to api.z.ai, with a loopback-only
// dial guard under the transport. The request the daemon would send must
// stop at the dialer: refused, aimed at api.z.ai:443, and nothing written.
// That is the assertion, not an assumption, that this suite does not reach
// Z.ai even when it runs the code that would.
func TestZaiAccountAPI_ProductionPathNeverLeavesLoopback(t *testing.T) {
	guard := installLoopbackOnlyDials(t)
	api := zaiAccountAPIEffects(e2eLog{}, services.NewAccountPoller())
	if api.transport == nil || api.credentials == nil {
		t.Fatal("production parts were not built")
	}

	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic","ANTHROPIC_AUTH_TOKEN":"not-a-real-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := fakeSessionList{{
		SessionID: "z1", Adapter: "claude-code", CWD: dir,
		Route: &session.RouteObservation{Status: session.RouteObserved, Endpoint: "https://api.z.ai/api/anthropic",
			Source: "settings:local", SettingsFile: settings},
	}}
	writer := &zaiTestWriter{}
	sw := services.NewZaiAccountSweeper(services.ZaiAccountSweeperDeps{
		Sessions:    sessions,
		Writer:      writer,
		Poller:      api.poller,
		Credentials: api.credentials,
		Transport:   api.transport,
		Granted:     func() bool { return true },
		Log:         e2eLog{},
		Interval:    time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sw.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	start := time.Now()
	deadline := start.Add(10 * time.Second)
	for {
		refused, _ := guard.snapshot()
		if len(refused) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %s the production path made no dial at all; this test would prove nothing", time.Since(start))
		}
		time.Sleep(10 * time.Millisecond)
	}

	refused, allowed := guard.snapshot()
	if refused[0] != "api.z.ai:443" {
		t.Errorf("refused dial = %q, want api.z.ai:443", refused[0])
	}
	if allowed != 0 {
		t.Errorf("%d dial(s) were let through; this path dials nothing local", allowed)
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.writes != 0 {
		t.Errorf("%d write(s) after a refused fetch; a failed poll publishes nothing", writer.writes)
	}
}

// countingZaiTransport answers every fetch with the #2010 probe's 13:16:20
// body and counts the fetches.
type countingZaiTransport struct {
	mu    sync.Mutex
	calls int
}

func (c *countingZaiTransport) Fetch(context.Context, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(`{"code":200,"success":true,"data":{"level":"lite","limits":[` +
		`{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":2,"remaining":1997,"percentage":1,"nextResetTime":1795284966038}]}}`)}, nil
}

// Remove (Stop) drops every cached Z.ai reading, so the next poll fetches
// again; Apply (Start) does nothing and never fails the grant.
func TestZaiAccountAPI_StopRevokesZaiKeys(t *testing.T) {
	transport := &countingZaiTransport{}
	api := newZaiAccountAPI(services.NewAccountPoller(), transport, nil)
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	cred := zaiaccountapi.Credential{Key: outbound.NewCredential("k"), Fingerprint: "fp"}
	granted := func() bool { return true }
	for i := 0; i < 2; i++ {
		if _, err := services.ZaiAccountRefresh(context.Background(), api.poller, transport, granted, "z1", cred); err != nil {
			t.Fatal(err)
		}
	}
	if transport.calls != 1 {
		t.Fatalf("fetches = %d before Stop, want 1 (the second poll is served from the cache)", transport.calls)
	}
	if err := api.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := services.ZaiAccountRefresh(context.Background(), api.poller, transport, granted, "z1", cred); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 2 {
		t.Fatalf("fetches = %d after Stop, want 2: Stop must have dropped the cached reading", transport.calls)
	}
}
