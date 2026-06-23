// tasksummary_scan.go parses the agent-emitted task-summary marker from
// assistant text (issue #738). The agent authors a one-line description of
// what the current task is about and emits it in-band as a hidden HTML
// comment, e.g.
//
//	<!-- {"marker":"irrlicht-summary","summary":"Add a logout button to the navbar"} -->
//
// irrlicht only parses it (read-only). It is the stable companion to the
// irrlicht-eta progress marker (taskestimate_scan.go): the ETA churns per
// phase, the summary describes the task as a whole and is wall-clock
// independent (so it survives replay). Parsing is tolerant by design — the
// model rewrites markers — so we accept key drift and ignore anything
// malformed rather than erroring. Latest non-empty marker wins.
package tailer

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// taskSummaryMarkerKeyRe accepts the marker-key spellings the model emits
// ("irrlicht-summary", "irrlicht_summary").
var taskSummaryMarkerKeyRe = regexp.MustCompile(`^irrlicht[-_]summary$`)

// controlCharRe matches ASCII control characters (including the newlines and
// tabs a multi-line marker may carry) so the surfaced summary is a single
// clean line.
var controlCharRe = regexp.MustCompile(`[\x00-\x1f\x7f]+`)

// maxTaskSummaryRunes caps the surfaced summary. The instruction asks for
// ~120 chars; we keep generous headroom but never let a runaway value bloat
// the session JSON or the UI row.
const maxTaskSummaryRunes = 280

// ScanTaskSummary scans the full text of an assistant text block for
// task-summary markers and returns the last valid one, stamped with the event
// timestamp. Returns nil when no valid marker is present. Like
// ScanTaskEstimate it must be fed the complete block text — ParsedEvent's
// truncated AssistantText would lose markers in long messages.
func ScanTaskSummary(text string, observedAt time.Time) *TaskSummary {
	// Fast-reject the common no-marker case before running the regex engine
	// over the (potentially large) full text block — shares the hot path with
	// ScanTaskEstimate.
	if !strings.Contains(text, "<!--") {
		return nil
	}
	var latest *TaskSummary
	for _, m := range taskEstimateCommentRe.FindAllStringSubmatch(text, -1) {
		payload := m[1]
		if len(payload) > maxTaskEstimateCommentLen {
			continue
		}
		if s := parseTaskSummaryPayload(payload); s != nil {
			s.ObservedAt = observedAt.Unix()
			latest = s
		}
	}
	return latest
}

// parseTaskSummaryPayload decodes one candidate JSON payload into a
// TaskSummary. Acceptance gate: the marker key matches irrlicht[-_]summary and
// a non-empty summary string is present (tolerating the "text"/"title" key
// drift). Returns nil for anything malformed or empty. Never errors.
func parseTaskSummaryPayload(payload string) *TaskSummary {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return nil
	}
	if marker, _ := obj["marker"].(string); !taskSummaryMarkerKeyRe.MatchString(marker) {
		return nil
	}
	text := cleanSummaryText(strField(obj, "summary", "text", "title"))
	if text == "" {
		return nil
	}
	return &TaskSummary{Text: text}
}

// cleanSummaryText collapses control characters/whitespace to single spaces,
// trims, and caps the length so the surfaced summary is a single tidy line.
func cleanSummaryText(s string) string {
	s = controlCharRe.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > maxTaskSummaryRunes {
		s = string([]rune(s)[:maxTaskSummaryRunes])
		s = strings.TrimSpace(s)
	}
	return s
}

// strField returns the first of keys present in obj as a non-empty string.
func strField(obj map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
