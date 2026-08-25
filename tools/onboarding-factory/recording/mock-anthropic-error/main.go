// mock-anthropic-error — an Anthropic-compatible API stub that fails on
// demand, for #1803's three provider-error scenarios:
//
//	2.22 provider-overloaded-retry     --status 529 --succeed-after 1
//	2.23 provider-overloaded-terminal  --status 529 --succeed-after 0
//	2.24 auth-credentials-rejected     --status 401 --error-type authentication_error
//
// ONE binary rather than three directories, because the three differ only in
// a status code, an error type and how many attempts fail before one works —
// and the per-cell recipe's new `mock.args` field is exactly the place for a
// difference that small (see run-cell.sh's mock hook). The older mocks in this
// tree predate that field and each hardcodes its own shape.
//
// Semantics of --succeed-after N:
//
//	N == 0  every counted POST fails, forever. The agent exhausts its retry
//	        ladder and gives up — a TERMINAL failure.
//	N >= 1  the first N counted POSTs fail, the (N+1)th streams a normal
//	        end_turn — the agent retries with backoff and the turn RECOVERS.
//
// Why the counter is on POSTs and not on turns: claude retries INSIDE one
// turn, so "attempt" and "request" are the same thing here. HEAD/OPTIONS and
// any non-POST probe are excluded so an SDK preflight cannot burn a slot —
// the same guard mock-anthropic-429 carries, for the same reason.
//
// --ignore-model exists because claude-code issues small side requests (topic
// titles, and similar) against a cheaper model on the SAME endpoint, and those
// would silently consume attempt slots and make --succeed-after mean something
// other than what it says. It is EMPTY by default rather than guessing a model
// name: every request's model is logged, so a recording that mis-counts is
// diagnosed from the mock's own log rather than from a guess baked in here.
//
// --retry-after is deliberately OFF by default. #1803's deliverable is the
// real dwell in the `error` state under the agent's OWN backoff (#1798 has no
// minimum hold and deferred the measurement to that recording); pinning a
// retry-after header would measure the header instead of the agent.
//
// Usage:
//
//	go run ./tools/onboarding-factory/recording/mock-anthropic-error/main.go \
//	  --addr 127.0.0.1:18767 --status 529 --succeed-after 1
//
// The server listens until SIGINT/SIGTERM.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18767", "bind address")
	status := flag.Int("status", 529, "HTTP status to fail with (529 overloaded, 401/403 auth, 500 provider)")
	errType := flag.String("error-type", "overloaded_error", "Anthropic error.type in the failure body")
	errMsg := flag.String("error-message", "Overloaded", "Anthropic error.message in the failure body")
	succeedAfter := flag.Int("succeed-after", 0, "number of counted POSTs that fail before one succeeds; 0 = never succeed")
	retryAfter := flag.String("retry-after", "", "value for the retry-after response header; empty = do not send one")
	ignoreModel := flag.String("ignore-model", "", "substring: requests whose model contains it are answered happily and NOT counted")
	flag.Parse()

	if *succeedAfter < 0 {
		log.Fatalf("--succeed-after must be >= 0, got %d", *succeedAfter)
	}
	if *status < 400 || *status > 599 {
		log.Fatalf("--status must be a 4xx/5xx failure code, got %d", *status)
	}

	cfg := failureConfig{
		status:       *status,
		errType:      *errType,
		errMsg:       *errMsg,
		succeedAfter: *succeedAfter,
		retryAfter:   *retryAfter,
		ignoreModel:  *ignoreModel,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", cfg.handleMessages)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 30 * time.Second}
	log.Printf("mock-anthropic-error listening on %s (status=%d type=%s succeed-after=%d)",
		*addr, *status, *errType, *succeedAfter)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

// failureConfig is the flag set, bound once so the handler is a method rather
// than a closure over eight pointers — which is also what keeps main() short
// enough to read.
type failureConfig struct {
	status       int
	errType      string
	errMsg       string
	succeedAfter int
	retryAfter   string
	ignoreModel  string

	failures atomic.Int64
}

// handleMessages is the whole mock: count, decide, answer.
func (c *failureConfig) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	model := modelOf(body)

	if c.ignoreModel != "" && strings.Contains(model, c.ignoreModel) {
		log.Printf("POST /v1/messages #0 model=%s — ignored (matches --ignore-model), answering happily", model)
		streamHappyPath(w)
		return
	}

	// Count first, then decide, so the log line and the decision can never
	// disagree about which attempt this was.
	n := c.failures.Add(1)
	if c.succeedAfter > 0 && n > int64(c.succeedAfter) {
		log.Printf("POST /v1/messages #%d model=%s — succeeding (--succeed-after %d)", n, model, c.succeedAfter)
		streamHappyPath(w)
		return
	}
	log.Printf("POST /v1/messages #%d model=%s — failing %d %s", n, model, c.status, c.errType)
	c.writeFailure(w)
}

// writeFailure emits the Anthropic-shaped error body.
func (c *failureConfig) writeFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if c.retryAfter != "" {
		w.Header().Set("retry-after", c.retryAfter)
	}
	w.WriteHeader(c.status)
	payload, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": c.errType, "message": c.errMsg},
	})
	if err != nil { // unreachable: the map is all plain values
		log.Printf("marshal failure body: %v", err)
		return
	}
	_, _ = w.Write(append(payload, '\n'))
}

// modelOf best-effort extracts the request's model for the log. A body it
// cannot parse yields "?" rather than an error: the mock's job is to fail the
// request in a controlled way, and a diagnostic that can abort the run would
// be worse than one that reads "?".
//
// The result goes through an ALLOWLIST because it is request-controlled data
// in a log the recording rig then greps: a model name containing a newline
// could forge additional log lines — including a counterfeit
// `POST /v1/messages #N`, which is exactly the line
// recipe_runtime_assert_mock_used counts to decide whether the agent really
// reached the mock. Forging those would let a request talk the rig into
// believing a recording is trustworthy.
func modelOf(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		return "?"
	}
	return sanitizeForLog(req.Model)
}

// safeModelName is an ALLOWLIST, not a filter. Every character a real
// Anthropic model id can contain is here and nothing else — no whitespace, no
// control characters, no newline.
var safeModelName = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,64}$`)

// sanitizeForLog returns s only when it matches the allowlist, and a fixed
// placeholder otherwise. An allowlist rather than the strip-the-bad-bytes pass
// this started as: a denylist is a claim that you enumerated every dangerous
// byte, and it is also invisible to a taint analyser, which is what kept this
// file's security rating at B after the first fix.
//
// The value is worth logging at all because `--succeed-after N` counts POSTs,
// and claude-code issues side requests against a cheaper model on the same
// endpoint — without the model in the log, a recording that mis-counts cannot
// be diagnosed from the mock's own output.
func sanitizeForLog(s string) string {
	if !safeModelName.MatchString(s) {
		return "<unprintable>"
	}
	return s
}

// streamHappyPath writes a minimal SSE response that satisfies claude's
// streaming JSON parser: message_start → content_block_start → one short
// content_block_delta carrying assistant text → content_block_stop →
// message_delta with stop_reason: end_turn → message_stop.
//
// Byte-for-byte the same shape mock-anthropic-429 emits — kept identical on
// purpose so a recording made against this mock and one made against that one
// differ only in the failure, never in what a healthy turn looks like.
func streamHappyPath(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("response writer is not a Flusher — cannot stream")
		return
	}

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
