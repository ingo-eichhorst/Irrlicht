// mock-anthropic-429.go — minimal Anthropic-compatible API stub for the
// claudecode/token-quota-exhausted scenario.
//
// First /v1/messages request: streams a normal end_turn response so the
// agent's first turn lands cleanly (state ready→working→ready).
// Second /v1/messages request: returns HTTP 429 so claude's quota-exhausted
// path fires (state working→ready without sticking).
//
// Usage:
//   go run ./tools/mock-anthropic-429.go --addr 127.0.0.1:18765
//
// The server listens until SIGINT/SIGTERM.

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

func main() {
	addr := flag.String("addr", "127.0.0.1:18765", "bind address")
	flag.Parse()

	var reqCount atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		// Only POST should consume the request counter. HEAD/OPTIONS preflights
		// or SDK probes would otherwise burn the happy-path slot.
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		n := reqCount.Add(1)
		log.Printf("POST /v1/messages #%d", n)
		if n == 1 {
			streamHappyPath(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("anthropic-ratelimit-unified-status", "allowed_warning")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintln(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit (https://docs.anthropic.com/en/api/rate-limits). Please retry later."}}`)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 30 * time.Second}
	log.Printf("mock-anthropic-429 listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

// streamHappyPath writes a minimal SSE response that satisfies claude's
// streaming JSON parser: message_start → content_block_start → one short
// content_block_delta carrying assistant text → content_block_stop →
// message_delta with stop_reason: end_turn → message_stop.
func streamHappyPath(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)

	write := func(event, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
	write("message_start", `{"type":"message_start","message":{"id":"msg_mock_001","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`)
	write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
	write("content_block_stop", `{"type":"content_block_stop","index":0}`)
	write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`)
	write("message_stop", `{"type":"message_stop"}`)
}
