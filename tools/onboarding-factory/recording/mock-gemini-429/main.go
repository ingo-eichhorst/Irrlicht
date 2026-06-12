// mock-gemini-429 — minimal Gemini-compatible API stub for the
// gemini-cli/token-quota-exhausted scenario.
//
// Gemini CLI 0.46.0 makes TWO kinds of calls per user turn against
// GOOGLE_GEMINI_BASE_URL (the api-key path honors that env):
//
//  1. a NumericalClassifierStrategy ROUTER call on
//     `…/<router-model>:generateContent` (non-stream) that expects a
//     structured-JSON complexity score — we always answer a LOW score so the
//     router routes the turn to flash (cheap, deterministic) rather than pro.
//  2. the MAIN turn on `…/<model>:streamGenerateContent` (SSE).
//
// Only the MAIN-turn (streamGenerateContent) requests are counted:
//   main #1  → a normal SSE turn (STOP) so the first turn lands ready.
//   main #2+ → HTTP 429 RESOURCE_EXHAUSTED so gemini's quota-exhausted path
//              fires: it retries with backoff, gives up, and settles the
//              session ready with a text-only turn_done (no stuck working).
//
// Router calls are NEVER counted or 429'd — a 429 on the router would abort
// before the turn the scenario is about.
//
// Usage:
//   go run ./tools/onboarding-factory/recording/mock-gemini-429/main.go --addr 127.0.0.1:18790
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
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18790", "bind address")
	flag.Parse()

	var mainTurns atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		path := r.URL.Path

		// Router (non-stream generateContent): always answer a LOW complexity
		// score so the turn routes to flash. Not counted.
		if strings.Contains(path, ":generateContent") {
			log.Printf("router %s", path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, routerLowScore)
			return
		}

		// Main turn (streamGenerateContent / SSE).
		if strings.Contains(path, ":streamGenerateContent") {
			n := mainTurns.Add(1)
			log.Printf("main-turn #%d %s", n, path)
			if n == 1 {
				streamHappyPath(w)
				return
			}
			// Second+ main turn → 429 RESOURCE_EXHAUSTED (Gemini wire shape).
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, resourceExhausted)
			return
		}

		log.Printf("unhandled %s %s", r.Method, path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 60 * time.Second}
	log.Printf("mock-gemini-429 listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

// routerLowScore is the structured-JSON response the NumericalClassifierStrategy
// router expects (responseJsonSchema {complexity_reasoning, complexity_score}).
// A low score routes the turn to flash.
const routerLowScore = `{"candidates":[{"content":{"role":"model","parts":[{"text":"{\"complexity_reasoning\":\"trivial direct reply\",\"complexity_score\":5}"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":8,"totalTokenCount":18},"modelVersion":"gemini-3.1-flash-lite"}`

// resourceExhausted is Gemini's 429 RESOURCE_EXHAUSTED error body.
const resourceExhausted = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED"}}`

// streamHappyPath writes a minimal SSE response that satisfies gemini's
// streamGenerateContent parser: one text chunk, then a final chunk carrying
// finishReason STOP + usageMetadata.
func streamHappyPath(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	emit := func(data string) {
		fmt.Fprintf(w, "data: %s\r\n\r\n", data)
		flusher.Flush()
	}
	emit(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"index":0}]}`)
	emit(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":1,"totalTokenCount":13},"modelVersion":"gemini-3.5-flash"}`)
}
