package main

// Hardening pins added after review. The concurrency and label tests were
// seen red against the unserialized issueToken path before the fix; the
// rest exist because a deliberate mutation of the code they cover left the
// rest of the suite green (the repo rule: anything a change adds owes a
// mutation seen red).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"irrlicht/core/cmd/irrlichtrelay/push"
)

// mintCode mints one pairing code through the HTTP surface.
func mintCode(t *testing.T, env *pushEnv, bearer string) string {
	t.Helper()
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pairings", bearer, nil)
	if status != http.StatusCreated {
		t.Fatalf("mint: status %d, want 201 (%s)", status, body)
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("mint response: %v", err)
	}
	return resp.Code
}

func TestConcurrentPairingsAllPersist(t *testing.T) {
	// Two phones pairing at once must both end up with a persisted device
	// token: the pair handler is the tokens file's first concurrent writer
	// (the CLI is single-shot), and an unserialized load-append-save loses
	// the race's earlier records — the losing phone holds an HTTP-200
	// device token whose hash never reached disk, and its one-time code is
	// already burnt.
	env := newPushEnv(t, tokenSeed{label: "cli", workspace: "acme"})
	const phones = 8
	codes := make([]string, phones)
	for i := range codes {
		codes[i] = mintCode(t, env, env.tokens["cli"])
	}

	type result struct {
		status int
		body   []byte
		err    error
	}
	results := make([]result, phones)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]string{"code": codes[i]})
			resp, err := http.Post(env.srv.URL+"/api/v1/push/pair", contentTypeJSON, bytes.NewReader(payload))
			if err != nil {
				results[i] = result{err: err}
				return
			}
			defer resp.Body.Close()
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(resp.Body)
			results[i] = result{status: resp.StatusCode, body: buf.Bytes()}
		}(i)
	}
	wg.Wait()

	recs, err := loadTokens(env.tokensPath)
	if err != nil {
		t.Fatalf("loadTokens: %v", err)
	}
	persisted := make(map[string]bool, len(recs))
	for _, r := range recs {
		persisted[r.Hash] = true
	}
	for i, r := range results {
		if r.err != nil {
			t.Errorf("pair %d: request error: %v", i, r.err)
			continue
		}
		if r.status != http.StatusOK {
			t.Errorf("pair %d: status %d, want 200 (%s)", i, r.status, r.body)
			continue
		}
		var resp struct {
			Token   string `json:"token"`
			TokenID string `json:"token_id"`
		}
		if err := json.Unmarshal(r.body, &resp); err != nil {
			t.Errorf("pair %d: response: %v", i, err)
			continue
		}
		if !persisted[hashToken(resp.Token)] {
			t.Errorf("pair %d: device token %s answered 200 but its hash is not in tokens.json — the pairing was silently lost", i, resp.TokenID)
		}
	}
}

func TestPairLabelIsSanitized(t *testing.T) {
	// The label lands verbatim in tokens.json and `token list` prints it
	// into the operator's terminal, and the writer is a paired phone — a
	// valid code, not a license to redecorate a terminal or bloat the file.
	env := newPushEnv(t, tokenSeed{label: "cli"})
	code := mintCode(t, env, env.tokens["cli"])

	hostile := "\x1b[2Jphone\x00 of\ttrouble " + strings.Repeat("x", 200)
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pair", "", map[string]string{"code": code, "label": hostile})
	if status != http.StatusOK {
		t.Fatalf("pair: status %d, want 200 (%s)", status, body)
	}
	var resp struct {
		TokenID string `json:"token_id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	recs, err := loadTokens(env.tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.ID != resp.TokenID {
			continue
		}
		if n := utf8.RuneCountInString(r.Label); n > maxPairLabelRunes {
			t.Fatalf("stored label is %d runes, want at most %d", n, maxPairLabelRunes)
		}
		for _, ru := range r.Label {
			if unicode.IsControl(ru) {
				t.Fatalf("stored label %q carries a control character (%U) — `token list` prints it raw", r.Label, ru)
			}
		}
		return
	}
	t.Fatalf("device token %s not found in tokens.json", resp.TokenID)
}

func TestPairRateLimitAnswers429(t *testing.T) {
	// The 429/401 split is the wire contract the PWA codes against: a
	// rate-limited legitimate user must see "try later", not "your code is
	// wrong". Pinned at the HTTP surface — the service-level test cannot
	// see a wrong status constant.
	env := newPushEnv(t)
	for i := range 10 {
		status, _ := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pair", "", map[string]string{"code": "WRON-GGGG"})
		if status != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d, want the uniform 401", i, status)
		}
	}
	status, _ := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pair", "", map[string]string{"code": "WRON-GGGG"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("saturated failure window: status %d, want 429", status)
	}
}

func TestMintCapAnswers429(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "cli"})
	for i := range 32 {
		status, _ := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pairings", env.tokens["cli"], nil)
		if status != http.StatusCreated {
			t.Fatalf("mint %d: status %d, want 201", i, status)
		}
	}
	status, _ := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pairings", env.tokens["cli"], nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("mint past the outstanding-code cap: status %d, want 429", status)
	}
}

func TestSubscriptionRoutesRequireAuth(t *testing.T) {
	// Direct pins: the bearer gate on both subscription routes was
	// previously covered only via a registry-count side effect in an
	// idempotency test, which a legitimate rework of that test would
	// silently orphan.
	env := newPushEnv(t)
	if status, _ := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/subscriptions", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous subscription POST: status %d, want 401", status)
	}
	if status, _ := doPush(t, http.MethodDelete, env.srv.URL+"/api/v1/push/subscriptions", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous subscription DELETE: status %d, want 401", status)
	}
}

func TestRegisterPushRoutesRefusesServiceWithoutStore(t *testing.T) {
	// The "svc != nil implies store != nil" invariant spans two functions
	// in two files; a second construction site wiring a service without a
	// store would serve every push mutation route unauthenticated, all
	// callers sharing the "" registry slot. registerPushRoutes refuses.
	svc, err := push.NewService(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registerPushRoutes accepted a push service without an auth store")
		}
	}()
	registerPushRoutes(http.NewServeMux(), nil, svc, nil)
}

func TestRegisterPushRoutesRefusesServiceWithoutDispatcher(t *testing.T) {
	// Same invariant, other half: the test-notification endpoint sends
	// through the dispatcher that sends every real push
	// (docs/mobile-notifications-arc42.md §8.3). A push-enabled relay wired
	// without it would have to answer the one route built for doubt with a
	// 503, or grow a sender of its own — and a diagnostic with its own
	// sender proves nothing about the path it reports on.
	svc, err := push.NewService(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newAuthStore(filepath.Join(t.TempDir(), tokensFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registerPushRoutes accepted a push service with no dispatcher to send test notifications through")
		}
	}()
	registerPushRoutes(http.NewServeMux(), store, svc, nil)
}

func TestRegisterPushRoutesRefusesATypedNilDispatcher(t *testing.T) {
	// The sibling above passes a literal nil. This one passes the shape
	// runServe actually produces: a *pushObserver variable that happens to be
	// nil, stored in the testNotifier interface. That is a NON-nil interface
	// value holding a nil pointer, so a bare `notifier == nil` waves it
	// through and the route nil-derefs at request time — on the one endpoint
	// built to answer doubt, while somebody is using it to diagnose
	// something else.
	svc, err := push.NewService(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newAuthStore(filepath.Join(t.TempDir(), tokensFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registerPushRoutes accepted a typed-nil dispatcher — it would nil-deref at request time instead of refusing at wiring time")
		}
	}()
	registerPushRoutes(http.NewServeMux(), store, svc, (*pushObserver)(nil))
}
