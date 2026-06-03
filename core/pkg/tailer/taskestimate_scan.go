// taskestimate_scan.go parses the agent-emitted task-progress marker from
// assistant text (issue #558). The agent authors its own estimate and emits
// it in-band as a hidden HTML comment, e.g.
//
//	<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":2} -->
//
// irrlicht only parses it (read-only). Parsing is tolerant by design — the
// 2026-05-31 experiment showed 0% exact-format compliance (the model rewrites
// the marker), so we accept key drift and ignore anything malformed rather
// than erroring. Latest valid marker wins.
//
// The scan lives in the tailer package (like ExtractAssistantText) because
// the marker contract is plain text and adapter-agnostic; each adapter walks
// its own transcript shape and feeds the full text blocks here.
package tailer

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// taskEstimateCommentRe matches candidate HTML comments carrying a JSON
// object. Non-greedy so adjacent comments don't merge into one match; (?s) so
// a comment spanning lines still matches.
var taskEstimateCommentRe = regexp.MustCompile(`(?s)<!--\s*(\{.*?\})\s*-->`)

// taskEstimateMarkerKeyRe accepts the marker-key spellings the model emits
// ("irrlicht-eta", "irrlicht_estimate", …).
var taskEstimateMarkerKeyRe = regexp.MustCompile(`^irrlicht[-_]e(stimate|ta)$`)

// maxTaskEstimateCommentLen caps the JSON payload we'll consider — anything
// larger is not a progress marker.
const maxTaskEstimateCommentLen = 2048

// ScanTaskEstimate scans the full text of an assistant text block for
// task-estimate markers and returns the last valid one, stamped with the
// event timestamp. Returns nil when no valid marker is present. It must be
// fed the complete block text — ParsedEvent.AssistantText is tail-truncated
// to 200 runes and would lose markers in long messages.
func ScanTaskEstimate(text string, observedAt time.Time) *TaskEstimate {
	// Fast-reject the overwhelmingly common no-marker case before running the
	// regex engine over the (potentially large) full text block — this runs
	// per assistant text block on the tail hot path.
	if !strings.Contains(text, "<!--") {
		return nil
	}
	var latest *TaskEstimate
	for _, m := range taskEstimateCommentRe.FindAllStringSubmatch(text, -1) {
		payload := m[1]
		if len(payload) > maxTaskEstimateCommentLen {
			continue
		}
		if est := parseTaskEstimatePayload(payload); est != nil {
			est.ObservedAt = observedAt.Unix()
			latest = est
		}
	}
	return latest
}

// parseTaskEstimatePayload decodes one candidate JSON payload into a
// TaskEstimate. Acceptance gate (per #558): the JSON has a marker key
// matching irrlicht[-_]e(stimate|ta) or carries total_rounds. Values are
// validated — absurd markers are rejected wholesale rather than clamped, so a
// later well-formed emission isn't shadowed by junk. Never errors.
func parseTaskEstimatePayload(payload string) *TaskEstimate {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return nil
	}

	totalF, hasTotal := numField(obj, "total_rounds")
	marker, _ := obj["marker"].(string)
	if !taskEstimateMarkerKeyRe.MatchString(marker) && !hasTotal {
		return nil
	}
	if !hasTotal {
		return nil // marker key without total_rounds carries no estimate
	}
	total := int(totalF)
	if total < 1 || total > 100 {
		return nil
	}

	// completed_rounds tolerates the key drift seen in the experiment.
	// Missing → 0 (estimate recorded, chip suppressed until progress).
	completed := 0
	if completedF, ok := numField(obj, "completed_rounds", "done_rounds", "round"); ok {
		completed = int(completedF)
	}
	if completed < 0 || completed > total {
		return nil
	}

	est := &TaskEstimate{TotalRounds: total, CompletedRounds: completed}
	if risk, ok := obj["risk"].(string); ok {
		est.Risk = risk
	}
	// confidence is an optional passthrough — an out-of-range value is
	// ignored (dropped), NOT a reason to reject the whole marker and lose
	// the valid total/completed progress it carries.
	if c, ok := numField(obj, "confidence"); ok && c >= 0 && c <= 1 {
		est.Confidence = &c
	}
	return est
}

// numField returns the first of keys present in obj as a JSON number.
func numField(obj map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v, ok := obj[k].(float64); ok {
			return v, true
		}
	}
	return 0, false
}
