// unknown_hook_event.go holds the issue #1364 contract every hook-RECEIVING
// agent adapter must satisfy: an event name the receiver does not recognize is
// counted per (adapter, name), reported once per distinct name, and still
// answered 2xx.
//
// It is the third receiving-side contract in this package, and it exists for
// the reason the other two do — the obligation is a runtime choice, invisible
// to a static check. What makes this one worth pinning is that the failure it
// guards against is silent on BOTH sides. Before #1364 the default branch of
// both receivers logged at Info and returned 200, so an upstream event rename
// produced: no error, no counter, a permission still reading `granted`, a config
// still holding our entries, and state detection quietly degrading. Nothing in
// the daemon, and nothing a user would look at, said anything at all.
//
// The obligation that is easiest to get subtly wrong is "once per distinct
// name". A receiver that logs every unrecognized event satisfies "it is
// reported" and fails this contract on purpose: a rename fires on every tool
// call, and a log line per tool call is how the evidence gets buried rather
// than surfaced. The counter carries volume, the log carries the name.
package contracttesting

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/ports/outbound"
)

// UnknownEventReceiverUnderTest is one freshly built hook receiver plus the one
// thing the contract must see from outside it: whether the payload reached
// anything downstream.
type UnknownEventReceiverUnderTest struct {
	// Handler is the adapter's hook HTTP handler.
	Handler http.Handler

	// Observed reports whether the receiver dispatched anything downstream. An
	// unrecognized event must dispatch NOTHING — a receiver that guesses a
	// handler for an unfamiliar name is worse than one that ignores it — while a
	// recognized event must still dispatch, which is what keeps the negative
	// assertions from passing vacuously.
	Observed func() bool
}

// UnknownEventReceiver wires one adapter's hook receiver into
// AssertUnknownHookEventObserved. Every field is required.
type UnknownEventReceiver struct {
	// Adapter is the adapter name the counter is keyed by — the same string the
	// receiver passes to hookjson.IgnoreUnknownEvent. Asserted, not assumed: a
	// receiver that counts under the wrong name makes every count in the
	// diagnostics bundle attribute to the wrong CLI.
	Adapter string

	// Root relocates the adapter's declared transcript root to a
	// test-controlled directory — by t.Setenv of whatever env var moves it
	// ($HOME, $CODEX_HOME) — creates it, and returns its absolute path. Called
	// FIRST in every sub-test, before New.
	Root func(t *testing.T) string

	// WriteTranscript writes a transcript the adapter will resolve a session id
	// from into dir, and returns its absolute path. The path must survive
	// confinement, so every refusal the contract sees is about the EVENT NAME
	// and never about the path.
	WriteTranscript func(t *testing.T, dir string) string

	// New builds a fresh receiver logging into log. Fresh per sub-test so
	// Observed cannot carry over between obligations.
	New func(t *testing.T, log outbound.Logger) UnknownEventReceiverUnderTest

	// PayloadFor renders the adapter's hook JSON body carrying transcriptPath
	// and event.
	PayloadFor func(transcriptPath, event string) string

	// KnownEvent is an event name this adapter DOES recognize and dispatch on —
	// the vacuity guard's input.
	KnownEvent string

	// EndpointPath is the adapter's hook route, used to build the request.
	EndpointPath string
}

// AssertUnknownHookEventObserved runs the issue #1364 contract against r. Four
// obligations, each independently failable:
//
//  1. a RECOGNIZED event still dispatches, and is counted as unknown by nobody
//     — the vacuity guard. Without it a receiver that treats every event as
//     unrecognized passes 2–4 for entirely the wrong reason, and a receiver
//     that counts everything looks identical to one that counts correctly;
//  2. an unrecognized event is answered 2xx and dispatches nothing. The 2xx is
//     asserted rather than tolerated, for the reason RejectPath's doc comment
//     records: Claude Code documents that a non-2xx from a `type: http` hook
//     "can't block actions" but not whether it surfaces an error to the user,
//     and gemini-cli's pre-tool hooks and Copilot's preToolUse are known to
//     fail CLOSED on an error result. A receiver added for one of those must
//     not answer 4xx here (#1361, #1364);
//  3. it is counted per (adapter, event name) — repeats accumulate on their own
//     key and a second name gets its own. A single scalar cannot distinguish an
//     upstream rename, which fires on every tool call forever, from one stray
//     event, and distinguishing them is the whole diagnostic value;
//  4. it is reported exactly ONCE per distinct name, however many times that
//     name arrives. A rename would otherwise write a log line per tool call.
//
// The counters it reads are process-global, so every obligation measures a
// DELTA around its own uniquely-named events rather than an absolute — two
// adapters, or two invocations, can share a test binary without interfering.
func AssertUnknownHookEventObserved(t *testing.T, r UnknownEventReceiver) {
	t.Helper()
	t.Run("known_event_still_dispatched", func(t *testing.T) { assertKnownEventUncounted(t, r) })
	t.Run("unknown_event_answered_2xx_not_dispatched", func(t *testing.T) { assertUnknownIgnored(t, r) })
	t.Run("counted_per_adapter_and_name", func(t *testing.T) { assertUnknownCounted(t, r) })
	t.Run("reported_once_per_distinct_name", func(t *testing.T) { assertUnknownReportedOnce(t, r) })
}

// assertKnownEventUncounted is obligation 1.
func assertKnownEventUncounted(t *testing.T, r UnknownEventReceiver) {
	t.Helper()
	transcript := unknownEventTranscript(t, r)
	log := &RecordingLogger{}
	rut := r.New(t, log)

	before := unknownTotalFor(r.Adapter)
	rec := postUnknownEvent(t, r, rut, transcript, r.KnownEvent)

	if rec.Code < 200 || rec.Code > 299 {
		t.Fatalf("recognized event %q: status = %d, want 2xx", r.KnownEvent, rec.Code)
	}
	if !rut.Observed() {
		t.Fatalf("recognized event %q was not dispatched — every obligation below it would pass vacuously", r.KnownEvent)
	}
	if got := unknownTotalFor(r.Adapter) - before; got != 0 {
		t.Errorf("recognized event %q added %d to the unrecognized-event counters, want 0 — a receiver that counts everything reports a rename that never happened", r.KnownEvent, got)
	}
	if line, ok := log.FirstMentioning(r.KnownEvent, "unrecognized"); ok {
		t.Errorf("recognized event %q was reported as unrecognized: %s", r.KnownEvent, line)
	}
}

// assertUnknownIgnored is obligation 2.
func assertUnknownIgnored(t *testing.T, r UnknownEventReceiver) {
	t.Helper()
	transcript := unknownEventTranscript(t, r)
	rut := r.New(t, &RecordingLogger{})

	event := unknownEventName(t)
	rec := postUnknownEvent(t, r, rut, transcript, event)

	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("unrecognized event %q: status = %d, want 2xx — an unrecognized event is reported by the log and the counter, never by a status code on the user's critical path", event, rec.Code)
	}
	if rut.Observed() {
		t.Errorf("unrecognized event %q was dispatched downstream — an unfamiliar name must not be guessed into a handler", event)
	}
}

// assertUnknownCounted is obligation 3: the count is keyed by adapter AND name.
func assertUnknownCounted(t *testing.T, r UnknownEventReceiver) {
	t.Helper()
	transcript := unknownEventTranscript(t, r)
	rut := r.New(t, &RecordingLogger{})

	renamed, stray := unknownEventName(t)+"Renamed", unknownEventName(t)+"Stray"
	beforeRenamed, beforeStray := unknownCountFor(r.Adapter, renamed), unknownCountFor(r.Adapter, stray)

	for i := 0; i < 3; i++ {
		postUnknownEvent(t, r, rut, transcript, renamed)
	}
	postUnknownEvent(t, r, rut, transcript, stray)

	if got := unknownCountFor(r.Adapter, renamed) - beforeRenamed; got != 3 {
		t.Errorf("event %q arrived 3 times, counted %d for adapter %q — an event that keeps arriving is the signature of an upstream rename and has to be countable as such",
			renamed, got, r.Adapter)
	}
	if got := unknownCountFor(r.Adapter, stray) - beforeStray; got != 1 {
		t.Errorf("event %q arrived once, counted %d for adapter %q — a second name must get its own key, or a rename is indistinguishable from a stray event",
			stray, got, r.Adapter)
	}
}

// assertUnknownReportedOnce is obligation 4.
func assertUnknownReportedOnce(t *testing.T, r UnknownEventReceiver) {
	t.Helper()
	transcript := unknownEventTranscript(t, r)
	log := &RecordingLogger{}
	rut := r.New(t, log)

	flooding, second := unknownEventName(t)+"Flood", unknownEventName(t)+"Second"
	for i := 0; i < 5; i++ {
		postUnknownEvent(t, r, rut, transcript, flooding)
	}
	postUnknownEvent(t, r, rut, transcript, second)

	if n := log.CountMentioning(flooding); n != 1 {
		t.Errorf("event %q arrived 5 times and produced %d log line(s), want exactly 1 — a renamed event fires on every tool call, and one line per call buries the evidence it is meant to surface\nlines:\n%s",
			flooding, n, strings.Join(log.Lines(), "\n"))
	}
	if n := log.CountMentioning(second); n != 1 {
		t.Errorf("event %q arrived once and produced %d log line(s), want exactly 1 — every distinct name is reported, not only the first one ever seen\nlines:\n%s",
			second, n, strings.Join(log.Lines(), "\n"))
	}
}

// --- helpers ---

// unknownEventTranscript relocates the adapter's root and writes a transcript
// inside it, so confinement accepts the path and the only thing under test is
// the event name.
func unknownEventTranscript(t *testing.T, r UnknownEventReceiver) string {
	t.Helper()
	return r.WriteTranscript(t, mkSubdir(t, r.Root(t), "in-tree"))
}

// unknownEventName derives a name unique to the running sub-test. The counters
// are process-global and never reset, so a fixed literal would make two
// invocations in one test binary — two adapters, or a receiver wired twice —
// silently share a key and a first-sighting.
//
// It hashes t.Name() rather than embedding it. Retained names are bounded
// (hookjson.MaxUnknownEventNames' sibling cap), and a Go sub-test path is long
// enough to cross that bound — which would make the contract assert against a
// truncated key and fail for a reason that has nothing to do with the adapter.
// Real event names are short identifiers, so a short name is also the honest
// input.
func unknownEventName(t *testing.T) string {
	t.Helper()
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name()))
	return fmt.Sprintf("IrrUnk%08x", h.Sum32())
}

// unknownCountFor reads one (adapter, event) counter out of the snapshot.
func unknownCountFor(adapter, event string) uint64 {
	for _, row := range hookjson.UnknownEvents() {
		if row.Adapter == adapter && row.Event == event {
			return row.Count
		}
	}
	return 0
}

// unknownTotalFor sums every counter belonging to one adapter, so the vacuity
// guard can assert "nothing at all was counted" without knowing which name a
// miscounting receiver would have used.
func unknownTotalFor(adapter string) uint64 {
	var total uint64
	for _, row := range hookjson.UnknownEvents() {
		if row.Adapter == adapter {
			total += row.Count
		}
	}
	return total
}

// postUnknownEvent POSTs the adapter's hook body for event and returns the
// response.
func postUnknownEvent(t *testing.T, r UnknownEventReceiver, rut UnknownEventReceiverUnderTest, transcriptPath, event string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, r.EndpointPath, strings.NewReader(r.PayloadFor(transcriptPath, event)))
	rec := httptest.NewRecorder()
	rut.Handler.ServeHTTP(rec, req)
	return rec
}

// RecordingLogger is an outbound.Logger that keeps every line, so a contract can
// assert on what was reported rather than only on what was returned. Safe for
// concurrent use: hook receivers are HTTP handlers.
type RecordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *RecordingLogger) LogInfo(eventType, sessionID, message string) {
	l.append("info", eventType, sessionID, message)
}

func (l *RecordingLogger) LogError(eventType, sessionID, errorMsg string) {
	l.append("error", eventType, sessionID, errorMsg)
}

func (l *RecordingLogger) LogProcessingTime(string, string, int64, int, string) {}

func (l *RecordingLogger) Close() error { return nil }

func (l *RecordingLogger) append(level, eventType, sessionID, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf("[%s] %s %s: %s", level, eventType, sessionID, message))
}

// Lines returns a copy of everything logged so far.
func (l *RecordingLogger) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// CountMentioning counts the lines containing substr.
func (l *RecordingLogger) CountMentioning(substr string) int {
	var n int
	for _, line := range l.Lines() {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// FirstMentioning returns the first line containing every one of substrs.
func (l *RecordingLogger) FirstMentioning(substrs ...string) (string, bool) {
	for _, line := range l.Lines() {
		all := true
		for _, s := range substrs {
			if !strings.Contains(line, s) {
				all = false
				break
			}
		}
		if all {
			return line, true
		}
	}
	return "", false
}
