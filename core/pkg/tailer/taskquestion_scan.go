// taskquestion_scan.go parses the agent-emitted task-question marker from
// assistant text (issue #759). When the agent ends a turn by asking the user
// something, it may author a terse one-line version of that question and emit
// it in-band as a hidden HTML comment, e.g.
//
//	<!-- {"marker":"irrlicht-question","question":"Run the migration now?"} -->
//
// irrlicht only parses it (read-only). It is the question-state companion to
// the irrlicht-summary marker (tasksummary_scan.go): the summary describes the
// task, the question describes what the agent is currently blocked on. It is
// the preferred source for the surfaced waiting-state headline; when absent the
// daemon falls back to compacting the raw last-assistant text. Parsing is
// tolerant by design — the model rewrites markers — so we accept key drift and
// ignore anything malformed rather than erroring. Latest non-empty marker wins.
package tailer

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// taskQuestionMarkerKeyRe accepts the marker-key spellings the model emits
// ("irrlicht-question", "irrlicht_question").
var taskQuestionMarkerKeyRe = regexp.MustCompile(`^irrlicht[-_]question$`)

// ScanTaskQuestion scans the full text of an assistant text block for
// task-question markers and returns the last valid one, stamped with the event
// timestamp. Returns nil when no valid marker is present. Like ScanTaskSummary
// it must be fed the complete block text — ParsedEvent's truncated
// AssistantText would lose markers in long messages.
func ScanTaskQuestion(text string, observedAt time.Time) *TaskQuestion {
	// Fast-reject the common no-marker case before running the regex engine —
	// shares the hot path with ScanTaskEstimate / ScanTaskSummary.
	if !strings.Contains(text, "<!--") {
		return nil
	}
	var latest *TaskQuestion
	for _, m := range taskEstimateCommentRe.FindAllStringSubmatch(text, -1) {
		payload := m[1]
		if len(payload) > maxTaskEstimateCommentLen {
			continue
		}
		if q := parseTaskQuestionPayload(payload); q != nil {
			q.ObservedAt = observedAt.Unix()
			latest = q
		}
	}
	return latest
}

// parseTaskQuestionPayload decodes one candidate JSON payload into a
// TaskQuestion. Acceptance gate: the marker key matches irrlicht[-_]question
// and a non-empty question string is present (tolerating the "text" key drift).
// Returns nil for anything malformed or empty. Never errors.
func parseTaskQuestionPayload(payload string) *TaskQuestion {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return nil
	}
	if marker, _ := obj["marker"].(string); !taskQuestionMarkerKeyRe.MatchString(marker) {
		return nil
	}
	text := cleanSummaryText(strField(obj, "question", "text"))
	if text == "" {
		return nil
	}
	return &TaskQuestion{Text: text}
}
