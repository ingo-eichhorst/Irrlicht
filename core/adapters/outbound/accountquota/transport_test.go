package accountquota

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	outbound "irrlicht/core/ports/outbound"
)

// loopbackDialGuard wraps an *http.Transport's DialContext so this file's
// tests can ASSERT — never merely assume — that nothing they do reaches past
// 127.0.0.1/::1 (AGENTS.md: "the suite must NEVER leave the machine ...
// assert that, do not assume it"). dials counts every attempted dial
// (the vacuity half: a test asserting zero non-loopback dials because it
// dialed NOTHING would be worthless), refused counts any that were NOT
// loopback and would have been allowed through to net.Dialer.
type loopbackDialGuard struct {
	dials   atomic.Int64
	refused atomic.Int64
}

var errNonLoopbackDialRefused = errors.New("test dial guard: refusing a non-loopback address")

// newLoopbackOnlyTransport builds an *http.Transport whose DialContext
// refuses (and counts) any address that does not resolve to a loopback IP,
// and counts every dial it allows through — so a test can assert BOTH "at
// least one dial happened" (the guard actually exercised the network) and
// "zero were refused" (nothing this test did could have left the machine,
// checked structurally rather than by trusting the test's own httptest URLs).
func newLoopbackOnlyTransport(g *loopbackDialGuard, tlsConfig *tls.Config) *http.Transport {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.TLSClientConfig = tlsConfig
	rt.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			g.refused.Add(1)
			return nil, errNonLoopbackDialRefused
		}
		g.dials.Add(1)
		return dialer.DialContext(ctx, network, addr)
	}
	return rt
}

// certPool returns an x509.CertPool trusting every server's TLS certificate,
// so a client built from it can complete a handshake against ANY of them —
// needed because the redirect test's whole point is to let the (refused, or
// under mutation, followed) request reach a SECOND server.
func certPool(servers ...*httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	for _, s := range servers {
		pool.AddCert(s.Certificate())
	}
	return &tls.Config{RootCAs: pool}
}

func destinationFor(s *httptest.Server) outbound.FixedDestination {
	return outbound.FixedDestination{Key: "primary", URL: s.URL}
}

func testRequest(sessionID string) outbound.AccountQuotaRequest {
	return outbound.AccountQuotaRequest{
		DestinationKey: "primary",
		Credential:     outbound.NewCredential("test-secret-value"),
		Auth:           outbound.AuthMethod{Header: "Authorization", Prefix: "Bearer "},
		SessionID:      sessionID,
	}
}

func TestHTTPTransport_RefusesCrossHostRedirect(t *testing.T) {
	var redirectHits atomic.Int64
	redirectServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"redirected":true}`))
	}))
	defer redirectServer.Close()

	primary := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectServer.URL+"/quota", http.StatusFound)
	}))
	defer primary.Close()

	guard := &loopbackDialGuard{}
	rt := newLoopbackOnlyTransport(guard, certPool(primary, redirectServer))

	tr, err := newHTTPTransport([]outbound.FixedDestination{destinationFor(primary)}, rt)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}

	_, err = tr.Fetch(t.Context(), testRequest("s1"))

	var qerr *outbound.QuotaError
	if !errors.As(err, &qerr) || qerr.Reason != outbound.QuotaFailureRedirectRefused {
		t.Fatalf("expected a redirect_refused QuotaError, got err=%v", err)
	}
	if got := redirectHits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s), want 0 — the transport followed a refused redirect", got)
	}
	if guard.dials.Load() < 1 {
		t.Fatal("vacuity: the dial guard recorded zero dials — this test exercised no network at all")
	}
	if guard.refused.Load() != 0 {
		t.Fatalf("dial guard refused %d non-loopback dial(s) — this test attempted to leave the machine", guard.refused.Load())
	}
}

func TestHTTPTransport_TimesOut(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client gives up
	}))
	defer server.Close()

	guard := &loopbackDialGuard{}
	rt := newLoopbackOnlyTransport(guard, certPool(server))
	parsed, err := parseDestinations([]outbound.FixedDestination{destinationFor(server)})
	if err != nil {
		t.Fatalf("parseDestinations: %v", err)
	}
	tr := &HTTPTransport{
		destinations: parsed,
		client: &http.Client{
			Timeout:       50 * time.Millisecond,
			Transport:     rt,
			CheckRedirect: refuseAllRedirects,
		},
	}

	_, err = tr.Fetch(t.Context(), testRequest("s1"))
	var qerr *outbound.QuotaError
	if !errors.As(err, &qerr) || qerr.Reason != outbound.QuotaFailureTimeout {
		t.Fatalf("expected a timeout QuotaError, got err=%v", err)
	}
	if guard.dials.Load() < 1 {
		t.Fatal("vacuity: the dial guard recorded zero dials")
	}
	if guard.refused.Load() != 0 {
		t.Fatalf("dial guard refused %d non-loopback dial(s)", guard.refused.Load())
	}
}

func TestHTTPTransport_AuthRejectedNeverAttachesBodyOrSecret(t *testing.T) {
	const secretInBody = "leaked-secret-marker-should-never-surface"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + secretInBody + `"}`))
	}))
	defer server.Close()

	guard := &loopbackDialGuard{}
	rt := newLoopbackOnlyTransport(guard, certPool(server))
	tr, err := newHTTPTransport([]outbound.FixedDestination{destinationFor(server)}, rt)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}

	req := testRequest("s1")
	_, err = tr.Fetch(t.Context(), req)
	var qerr *outbound.QuotaError
	if !errors.As(err, &qerr) || qerr.Reason != outbound.QuotaFailureAuthRejected {
		t.Fatalf("expected an auth_rejected QuotaError, got err=%v", err)
	}
	if strings.Contains(err.Error(), secretInBody) {
		t.Fatalf("error text leaked the response body: %v", err)
	}
	if strings.Contains(err.Error(), req.Credential.Reveal()) {
		t.Fatalf("error text leaked the credential: %v", err)
	}
	if guard.refused.Load() != 0 {
		t.Fatalf("dial guard refused %d non-loopback dial(s)", guard.refused.Load())
	}
}

func TestHTTPTransport_SuccessReturnsBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret-value" {
			t.Errorf("Authorization header = %q, want Bearer test-secret-value", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"units_used":42}`))
	}))
	defer server.Close()

	guard := &loopbackDialGuard{}
	rt := newLoopbackOnlyTransport(guard, certPool(server))
	tr, err := newHTTPTransport([]outbound.FixedDestination{destinationFor(server)}, rt)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}

	resp, err := tr.Fetch(t.Context(), testRequest("s1"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if !bytes.Contains(resp.Body, []byte("units_used")) {
		t.Errorf("Body = %q, want it to contain units_used", resp.Body)
	}
}

func TestHTTPTransport_UnknownDestinationRefusedBeforeAnyDial(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server was contacted for an unknown destination key")
	}))
	defer server.Close()

	guard := &loopbackDialGuard{}
	rt := newLoopbackOnlyTransport(guard, certPool(server))
	tr, err := newHTTPTransport([]outbound.FixedDestination{destinationFor(server)}, rt)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}

	req := testRequest("s1")
	req.DestinationKey = "not-a-reviewed-destination"
	_, err = tr.Fetch(t.Context(), req)
	var qerr *outbound.QuotaError
	if !errors.As(err, &qerr) || qerr.Reason != outbound.QuotaFailureUnknownDestination {
		t.Fatalf("expected an unknown_destination QuotaError, got err=%v", err)
	}
	if guard.dials.Load() != 0 {
		t.Fatalf("dialed %d time(s) for an unknown destination — should never reach the network", guard.dials.Load())
	}
}

func TestNewHTTPTransport_RejectsNonHTTPS(t *testing.T) {
	if _, err := NewHTTPTransport([]outbound.FixedDestination{{Key: "p", URL: "http://example.invalid/quota"}}); err == nil {
		t.Fatal("expected NewHTTPTransport to reject a plain-http destination")
	}
}

// readBounded is exercised directly (no server) because the property under
// test — the READ itself never pulls more than the ceiling from its source —
// is invisible at the classification boundary: an unbounded io.ReadAll
// followed by the same len(data) check classifies an oversized body
// identically to the bounded version, so a test that only checks the
// returned error would pass under mutation #2. countingReader is what makes
// the actual bytes PULLED observable.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestReadBounded_NeverPullsPastTheCeilingPlusOne(t *testing.T) {
	huge := bytes.Repeat([]byte("x"), responseSizeCeiling*5)
	cr := &countingReader{r: bytes.NewReader(huge)}

	_, err := readBounded(cr)
	var qerr *outbound.QuotaError
	if !errors.As(err, &qerr) || qerr.Reason != outbound.QuotaFailureResponseTooLarge {
		t.Fatalf("expected a response_too_large QuotaError, got err=%v", err)
	}
	if cr.n > responseSizeCeiling+1 {
		t.Fatalf("readBounded pulled %d bytes from its source, want at most %d — the read itself is unbounded", cr.n, responseSizeCeiling+1)
	}
}

func TestReadBounded_AllowsExactlyTheCeiling(t *testing.T) {
	exact := bytes.Repeat([]byte("y"), responseSizeCeiling)
	data, err := readBounded(bytes.NewReader(exact))
	if err != nil {
		t.Fatalf("readBounded rejected a body of exactly the ceiling: %v", err)
	}
	if len(data) != responseSizeCeiling {
		t.Fatalf("len(data) = %d, want %d", len(data), responseSizeCeiling)
	}
}

// TestCredential_RevealHasOneCallSite is what makes accountquota.go's "the
// one call site" comment a checked claim rather than an assertion nobody
// re-runs: it greps this package's own non-test sources.
func TestCredential_RevealHasOneCallSite(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	count := 0
	var foundFile string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		n := strings.Count(string(data), ".Reveal()")
		if n > 0 {
			foundFile = name
		}
		count += n
	}
	if count != 1 {
		t.Fatalf("found %d call(s) to .Reveal() in this package's non-test sources (last seen in %s), want exactly 1 — "+
			"a second call site is a second place a credential can leak into a log or an error", count, foundFile)
	}
}

// TestHTTPTransport_RefusesEmptyCredentialBeforeDialing closes the
// test-coverage gap found by review (#2003): Fetch's "empty credential"
// guard (req.Credential.IsZero()) was shipped exercised by no test at all —
// dead code under test, even though it's a guard AGENTS.md's testing
// philosophy says earns its place. Also confirms the guard's failure
// reason is QuotaFailureCredential (review's finding #5's fix), not
// QuotaFailureNetwork.
func TestHTTPTransport_RefusesEmptyCredentialBeforeDialing(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server was contacted despite an empty credential")
	}))
	defer server.Close()

	guard := &loopbackDialGuard{}
	rt := newLoopbackOnlyTransport(guard, certPool(server))
	tr, err := newHTTPTransport([]outbound.FixedDestination{destinationFor(server)}, rt)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}

	req := testRequest("s1")
	req.Credential = outbound.Credential{} // zero value: IsZero() == true
	_, err = tr.Fetch(t.Context(), req)
	var qerr *outbound.QuotaError
	if !errors.As(err, &qerr) || qerr.Reason != outbound.QuotaFailureCredential {
		t.Fatalf("expected a credential QuotaError, got err=%v", err)
	}
	if guard.dials.Load() != 0 {
		t.Fatalf("dialed %d time(s) for an empty credential — should never reach the network", guard.dials.Load())
	}
}

// TestHTTPTransport_AuthRejectedIgnoresOversizedBody is the regression found
// by review (#2003): the bounded read used to run BEFORE the status-code
// classification, so a 401/403 with a body over the size ceiling reported
// response_too_large instead of auth_rejected — losing exactly the signal
// QuotaFailureAuthRejected's own doc comment says the poller must act on
// specially. Status is now classified before any body read is attempted.
func TestHTTPTransport_AuthRejectedIgnoresOversizedBody(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), responseSizeCeiling*3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(oversized)
	}))
	defer server.Close()

	guard := &loopbackDialGuard{}
	rt := newLoopbackOnlyTransport(guard, certPool(server))
	tr, err := newHTTPTransport([]outbound.FixedDestination{destinationFor(server)}, rt)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}

	_, err = tr.Fetch(t.Context(), testRequest("s1"))
	var qerr *outbound.QuotaError
	if !errors.As(err, &qerr) || qerr.Reason != outbound.QuotaFailureAuthRejected {
		t.Fatalf("expected an auth_rejected QuotaError even with an oversized body, got err=%v", err)
	}
}
