// mock-muse-overload — a stateful POST /responses stub for the
// muse/provider-overloaded-retry scenario (2.22): fails the first N counted
// requests with a retryable overload status, then streams a normal
// completion, so muse's Meta-provider client retries with backoff and the
// turn RECOVERS (unlike a dead port, which can fail but never recover).
//
// Why this mock had to be built new rather than reused (2-22's committed
// assessment, replaydata/agents/muse/scenarios/2-22_provider-overloaded-retry
// /metadata.json): muse's Meta client POSTs to /responses and speaks an
// OpenAI-Responses-API-shaped SSE stream — confirmed LIVE on this machine by
// pointing a logging capture proxy at --base-url and reading the raw request:
// POST /responses, Accept: text/event-stream, Content-Type: application/json,
// body {"model":...,"input":[...],"instructions":...,"tools":...,
// "reasoning":...,"stream":true,...}. None of the six existing mocks serve
// that path (mock-anthropic-* serve /v1/messages, mock-openai-429 serves
// /v1/chat/completions, mock-gemini-* serve Gemini's path shapes), and 2-14/
// 2-23's dead-port mechanism cannot recover (nothing is listening, so it
// never succeeds) — this scenario needs the Nth+1 request to actually work.
//
// Which statuses muse treats as retryable (live-confirmed, this machine,
// muse-bin 1.2.1-R2847.1): pointing --base-url at a mock returning HTTP 429
// produces "Meta Model API rate limited (HTTP 429) · retrying in Ns ·
// attempt N/10" in the TUI; a mock returning HTTP 503 produces "Meta Model
// API server error (HTTP 503) · retrying in Ns · attempt N/10" — both
// classified purely from the status code (a deliberately generic error body
// was accepted as either shape without muse inspecting its `type`/`code`
// fields). This matches real production evidence: scanning every session
// under ~/.local/share/muse/sessions on this machine for genuine
// phase:"retry_scheduled" task-status records (2-22's assessment) found
// http_status:429/error_kind:"rate_limited" overwhelmingly, plus one
// http_status:504/error_kind:"server" example — i.e. both 4xx rate-limiting
// and 5xx server overload drive the same retry path. Default here is 429
// (the dominant real-world shape); --status accepts 503 (or any 4xx/5xx) to
// reproduce the 5xx path instead.
//
// The "reminder observer" side channel (why counting is per-purpose, not
// global): live capture showed muse issues its OWN background POST
// /responses calls per turn — a "skill reminder observer" sub-call deciding
// whether to remind the main agent to load a skill — concurrently with the
// visible turn's model call, all hitting the SAME endpoint. Its request
// bodies are recognizable by the literal substring "reminder observer" in a
// user-role message (confirmed across two distinct body shapes this mock
// observed, one ~8KB, one ~72KB); the real turn's request body never
// contains that substring. A single shared request counter would let this
// side channel's own attempts silently consume the N failure slots meant
// for the visible turn (or vice versa), making --succeed-after mean
// something nondeterministic depending on scheduling. So requests matching
// --ignore-substring (default "reminder observer", case-insensitive) are
// answered with an immediate happy-path stream and are NOT counted — the
// same shape mock-anthropic-error's --ignore-model gives claude-code's own
// cheap side requests, for the identical reason.
//
// /muse-code/models and /muse-code/config?channel=public: muse probes both
// at startup, before any turn (live-observed). This mock serves the exact
// bodies captured from this machine's own on-disk caches
// (~/.local/share/muse/model-catalog and .../feature-config) so the shape is
// drawn from muse's own real state rather than invented. Serving those
// bodies verbatim did NOT clear muse's own "Could not refresh the model
// catalog (Provider returned malformed response data.)" startup warning —
// live-confirmed across multiple runs, including with the catalog body
// reduced to a bare rows array — but this is COSMETIC and non-blocking:
// muse falls back to its bundled model list and every live run in this
// file's own end-to-end verification still retried and completed the turn
// normally. The live warning text is reproduced here as a documented,
// investigated gap, not silently swallowed.
//
// Semantics of --succeed-after N (identical contract to mock-anthropic-error):
//
//	N == 0  every counted POST fails, forever — the agent exhausts its retry
//	        ladder (muse's own ceiling: max_attempts 10, live-observed) and
//	        gives up. Use this for a provider-overloaded-terminal sibling.
//	N >= 1  the first N counted POSTs fail, the (N+1)th streams a normal
//	        completion — the agent retries with backoff and the turn
//	        RECOVERS. Default 2, per 2-22's recipe (well within muse's
//	        observed 10-attempt ceiling, fast and deterministic to record).
//
// Usage:
//
//	go run ./tools/onboarding-factory/recording/mock-muse-overload/main.go \
//	  --addr 127.0.0.1:18801 --status 429 --succeed-after 2
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
	"strings"
	"sync/atomic"
	"time"
)

// modelsCatalogBody is the verbatim response body this machine's muse cached
// for its meta/tbh model catalog (~/.local/share/muse/model-catalog), used to
// answer the GET /muse-code/models startup probe.
const modelsCatalogBody = `{"schema_version": 1, "provider_id": "meta", "profile_id": "tbh", "source": "provider_catalog", "rows": [{"model_id": "muse-spark-1.3", "display_label": "muse-spark-1.3", "provider_id": "meta", "profile_id": "tbh", "visibility": "visible", "release_date": "2026-09-02", "display_order": null, "is_current": false, "is_default": false, "roles": [], "context_limit": 1007997, "output_limit": 128000, "description": null, "cost": null, "reasoning_effort_variants": [{"tier": "minimal", "description": null}, {"tier": "low", "description": null}, {"tier": "medium", "description": null}, {"tier": "high", "description": null}, {"tier": "xhigh", "description": null}, {"tier": "max", "description": null}]}, {"model_id": "muse-spark-1.3-contributor", "display_label": "muse-spark-1.3-contributor", "provider_id": "meta", "profile_id": "tbh", "visibility": "visible", "release_date": "2026-09-02", "display_order": null, "is_current": false, "is_default": true, "roles": [], "context_limit": 1007997, "output_limit": 128000, "description": "Your content, including inter-session messages, may be used for product improvement.", "cost": null, "reasoning_effort_variants": [{"tier": "minimal", "description": null}, {"tier": "low", "description": null}, {"tier": "medium", "description": null}, {"tier": "high", "description": null}, {"tier": "xhigh", "description": "Use this for deepest analysis and complex fixes."}, {"tier": "max", "description": null}]}, {"model_id": "muse-spark-1.2", "display_label": "muse-spark-1.2", "provider_id": "meta", "profile_id": "tbh", "visibility": "visible", "release_date": "2026-08-05", "display_order": null, "is_current": true, "is_default": false, "roles": [], "context_limit": 1007997, "output_limit": 128000, "description": null, "cost": null, "reasoning_effort_variants": [{"tier": "minimal", "description": null}, {"tier": "low", "description": null}, {"tier": "medium", "description": null}, {"tier": "high", "description": null}, {"tier": "xhigh", "description": "Use this for deepest analysis and complex fixes."}]}, {"model_id": "muse-spark-1.2-contributor", "display_label": "muse-spark-1.2-contributor", "provider_id": "meta", "profile_id": "tbh", "visibility": "visible", "release_date": "2026-08-05", "display_order": null, "is_current": false, "is_default": false, "roles": [], "context_limit": 1007997, "output_limit": 128000, "description": "Your content, including inter-session messages, may be used for product improvement.", "cost": null, "reasoning_effort_variants": [{"tier": "minimal", "description": null}, {"tier": "low", "description": null}, {"tier": "medium", "description": null}, {"tier": "high", "description": null}, {"tier": "xhigh", "description": "Use this for deepest analysis and complex fixes."}]}]}`

// featureConfigBody is the verbatim response body this machine's muse cached
// for the public feature-config channel (~/.local/share/muse/feature-config),
// used to answer the GET /muse-code/config?channel=public startup probe.
const featureConfigBody = `{"schema_version": 1, "ttl_seconds": 3600, "gates": {"local_session_messaging": true, "monitor": false, "plugins": false, "subscription_launch": true, "subscription_upsell": true, "voice": true, "voice_default_on": true, "web_fetch": true, "workflow_api_v2_rollout": false, "workflow_tool": true}, "killed_slash_commands": []}`

func main() {
	addr := flag.String("addr", "127.0.0.1:18801", "bind address")
	status := flag.Int("status", 429, "HTTP status to fail with (429 rate-limited, 503 server-overloaded — both live-confirmed retryable; see file header)")
	errType := flag.String("error-type", "rate_limit_error", "error.type in the failure body")
	errMsg := flag.String("error-message", "Rate limited by the Meta model API (mock).", "error.message in the failure body")
	succeedAfter := flag.Int("succeed-after", 2, "number of counted POSTs that fail before one succeeds; 0 = never succeed")
	ignoreSubstring := flag.String("ignore-substring", "reminder observer", "case-insensitive substring: /responses requests whose body contains it are muse's own background side-channel calls (see file header) — answered happily and NOT counted; empty disables the filter")
	flag.Parse()

	if *succeedAfter < 0 {
		log.Fatalf("--succeed-after must be >= 0, got %d", *succeedAfter)
	}
	if *status < 400 || *status > 599 {
		log.Fatalf("--status must be a 4xx/5xx failure code, got %d", *status)
	}

	cfg := failureConfig{
		status:          *status,
		errType:         *errType,
		errMsg:          *errMsg,
		succeedAfter:    *succeedAfter,
		ignoreSubstring: strings.ToLower(*ignoreSubstring),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/responses", cfg.handleResponses)
	mux.HandleFunc("/muse-code/models", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("GET /muse-code/models — serving cached catalog")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, modelsCatalogBody)
	})
	mux.HandleFunc("/muse-code/config", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("GET %s — serving cached feature-config", r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, featureConfigBody)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 30 * time.Second}
	log.Printf("mock-muse-overload listening on %s (status=%d succeed-after=%d ignore-substring=%q)",
		*addr, *status, *succeedAfter, *ignoreSubstring)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

// failureConfig is the flag set, bound once so the handler is a method
// rather than a closure over several pointers.
type failureConfig struct {
	status          int
	errType         string
	errMsg          string
	succeedAfter    int
	ignoreSubstring string

	requests atomic.Int64
}

// handleResponses is the whole mock: filter the reminder-observer side
// channel, count the real turn's attempts, decide, answer.
func (c *failureConfig) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)

	if c.ignoreSubstring != "" && strings.Contains(strings.ToLower(string(body)), c.ignoreSubstring) {
		log.Printf("POST /responses #0 — ignored (matches --ignore-substring %q, muse's own side channel), answering happily", c.ignoreSubstring)
		streamHappyPath(w)
		return
	}

	// Count first, then decide, so the log line and the decision can never
	// disagree about which attempt this was.
	n := c.requests.Add(1)
	if c.succeedAfter > 0 && n > int64(c.succeedAfter) {
		log.Printf("POST /responses #%d — succeeding (--succeed-after %d)", n, c.succeedAfter)
		streamHappyPath(w)
		return
	}
	log.Printf("POST /responses #%d — failing %d %s", n, c.status, c.errType)
	c.writeFailure(w)
}

// writeFailure emits a generic OpenAI/Responses-API-shaped error body. Which
// field muse actually reads to classify the failure is the HTTP status, not
// this body's `type`/`code` — live-confirmed: this same generic shape was
// accepted for both a 429 ("rate limited") and a 503 ("server error")
// classification, so the body content is realism/log value only, not load-
// bearing for muse's retry decision.
func (c *failureConfig) writeFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c.status)
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{"type": c.errType, "message": c.errMsg, "code": c.errType},
	})
	if err != nil { // unreachable: the map is all plain values
		log.Printf("marshal failure body: %v", err)
		return
	}
	_, _ = w.Write(append(payload, '\n'))
}

// streamHappyPath writes a minimal OpenAI-Responses-API SSE completion: the
// event names and nesting are the ones muse's own protocol schema documents
// (`muse schema generate-json-schema`) and that this mock's own live capture
// observed real muse sessions record as `producer.detail.stream
// .last_wire_event_type` — terminating in "response.completed", which is
// what IsAgentDone()-equivalent logic in muse's client watches for.
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

	var seq int
	write := func(eventType, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
		flusher.Flush()
		seq++
	}
	const rid = "resp_mock_muse_overload_001"
	const itemID = "msg_mock_muse_overload_001"
	const model = "muse-spark-1.2"
	const createdAt = 1789400000

	// responseObj renders the full top-level Response object OpenAI's
	// Responses API documents (id/object/created_at/status plus the full
	// set of request-echo fields: instructions/tools/tool_choice/
	// temperature/top_p/truncation/reasoning/text/metadata/...). Every
	// field here is populated (never omitted) because muse's own client is
	// a strict Rust deserializer: leaving out a field that shape treats as
	// required — not omitting it as JSON `null` — produced exactly this
	// mock's own worst live failure mode, "response could not be read",
	// which this mock's end-to-end verification hit and fixed by filling
	// every field rather than guessing which were optional.
	responseObj := func(status, output, usage string) string {
		return fmt.Sprintf(`{"id":%q,"object":"response","created_at":%d,"status":%q,"error":null,"incomplete_details":null,"instructions":null,"max_output_tokens":null,"model":%q,"output":%s,"parallel_tool_calls":true,"previous_response_id":null,"reasoning":{"effort":"high","summary":null},"store":true,"temperature":1,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1,"truncation":"disabled","usage":%s,"user":null,"metadata":{}}`,
			rid, createdAt, status, model, output, usage)
	}
	msgItem := func(status, content string) string {
		return fmt.Sprintf(`{"id":%q,"type":"message","status":%q,"role":"assistant","content":%s}`, itemID, status, content)
	}
	emptyContent := `[]`
	doneContent := `[{"type":"output_text","text":"ok","annotations":[]}]`
	usageObj := `{"input_tokens":12,"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":13}`

	write("response.created", fmt.Sprintf(`{"type":"response.created","sequence_number":%d,"response":%s}`, seq, responseObj("in_progress", emptyContent, "null")))
	write("response.in_progress", fmt.Sprintf(`{"type":"response.in_progress","sequence_number":%d,"response":%s}`, seq, responseObj("in_progress", emptyContent, "null")))
	write("response.output_item.added", fmt.Sprintf(`{"type":"response.output_item.added","sequence_number":%d,"output_index":0,"item":%s}`, seq, msgItem("in_progress", emptyContent)))
	write("response.content_part.added", fmt.Sprintf(`{"type":"response.content_part.added","sequence_number":%d,"item_id":%q,"output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`, seq, itemID))
	write("response.output_text.delta", fmt.Sprintf(`{"type":"response.output_text.delta","sequence_number":%d,"item_id":%q,"output_index":0,"content_index":0,"delta":"ok"}`, seq, itemID))
	write("response.output_text.done", fmt.Sprintf(`{"type":"response.output_text.done","sequence_number":%d,"item_id":%q,"output_index":0,"content_index":0,"text":"ok"}`, seq, itemID))
	write("response.content_part.done", fmt.Sprintf(`{"type":"response.content_part.done","sequence_number":%d,"item_id":%q,"output_index":0,"content_index":0,"part":{"type":"output_text","text":"ok","annotations":[]}}`, seq, itemID))
	write("response.output_item.done", fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":%d,"output_index":0,"item":%s}`, seq, msgItem("completed", doneContent)))
	write("response.completed", fmt.Sprintf(`{"type":"response.completed","sequence_number":%d,"response":%s}`, seq, responseObj("completed", fmt.Sprintf(`[%s]`, msgItem("completed", doneContent)), usageObj)))
}
