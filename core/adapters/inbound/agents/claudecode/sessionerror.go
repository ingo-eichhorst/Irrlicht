package claudecode

import (
	"strings"

	"irrlicht/core/pkg/tailer"
)

// Claude Code reports a session-level failure in two unrelated shapes, and this
// file is the whole of #1799's claudecode producer.
//
//   - `system` / `subtype:"api_error"` — the retry ladder. Rich: an HTTP status,
//     the provider's own error type and message, and the three retry counters.
//     It is Skip=true (handleSystemEvent's catch-all), so it reaches the tailer
//     through applySkippedEvent rather than processParsedEvent.
//   - an ordinary `assistant` message flagged `isApiErrorMessage` — the give-up.
//     It carries a terminal stop_reason and no counters at all, which is exactly
//     why IsAgentDone reads true for it and why the classifier's session_error
//     rule has to outrank agent_done.
//
// Neither shape sets ParsedEvent.IsError. That field is a *tool result* failure
// — a grep that matched nothing, a build that broke — and turning a session red
// for one would be wrong. See ParsedEvent.SessionError's own doc.
const (
	// systemSubtypeAPIError is the `system` subtype carrying a provider error.
	systemSubtypeAPIError = "api_error"

	// terminalAPIErrorClass is the fallback class recorded for the
	// isApiErrorMessage shape when the transcript's own top-level `"error"`
	// token is either absent or the literal string "unknown" — claude-code's
	// own spelling of "no classification offered," true of every recording
	// before 2.1.245. From 2.1.245 on, that same field carries a real machine
	// token (`server_error`, `authentication_failed`, ...) that
	// terminalAPIErrorMessageClass prefers over this constant. Reproduce the
	// split with `grep -rh '"isApiErrorMessage":true' replaydata/agents/claudecode/
	// | jq -c .error`. "provider" is the generic bucket from
	// session.SessionError.Class's vocabulary; the real information is in
	// Message, which is carried verbatim.
	terminalAPIErrorClass = "provider"
)

// apiErrorFromSystemEvent maps a `system`/`api_error` line onto the tailer's
// session-error shape. Callers must have already established the subtype.
//
// PHASE IS READ FROM THE RETRY FIELDS, NEVER FROM `Attempt == MaxAttempts`.
// Across all 16 api_errors in replaydata the counter runs 1..6 of 10 and never
// reaches the ceiling — the ladder is abandoned when the user intervenes, not
// exhausted — so the equality that looks like the obvious derivation would
// report every real give-up as "still retrying". The honest signal is that the
// agent told us when the next attempt is due; see session.ErrorPhase.
// Reproduce the census with:
//
//	grep -rh '"subtype":"api_error"' replaydata/agents/claudecode/ \
//	  | jq -c '.retryAttempt' | sort -n | uniq -c
func apiErrorFromSystemEvent(raw map[string]interface{}) *tailer.SessionError {
	errObj, _ := raw["error"].(map[string]interface{})

	se := &tailer.SessionError{
		Class:       apiErrorClass(errObj),
		Message:     apiErrorMessage(errObj),
		HTTPStatus:  tailer.OptInt(errObj, "status"),
		Attempt:     tailer.OptInt(raw, "retryAttempt"),
		MaxAttempts: tailer.OptInt(raw, "maxRetries"),
		RetryIn:     tailer.OptDurationFromMillis(raw, "retryInMs"),
	}
	if se.Attempt != nil || se.RetryIn != nil {
		se.Phase = tailer.ErrorPhaseRetrying
	}
	return se
}

// terminalAPIError maps an assistant message flagged `isApiErrorMessage` onto a
// terminal session error, and returns nil for every other event.
//
// THE GATE IS ON THE VALUE, NEVER ON THE FIELD'S PRESENCE, and that is the
// single most important line in this file. Claude Code writes
// `isApiErrorMessage` on assistant messages whether or not anything failed: the
// ESC-interrupt recording carries it with value false, as do three of the
// committed regressions. Reproduce with:
//
//	grep -rho '"isApiErrorMessage":[a-z]*' replaydata/agents/claudecode/ \
//	  | sort | uniq -c        # -> 4 false, 2 true
//
// A presence check would therefore paint every user interrupt red — the most
// common interaction in the product. TestParser_ESCInterrupt_IsApiErrorMessage
// FalseIsNotAnError is the fixture that catches it.
//
// HTTPStatus is read from the top-level `apiErrorStatus` field claude-code
// 2.1.245 started writing alongside isApiErrorMessage — the real HTTP status
// (529, 401, ...). It is never derived from the message text: the recorded
// text can itself read "API Error: API returned an empty or malformed
// response (HTTP 200)", and the number in THAT prose is the transport's
// SUCCESS code, not a failure code — apiErrorStatus is a structured sibling
// field, not a re-parse of the string. Recordings older than 2.1.245 carry no
// apiErrorStatus key at all, and tailer.OptInt returns nil rather than a
// fabricated 0 for a missing key, so HTTPStatus stays nil for them — which is
// what the pointer is for. See TestParser_TerminalAPIErrorMessage_FromRecording
// for the pre-field fixture and TestParser_TerminalAPIError_StructuredFields
// for the two post-field ones (#1818).
func terminalAPIError(raw map[string]interface{}) *tailer.SessionError {
	// A non-bool or absent value yields false, which is the correct reading:
	// only an explicit true is claudecode saying this message IS the error.
	if flagged, _ := raw["isApiErrorMessage"].(bool); !flagged {
		return nil
	}
	return &tailer.SessionError{
		Phase:      tailer.ErrorPhaseTerminal,
		Class:      terminalAPIErrorMessageClass(raw),
		Message:    strings.TrimSpace(tailer.ExtractAssistantFullText(raw)),
		HTTPStatus: tailer.OptInt(raw, "apiErrorStatus"),
	}
}

// terminalAPIErrorMessageClass prefers claudecode's own machine-readable
// top-level `"error"` token (server_error, authentication_failed, ...) over
// the generic terminalAPIErrorClass fallback. "unknown" is claudecode's own
// spelling of "no classification offered" — every occurrence of it in
// replaydata predates apiErrorStatus/error carrying real values — so it is
// treated the same as absence rather than surfaced verbatim as a Class.
func terminalAPIErrorMessageClass(raw map[string]interface{}) string {
	if e, _ := raw["error"].(string); e != "" && e != "unknown" {
		return e
	}
	return terminalAPIErrorClass
}

// apiErrorClass prefers the provider's own nested error type over the summary
// claudecode flattens onto the error object. Both read "rate_limit_error" in
// every recorded api_error, so the preference costs nothing today and keeps the
// provider's word authoritative if they ever diverge.
func apiErrorClass(errObj map[string]interface{}) string {
	if inner := nestedProviderError(errObj); inner != nil {
		if t, _ := inner["type"].(string); t != "" {
			return t
		}
	}
	t, _ := errObj["type"].(string)
	return t
}

// apiErrorMessage reads the provider's human-readable text, which lives only on
// the nested error object.
func apiErrorMessage(errObj map[string]interface{}) string {
	if inner := nestedProviderError(errObj); inner != nil {
		if m, _ := inner["message"].(string); m != "" {
			return strings.TrimSpace(m)
		}
	}
	m, _ := errObj["message"].(string)
	return strings.TrimSpace(m)
}

// nestedProviderError walks `error.error.error` — the provider's own object
// inside Anthropic's `{"type":"error","error":{…}}` envelope, inside the
// transport error claudecode records. Returns nil at the first level that is
// not an object, so a reshaped payload degrades to the outer summary rather
// than panicking.
func nestedProviderError(errObj map[string]interface{}) map[string]interface{} {
	envelope, ok := errObj["error"].(map[string]interface{})
	if !ok {
		return nil
	}
	inner, ok := envelope["error"].(map[string]interface{})
	if !ok {
		return nil
	}
	return inner
}
