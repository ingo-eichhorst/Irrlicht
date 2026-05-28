// mock-openai-429 — minimal OpenAI-compatible API stub for the
// opencode/token-quota-exhausted scenario (the openai-compatible provider
// opencode uses for LM Studio / OpenAI / IONOS speaks this wire format).
//
// GET  /v1/models            — advertises one model so opencode's provider
//                              discovery succeeds.
// POST /v1/chat/completions  — first request streams a normal "ok" completion
//                              (finish_reason: stop) so turn 1 lands cleanly
//                              (ready→working→ready). Every subsequent request
//                              returns --error-status (default 403) with the
//                              OpenAI insufficient_quota body, so opencode's
//                              quota-exhausted path fires on turn 2 and records
//                              the failure on the assistant message's `error`
//                              field.
//
// Why 403 and not the literal 429 the name suggests: real OpenAI returns 429
// for `insufficient_quota`, but the AI-SDK opencode wraps RETRIES every 429
// indefinitely (exponential backoff, 24+ requests over 180s, no terminal
// message.data.error ever written — verified live). To produce a terminal,
// observable quota error in the rig, the mock returns a status the SDK does
// NOT retry (403 default; pass --error-status=429 to reproduce the
// retry-forever behaviour for comparison). The body shape stays faithful to
// OpenAI's documented insufficient_quota error.
//
// Usage:
//   go run ./tools/agent-onboarding/recording/mock-openai-429/main.go --addr 127.0.0.1:18766
//
// Mirrors mock-anthropic-429 (the claudecode equivalent). Listens until
// SIGINT/SIGTERM.

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

const modelID = "mock-quota-model"

func main() {
	addr := flag.String("addr", "127.0.0.1:18766", "bind address")
	errorStatus := flag.Int("error-status", http.StatusForbidden, "HTTP status to return on requests after the first (default 403, non-retryable; set 429 to reproduce AI-SDK retry-forever behaviour)")
	flag.Parse()

	var reqCount atomic.Int64

	mux := http.NewServeMux()

	// Provider discovery: opencode/the AI SDK may GET the model list.
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model","created":0,"owned_by":"mock"}]}`, modelID)
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		n := reqCount.Add(1)
		log.Printf("POST /v1/chat/completions #%d", n)
		if n == 1 {
			streamHappyPath(w)
			return
		}
		// OpenAI's documented insufficient_quota body, returned with a status
		// the AI-SDK will NOT retry (403 default; see file header for why).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(*errorStatus)
		_, _ = fmt.Fprintln(w, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 30 * time.Second}
	log.Printf("mock-openai-429 listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

// streamHappyPath writes a minimal OpenAI streaming chat-completion: one
// content delta ("ok") then a finish_reason: stop chunk, then [DONE]. This is
// the SSE shape the @ai-sdk/openai-compatible provider opencode uses expects.
func streamHappyPath(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)

	write := func(data string) {
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	const created = 0
	// Role chunk, content chunk, then a stop chunk with usage.
	write(fmt.Sprintf(`{"id":"chatcmpl-mock-001","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`, created, modelID))
	write(fmt.Sprintf(`{"id":"chatcmpl-mock-001","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`, created, modelID))
	write(fmt.Sprintf(`{"id":"chatcmpl-mock-001","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":1,"total_tokens":13}}`, created, modelID))
	write("[DONE]")
}
