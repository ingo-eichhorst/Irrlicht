package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleChatCompletionsFailsFirstNThenSucceeds(t *testing.T) {
	cfg := &failureConfig{
		status:          http.StatusServiceUnavailable,
		errType:         "server_error",
		errCode:         "overloaded",
		errMsg:          "overloaded (test)",
		succeedAfter:    2,
		ignoreSubstring: "create a concise title",
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		rec := httptest.NewRecorder()
		cfg.handleChatCompletions(rec, req)
		return rec
	}

	for i := 1; i <= cfg.succeedAfter; i++ {
		rec := post(`{"model":"mock-model","messages":[{"role":"user","content":"work"}],"stream":true}`)
		if rec.Code != cfg.status {
			t.Fatalf("request #%d: got status %d, want %d", i, rec.Code, cfg.status)
		}
		if !strings.Contains(rec.Body.String(), `"type":"server_error"`) {
			t.Fatalf("request #%d: failure body missing error type: %s", i, rec.Body.String())
		}
	}

	rec := post(`{"model":"mock-model","messages":[{"role":"user","content":"work"}],"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("request N+1: got status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("request N+1: got Content-Type %q, want text/event-stream", got)
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("request N+1: completion is missing [DONE]: %s", rec.Body.String())
	}
}

func TestHandleChatCompletionsIgnoresTitleSideChannel(t *testing.T) {
	cfg := &failureConfig{
		status:          http.StatusServiceUnavailable,
		errType:         "server_error",
		errCode:         "overloaded",
		errMsg:          "overloaded (test)",
		succeedAfter:    1,
		ignoreSubstring: "create a concise title",
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		rec := httptest.NewRecorder()
		cfg.handleChatCompletions(rec, req)
		return rec
	}

	for i := 0; i < 3; i++ {
		rec := post(`{"model":"mock-model","messages":[{"role":"system","content":"Create a concise title for an AI coding-assistant session."}],"stream":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("ignored title request #%d: got status %d, want 200", i+1, rec.Code)
		}
	}
	if got := cfg.requests.Load(); got != 0 {
		t.Fatalf("ignored title requests incremented the counter: got %d, want 0", got)
	}

	if rec := post(`{"model":"mock-model","messages":[{"role":"user","content":"work"}],"stream":true}`); rec.Code != cfg.status {
		t.Fatalf("first counted request: got status %d, want %d", rec.Code, cfg.status)
	}
	if rec := post(`{"model":"mock-model","messages":[{"role":"user","content":"work"}],"stream":true}`); rec.Code != http.StatusOK {
		t.Fatalf("second counted request: got status %d, want 200", rec.Code)
	}
}

func TestHandleChatCompletionsRejectsNonPost(t *testing.T) {
	cfg := &failureConfig{}
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	cfg.handleChatCompletions(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: got status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
