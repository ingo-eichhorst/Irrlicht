package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamHappyPathEscapesContentInEveryChunk(t *testing.T) {
	content := "quoted \"text\"\n\ndata: injected"
	rec := httptest.NewRecorder()
	streamHappyPath(rec, content)
	chunks := decodeSuccessStream(t, rec.Body.String())
	if got := chunks[1].Choices[0].Delta["content"]; got != content {
		t.Fatalf("content was changed: %q", got)
	}
}

type successChunk struct {
	Choices []struct {
		Delta map[string]string `json:"delta"`
	} `json:"choices"`
}

func decodeSuccessStream(t *testing.T, body string) []successChunk {
	t.Helper()
	frames := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(frames) != 4 {
		t.Fatalf("got %d stream frames, want four", len(frames))
	}
	if frames[3] != "data: [DONE]" {
		t.Fatalf("missing final stream marker: %q", frames[3])
	}
	var chunks []successChunk
	for _, frame := range frames[:3] {
		chunks = append(chunks, decodeSuccessChunk(t, frame))
	}
	return chunks
}

func decodeSuccessChunk(t *testing.T, frame string) successChunk {
	t.Helper()
	if !strings.HasPrefix(frame, "data: ") {
		t.Fatalf("not an SSE data frame: %q", frame)
	}
	var chunk successChunk
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &chunk); err != nil {
		t.Fatalf("stream frame is not JSON: %v", err)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("stream frame has %d choices, want one", len(chunk.Choices))
	}
	return chunk
}

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
		rec := post(`{"model":"mock-model","messages":[{"role":"system","content":"Create a concise title for an AI coding-assistant session from the supplied human messages."},{"role":"user","content":"Generate the session title from this JSON array of human messages:\n[]"}],"stream":true}`)
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

func TestHandleChatCompletionsCountsUserTextThatMentionsTitle(t *testing.T) {
	cfg := &failureConfig{
		status:          http.StatusUnauthorized,
		errType:         "authentication_error",
		errCode:         "invalid_api_key",
		succeedAfter:    1,
		ignoreSubstring: "create a concise title",
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"mock-model","messages":[{"role":"user","content":"Please create a concise title for my note."}],"stream":true}`,
	))
	response := httptest.NewRecorder()
	cfg.handleChatCompletions(response, request)
	if response.Code != cfg.status || cfg.requests.Load() != 1 {
		t.Fatalf("user request bypassed configured failure: status=%d requests=%d, want %d and 1", response.Code, cfg.requests.Load(), cfg.status)
	}
}

func TestTitleRequestRecognizesTextPartContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"Create a concise title for an AI coding-assistant session from the supplied human messages."},{"role":"user","content":[{"type":"text","text":"Generate the session title from this JSON array of human messages:\n[]"}]}]}`)
	if !isTitleRequest(body, "create a concise title") {
		t.Fatal("title request with a text content part was counted as primary traffic")
	}
	if isTitleRequest([]byte(`{"messages":[{"role":"user","content":"Create a concise title"}]}`), "create a concise title") {
		t.Fatal("normal user text was classified as a title request")
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

func TestHandleChatCompletionsCanStreamDeterministicOversizedContent(t *testing.T) {
	cfg := &failureConfig{
		alwaysSucceed: true,
		successBytes:  10_199,
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"mock-model","messages":[{"role":"user","content":"work"}],"stream":true}`,
	))
	rec := httptest.NewRecorder()
	cfg.handleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), strings.Repeat("x", cfg.successBytes)) {
		t.Fatalf("streamed content does not contain the requested %d-byte payload", cfg.successBytes)
	}
	if got := cfg.requests.Load(); got != 1 {
		t.Fatalf("counted requests = %d, want 1", got)
	}
}
