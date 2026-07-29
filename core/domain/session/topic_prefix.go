// topic_prefix.go derives the short "ChatGPT-conversation-title"-style topic
// that prefixes a surfaced pending-question headline, so a watcher scanning
// waiting sessions sees WHAT the question is about, not just the bare ask
// (issue #1186). The prefix source is the session's first user prompt; the
// composition ("<topic>: <question>") happens in the tailer→domain conversion.
package session

import (
	"strings"
	"unicode/utf8"
)

// maxTopicPrefixRunes bounds the derived topic so it can never dominate the
// headline budget: a pasted URL or a wall-of-text first prompt collapses to a
// single long "word", and the join must still leave room for the question.
// Generous for a 3–5 word topic.
const maxTopicPrefixRunes = 48

// topicWordMax is the upper end of the "3–5 word" topic window: the derived
// topic keeps at most this many leading words.
const topicWordMax = 5

// topicTrailingPunct are characters trimmed from the end of a derived topic so
// the ": " join reads cleanly (no "DB migration -: …").
const topicTrailingPunct = " \t.,;:!?-—–"

// topicTrailingStopwords are articles/prepositions/conjunctions trimmed from
// the END of a topic so it doesn't dangle on "... to the". Only trailing ones
// are dropped, and never the last remaining word.
var topicTrailingStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "of": true, "for": true,
	"and": true, "or": true, "in": true, "on": true, "with": true, "at": true,
	"by": true, "from": true, "into": true, "as": true,
}

// DeriveTopicPrefix turns a session's first user prompt into a short topic
// (3–5 words) for prefixing the pending-question headline (issue #1186). It
// strips a leading slash-command token (e.g. "/ir:exec 1042 …"), collapses
// whitespace, keeps the first few words on a word boundary, trims trailing
// stopwords and punctuation, and caps the result. Returns "" when the prompt
// yields nothing usable — the caller then emits the bare question rather than
// a dangling ": ".
func DeriveTopicPrefix(prompt string) string {
	fields := strings.Fields(prompt)
	// Drop a leading "/command" token (e.g. "/ir:exec"): it names the tool, not
	// the task, and would make a useless topic.
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > topicWordMax {
		fields = fields[:topicWordMax]
	}
	// Trim trailing filler words so the topic doesn't dangle on "to the".
	for len(fields) > 1 && topicTrailingStopwords[strings.ToLower(strings.Trim(fields[len(fields)-1], topicTrailingPunct))] {
		fields = fields[:len(fields)-1]
	}
	topic := strings.TrimRight(strings.Join(fields, " "), topicTrailingPunct)
	return capTopicRunes(topic, maxTopicPrefixRunes)
}

// QuestionHasTopicPrefix reports whether question already leads with a topic —
// either it starts with topic itself, or it carries its own short "Lead: "
// colon prefix — so the caller doesn't prepend a second one (issue #1186).
func QuestionHasTopicPrefix(question, topic string) bool {
	q := strings.TrimSpace(question)
	if topic != "" && strings.HasPrefix(strings.ToLower(q), strings.ToLower(topic)) {
		return true
	}
	// A short "Lead words: …" already present in the question counts as a
	// topic prefix. Bounded to the topic budget and to a few words — and
	// vetoed by sentence punctuation before the colon — so a mid-sentence
	// colon (or a "PKCE: implicit" style clause) doesn't false-match.
	if idx := strings.Index(q, ": "); idx > 0 {
		lead := q[:idx]
		if utf8.RuneCountInString(lead) <= maxTopicPrefixRunes &&
			len(strings.Fields(lead)) <= topicWordMax &&
			!strings.ContainsAny(lead, "?!.") {
			return true
		}
	}
	return false
}

// capTopicRunes truncates s to at most max runes on a rune boundary, with a
// trailing ellipsis when it drops text. Mirrors the compaction adapter's
// capRunes; kept here so the domain owns its own bound without importing an
// outbound adapter.
func capTopicRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:max-1]), " ") + "…"
}
