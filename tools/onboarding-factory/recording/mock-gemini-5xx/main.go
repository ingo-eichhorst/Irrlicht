// mock-gemini-5xx — minimal Gemini-compatible API stub for the
// gemini-cli/turn-aborted-by-error scenario.
//
// Gemini CLI 0.46.0 makes two kinds of calls per turn against
// GOOGLE_GEMINI_BASE_URL (honored on the api-key path):
//
//  1. a NumericalClassifierStrategy ROUTER call on `…:generateContent`
//     (non-stream) — we answer a LOW complexity score so the turn routes to
//     flash deterministically.
//  2. the MAIN turn on `…:streamGenerateContent` (SSE) — we return a
//     NON-RETRYABLE error so gemini aborts the turn and records it.
//
// Why a NON-retryable 400 (not a 500): gemini's retryWithBackoff treats
// 429/500/503 as retryable — it backs off, exhausts retries, and writes NO
// transcript line (verified empirically; same shape as the 429 cell). A
// non-retryable 400 INVALID_ARGUMENT aborts the turn immediately and gemini
// writes a `type:"error"` MessageRecord to the chat JSONL (PR #13300). That
// error line is exactly the load-bearing signal this known_failing cell turns
// on: the geminicli parser dispatches strictly on type (user/gemini handled,
// error skipped) and infers turn_done only from a final text-only `gemini`
// message — so the error line is dropped, no turn_done fires, and the session
// STICKS in working (daemon=bug).
//
// Usage:
//   go run ./tools/onboarding-factory/recording/mock-gemini-5xx/main.go --addr 127.0.0.1:18791
//
// The server listens until SIGINT/SIGTERM.

package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18791", "bind address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		path := r.URL.Path

		// Router (non-stream generateContent): answer a LOW complexity score so
		// the turn routes to flash. Never errors.
		if strings.Contains(path, ":generateContent") {
			log.Printf("router %s", path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, routerLowScore)
			return
		}

		// Main turn (streamGenerateContent): non-retryable 400 → gemini aborts
		// the turn and records a type:"error" line in the transcript.
		if strings.Contains(path, ":streamGenerateContent") {
			log.Printf("main-turn (abort) %s", path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, invalidArgument)
			return
		}

		log.Printf("unhandled %s %s", r.Method, path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 60 * time.Second}
	log.Printf("mock-gemini-5xx listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

const routerLowScore = `{"candidates":[{"content":{"role":"model","parts":[{"text":"{\"complexity_reasoning\":\"trivial direct reply\",\"complexity_score\":5}"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":8,"totalTokenCount":18},"modelVersion":"gemini-3.1-flash-lite"}`

// invalidArgument is a non-retryable 400 INVALID_ARGUMENT — gemini does NOT
// back off on it; it aborts the turn and records a type:"error" line.
const invalidArgument = `{"error":{"code":400,"message":"mid-turn non-retryable failure (mock-gemini-5xx)","status":"INVALID_ARGUMENT"}}`
