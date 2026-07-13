// status_fallback.go implements ExtractStatusFallback: the shared,
// adapter-agnostic fallback used when ExtractQuestionSnippet finds no
// literal question in the last assistant text. See issue #979 — the
// previous fallback ("first non-empty line") surfaced noise as often as
// signal; this scores each sentence on high-signal patterns instead.
package session

import (
	"regexp"
	"strings"
)

// statusVerbPattern flags sentences reporting a state change — the
// strongest non-question signal available in a plain status update.
var statusVerbPattern = regexp.MustCompile(`(?i)\b(?:fixed|merged|blocked|failed|failing|shipped|done|implemented|reviewed|passed|ready|waiting|resolved|reverted|deployed|landed)\b`)

// issueRefPattern flags a GitHub issue/PR reference (e.g. "#921").
var issueRefPattern = regexp.MustCompile(`#\d+`)

// filePathPattern flags a file-path-looking token (e.g. "core/domain/x.go",
// optionally backtick-wrapped).
var filePathPattern = regexp.MustCompile("`?\\b[\\w-]+(?:/[\\w.-]+)+\\.\\w+\\b`?")

// eitherOrPattern flags disjunction framing ("either X or Y", or plain "X, or
// Y") — a weak, tie-breaking signal (weight 1): the bare "or" branch matches
// on any ordinary conjunction too, which is deliberate (the real either/or
// example this fallback targets, e.g. "resolve the conflict now, or dig into
// the design decision first", never uses the word "either" at all).
var eitherOrPattern = regexp.MustCompile(`(?i)\beither\b|\bor\b`)

// ExtractStatusFallback picks the highest-signal sentence in text — the
// fallback used when no literal question was found. A rule-based scorer
// standing in for "first/last sentence": sentences carrying a state verb, an
// issue/PR reference, a file path, either/or framing, or a trailing question
// mark are far more informative than an arbitrary positional pick. Ties
// favor the later (more recent) sentence. Falls back to the first non-empty
// line when nothing scores, matching the behavior it replaces.
func ExtractStatusFallback(text string) string {
	if text == "" {
		return ""
	}
	best, bestScore := "", 0
	for _, s := range splitSentences(text) {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		if score := scoreStatusSentence(trimmed); score > 0 && score >= bestScore {
			best, bestScore = trimmed, score
		}
	}
	if best != "" {
		return best
	}
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func scoreStatusSentence(s string) int {
	score := 0
	if endsWithQuestionMark(s) {
		score += 3
	}
	if eitherOrPattern.MatchString(s) {
		score++
	}
	if issueRefPattern.MatchString(s) {
		score += 2
	}
	if filePathPattern.MatchString(s) {
		score += 2
	}
	if statusVerbPattern.MatchString(s) {
		score += 2
	}
	return score
}
