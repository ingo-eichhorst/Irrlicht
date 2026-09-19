// mock-openai-error is a stateful OpenAI-compatible chat-completions stub for
// deterministic provider-error recordings.
//
// The first --succeed-after counted requests fail. The next request and all
// later requests stream a normal completion. A value of zero fails forever.
// This supports retry recovery, terminal overload, and rejected-credential
// scenarios through one binary and different recipe arguments.
//
// DeepSeek Harness also sends an auxiliary session-title request to the same
// route. The title request is not part of the user turn. The default
// --ignore-substring matches the stable system prompt in
// @deepseek-ai/dsh-session-title-llm 0.1.5-rc.2. The plugin also sends a
// distinct framed user message. Both roles must match, so normal user text
// cannot consume the title exception.
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

const (
	contentTypeHeader = "Content-Type"
	modelID           = "mock-openai-error-model"
	maxRequestBytes   = 1 << 20
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18806", "bind address")
	status := flag.Int("status", http.StatusServiceUnavailable, "HTTP status to fail with")
	errType := flag.String("error-type", "server_error", "OpenAI error.type")
	errCode := flag.String("error-code", "overloaded", "OpenAI error.code")
	errMsg := flag.String("error-message", "Provider overloaded (mock).", "OpenAI error.message")
	succeedAfter := flag.Int("succeed-after", 0, "number of counted requests that fail before success; 0 = never succeed")
	ignoreSubstring := flag.String("ignore-substring", "create a concise title", "case-insensitive title system-message substring to answer successfully without counting; empty disables filtering")
	alwaysSucceed := flag.Bool("always-succeed", false, "stream a successful response for every counted request")
	successBytes := flag.Int("success-bytes", 2, "number of ASCII content bytes in a successful response")
	flag.Parse()

	if *succeedAfter < 0 {
		log.Fatalf("--succeed-after must be >= 0, got %d", *succeedAfter)
	}
	if *status < 400 || *status > 599 {
		log.Fatalf("--status must be a 4xx/5xx failure code, got %d", *status)
	}
	if *successBytes < 1 || *successBytes > maxRequestBytes {
		log.Fatalf("--success-bytes must be in 1..%d, got %d", maxRequestBytes, *successBytes)
	}

	cfg := failureConfig{
		status:          *status,
		errType:         *errType,
		errCode:         *errCode,
		errMsg:          *errMsg,
		succeedAfter:    *succeedAfter,
		ignoreSubstring: strings.ToLower(*ignoreSubstring),
		alwaysSucceed:   *alwaysSucceed,
		successBytes:    *successBytes,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", cfg.handleChatCompletions)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadTimeout: 30 * time.Second}
	log.Printf("mock-openai-error listening on %s (status=%d type=%s succeed-after=%d)",
		*addr, *status, *errType, *succeedAfter)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

type failureConfig struct {
	status          int
	errType         string
	errCode         string
	errMsg          string
	succeedAfter    int
	ignoreSubstring string
	alwaysSucceed   bool
	successBytes    int

	requests atomic.Int64
}

func (c *failureConfig) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		http.Error(w, "request body exceeds mock limit", http.StatusRequestEntityTooLarge)
		return
	}
	model := modelOf(body)

	if c.ignoreSubstring != "" && isTitleRequest(body, c.ignoreSubstring) {
		log.Printf("POST /v1/chat/completions #0 model=%s — ignored title side channel", model)
		streamHappyPath(w, "ok")
		return
	}

	n := c.requests.Add(1)
	if c.alwaysSucceed {
		log.Printf("POST /v1/chat/completions #%d model=%s — succeeding (--always-succeed, %d bytes)", n, model, c.successBytes)
		streamHappyPath(w, strings.Repeat("x", c.successBytes))
		return
	}
	if c.succeedAfter > 0 && n > int64(c.succeedAfter) {
		log.Printf("POST /v1/chat/completions #%d model=%s — succeeding (--succeed-after %d)", n, model, c.succeedAfter)
		streamHappyPath(w, "ok")
		return
	}
	log.Printf("POST /v1/chat/completions #%d model=%s — failing %d %s", n, model, c.status, c.errType)
	c.writeFailure(w)
}

// isTitleRequest matches the two-message shape read from
// @deepseek-ai/dsh-session-title-llm 0.1.5-rc.2 lib/index.js: systemPrompt and
// frameMessages. A parse failure is a counted request, not a title exemption.
func isTitleRequest(body []byte, systemNeedle string) bool {
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &request) != nil || len(request.Messages) != 2 {
		return false
	}
	system, systemOK := messageText(request.Messages[0].Content)
	user, userOK := messageText(request.Messages[1].Content)
	if !systemOK || !userOK {
		return false
	}
	return request.Messages[0].Role == "system" && request.Messages[1].Role == "user" &&
		strings.Contains(strings.ToLower(system), systemNeedle) &&
		strings.HasPrefix(user, "Generate the session title from this JSON array of human messages:")
}

func messageText(content json.RawMessage) (string, bool) {
	var plain string
	if json.Unmarshal(content, &plain) == nil {
		return plain, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil || len(parts) == 0 {
		return "", false
	}
	var text strings.Builder
	for _, part := range parts {
		if part.Type != "text" {
			return "", false
		}
		text.WriteString(part.Text)
	}
	return text.String(), true
}

func (c *failureConfig) writeFailure(w http.ResponseWriter) {
	w.Header().Set(contentTypeHeader, "application/json")
	w.WriteHeader(c.status)
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": c.errMsg,
			"type":    c.errType,
			"param":   nil,
			"code":    c.errCode,
		},
	})
	if err != nil {
		log.Printf("marshal failure body: %v", err)
		return
	}
	_, _ = w.Write(append(payload, '\n'))
}

func handleModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(contentTypeHeader, "application/json")
	_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model","created":0,"owned_by":"mock"}]}`, modelID)
}

func modelOf(body []byte) string {
	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &request); err != nil || request.Model == "" {
		return "?"
	}
	if !safeModelName.MatchString(request.Model) {
		return "<unprintable>"
	}
	return request.Model
}

var safeModelName = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,128}$`)

func streamHappyPath(w http.ResponseWriter, content string) {
	w.Header().Set(contentTypeHeader, "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("response writer is not a Flusher — cannot stream")
		return
	}

	write := func(data string) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	writeChunk := func(delta map[string]string, finishReason any, usage map[string]int) {
		chunk := map[string]any{
			"id":      "chatcmpl-mock-001",
			"object":  "chat.completion.chunk",
			"created": 0,
			"model":   modelID,
			"choices": []any{map[string]any{
				"index": 0, "delta": delta, "finish_reason": finishReason,
			}},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			log.Printf("marshal success chunk: %v", err)
			return
		}
		write(string(encoded))
	}
	writeChunk(map[string]string{"role": "assistant", "content": ""}, nil, nil)
	writeChunk(map[string]string{"content": content}, nil, nil)
	writeChunk(map[string]string{}, "stop", map[string]int{
		"prompt_tokens": 12, "completion_tokens": 1, "total_tokens": 13,
	})
	write("[DONE]")
}
