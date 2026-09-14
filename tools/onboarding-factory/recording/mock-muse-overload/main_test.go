package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleResponses_FailsFirstNThenSucceeds pins the mock's central
// contract: the first --succeed-after counted POSTs to /responses fail with
// the configured status, and the next one streams a happy-path completion.
// This is the behavior the whole scenario (muse retries, then recovers)
// depends on — see AGENTS.md's testing philosophy on why a defect test here
// is mutation-verified rather than merely present.
func TestHandleResponses_FailsFirstNThenSucceeds(t *testing.T) {
	cfg := &failureConfig{
		status:          429,
		errType:         "rate_limit_error",
		errMsg:          "rate limited (test)",
		succeedAfter:    2,
		ignoreSubstring: "reminder observer",
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
		rec := httptest.NewRecorder()
		cfg.handleResponses(rec, req)
		return rec
	}

	for i := 1; i <= cfg.succeedAfter; i++ {
		rec := post(`{"model":"muse-spark-1.2","input":[]}`)
		if rec.Code != cfg.status {
			t.Fatalf("request #%d: got status %d, want %d (the configured overload status)", i, rec.Code, cfg.status)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("request #%d: got Content-Type %q, want application/json", i, ct)
		}
	}

	rec := post(`{"model":"muse-spark-1.2","input":[]}`)
	if rec.Code != 200 {
		t.Fatalf("request #%d (N+1): got status %d, want 200 (the happy path)", cfg.succeedAfter+1, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("request #%d (N+1): got Content-Type %q, want text/event-stream", cfg.succeedAfter+1, ct)
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("request #%d (N+1): success body missing a response.completed event: %s", cfg.succeedAfter+1, rec.Body.String())
	}
}

// TestHandleResponses_IgnoresReminderObserverSideChannel pins the other
// half of the counting contract: a request whose body matches
// --ignore-substring (muse's own background "skill reminder observer"
// side-channel call — see the file header for how this was discovered live)
// is answered happily immediately and never consumes a counted slot, no
// matter how many of them arrive before the real turn's request.
func TestHandleResponses_IgnoresReminderObserverSideChannel(t *testing.T) {
	cfg := &failureConfig{
		status:          429,
		errType:         "rate_limit_error",
		errMsg:          "rate limited (test)",
		succeedAfter:    1,
		ignoreSubstring: "reminder observer",
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
		rec := httptest.NewRecorder()
		cfg.handleResponses(rec, req)
		return rec
	}

	// Several ignored side-channel calls, interleaved with the counted
	// stream, must not perturb the counted stream's own N+1 threshold.
	for i := 0; i < 5; i++ {
		rec := post(`{"model":"muse-spark-1.2","input":[{"role":"user","content":"You are a reminder observer for the main agent."}]}`)
		if rec.Code != 200 {
			t.Fatalf("ignored side-channel call #%d: got status %d, want 200 (never counted, always happy)", i, rec.Code)
		}
	}
	if cfg.requests.Load() != 0 {
		t.Fatalf("ignored side-channel calls incremented the counted-request counter: got %d, want 0", cfg.requests.Load())
	}

	rec := post(`{"model":"muse-spark-1.2","input":[]}`)
	if rec.Code != cfg.status {
		t.Fatalf("first REAL counted request: got status %d, want %d", rec.Code, cfg.status)
	}
	rec = post(`{"model":"muse-spark-1.2","input":[]}`)
	if rec.Code != 200 {
		t.Fatalf("second REAL counted request (N+1): got status %d, want 200", rec.Code)
	}
}
