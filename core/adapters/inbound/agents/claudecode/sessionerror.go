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

	// terminalAPIErrorClass is the class recorded for the isApiErrorMessage
	// shape.
	//
	// It is a constant rather than a field read because the only classification
	// claudecode offers on this shape is a top-level `"error"` whose recorded
	// value is the string "unknown" — the absence of a class rather than one.
	// (Both occurrences in replaydata carry it: reproduce with
	// `grep -rh '"isApiErrorMessage":true' replaydata/agents/claudecode/ | jq -c .error`.)
	// "provider" is the generic bucket from session.SessionError.Class's
	// vocabulary; the real information is in Message, which is carried verbatim.
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
// No status code is derived. The recorded text is "API Error: API returned an
// empty or malformed response (HTTP 200)" — the number in the prose is the
// transport's SUCCESS code, so reading a status out of the message would record
// 200 as a failure code. HTTPStatus stays nil, which is what the pointer is for.
func terminalAPIError(raw map[string]interface{}) *tailer.SessionError {
	// A non-bool or absent value yields false, which is the correct reading:
	// only an explicit true is claudecode saying this message IS the error.
	if flagged, _ := raw["isApiErrorMessage"].(bool); !flagged {
		return nil
	}
	return &tailer.SessionError{
		Phase:   tailer.ErrorPhaseTerminal,
		Class:   terminalAPIErrorClass,
		Message: strings.TrimSpace(tailer.ExtractAssistantFullText(raw)),
	}
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
