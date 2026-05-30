// mock-anthropic-5xx — minimal Anthropic-compatible API stub for the
// claudecode/turn-aborted-by-error scenario (Bucket A: provider 5xx
// AFTER partial stream, runtime appends synthesized error text to the
// transcript with a terminal stop_reason → IsAgentDone() fires normally).
//
// The first /v1/messages POST streams message_start + content_block_start
// + a single content_block_delta carrying assistant text, then sends an
// SSE `event: error` with an overloaded_error payload and closes the
// connection. Claude Code's streaming consumer treats the mid-stream
// error as a turn-fail-with-content event: it appends a synthesized
// error message to the transcript and terminates the turn cleanly.
//
// Subsequent POSTs (if claude retries) return the same shape, so a
// recipe can either drive one prompt (single failed turn) or two
// prompts (failed + retry-failed) without the mock running out of
// scripted responses.
//
// Usage:
//   go run ./tools/onboarding-factory/recording/mock-anthropic-5xx/main.go --addr 127.0.0.1:18766
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
	addr := flag.String("addr", "127.0.0.1:18766", "bind address")
	flag.Parse()

	var reqCount atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		n := reqCount.Add(1)
		log.Printf("POST /v1/messages #%d", n)
		streamPartialThenError(w)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 30 * time.Second}
	log.Printf("mock-anthropic-5xx listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

// streamPartialThenError writes the prefix of a normal SSE response and
// then emits an `event: error` payload (per Anthropic's streaming-error
// contract) instead of a clean message_delta+message_stop. The mid-
// stream error is the Bucket-A trigger: claude's streaming consumer
// sees partial content + a terminal error and writes a synthesized
// error message to the transcript with a non-end_turn stop_reason.
func streamPartialThenError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)

	write := func(event, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
	write("message_start", `{"type":"message_start","message":{"id":"msg_mock_5xx_001","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`)
	write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Working on"}}`)
	// Tiny pause so the partial content lands before the error event.
	time.Sleep(50 * time.Millisecond)
	write("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
}
