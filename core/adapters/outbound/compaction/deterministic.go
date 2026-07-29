package compaction

import (
	"regexp"
	"strings"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// maxHeadlineRunes is a generous safety bound, not a presentation cut: it
// only stops a raw paragraph from bloating the payload. Each UI truncates to
// its own real estate (macOS's multi-line pill vs. web's 3-line clamp) — a
// tighter daemon-side cut here would just throw away content before either
// UI gets to decide. See issue #979: the previous 70-rune value chopped an
// already-correctly-extracted question sentence in half.
const maxHeadlineRunes = 200

// Noise-stripping regexes, applied before headline selection. Each is
// non-greedy and (?s) so a block spanning lines still matches.
var (
	fencedCodeRe     = regexp.MustCompile("(?s)```.*?```")
	htmlCommentRe    = regexp.MustCompile(`(?s)<!--.*?-->`)
	systemReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	markdownLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	inlineMarkupRe   = regexp.MustCompile("[`*]+")
)

// DeterministicCompactor is the default pure-heuristic strategy: it strips
// fenced code, HTML-comment markers and system-reminder tags, picks the most
// headline-worthy fragment per kind (the question sentence, the first sentence
// of an intent, or — for the verbatim kind — the whole already-composed line),
// strips inline markdown, collapses whitespace and caps the result to
// maxHeadlineRunes with an ellipsis on a rune boundary. Empty in → empty out.
// It never errors. See issues #759 and #1186.
type DeterministicCompactor struct{}

var _ outbound.TextCompactor = DeterministicCompactor{}

// Compact reduces text to a one-line headline of the given kind.
func (DeterministicCompactor) Compact(text string, kind outbound.CompactKind) string {
	cleaned := stripNoise(text)
	if cleaned == "" {
		return ""
	}

	var headline string
	switch kind {
	case outbound.CompactQuestion:
		// The same extractor the waiting-state classifier uses, so the headline
		// matches the question that put the session into `waiting`. Fall back to
		// the shared heuristic scorer (issue #979) when there is no literal
		// question — it picks the highest-signal sentence instead of an
		// arbitrary positional one.
		if snippet := session.ExtractQuestionSnippet(cleaned); snippet != "" {
			headline = snippet
		} else {
			headline = session.ExtractStatusFallback(cleaned)
		}
	case outbound.CompactQuestionVerbatim:
		// Already a one-line "<topic>: <question>" — keep the whole thing so a
		// multi-sentence marker doesn't lose its topic prefix to sentence
		// selection (issue #1186). The shared strip/collapse/cap below still
		// runs, so the 200-rune budget is enforced (trimming the question tail,
		// never the leading topic).
		headline = cleaned
	default: // CompactIntent
		headline = firstSentence(cleaned)
	}

	headline = stripInlineMarkdown(headline)
	headline = strings.Join(strings.Fields(headline), " ")
	return session.CapRunes(headline, maxHeadlineRunes)
}

// stripNoise removes fenced code, HTML comments and system-reminder blocks —
// the structural noise that would otherwise dominate a headline.
func stripNoise(s string) string {
	s = fencedCodeRe.ReplaceAllString(s, " ")
	s = htmlCommentRe.ReplaceAllString(s, " ")
	s = systemReminderRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// stripInlineMarkdown unwraps markdown links to their text and removes inline
// emphasis/code markers. Underscores are left alone so snake_case identifiers
// in a headline aren't mangled.
func stripInlineMarkdown(s string) string {
	s = markdownLinkRe.ReplaceAllString(s, "$1")
	s = inlineMarkupRe.ReplaceAllString(s, "")
	s = strings.TrimLeft(s, "#> ")
	return strings.TrimSpace(s)
}

// firstSentence returns the leading sentence of s — text up to the first
// sentence-terminator (. ! ?) followed by whitespace or end-of-string, or up to
// the first newline, whichever comes first. Heuristic: an abbreviation's period
// followed by a space can split early; for a one-line headline that is an
// acceptable trade and the cap still bounds the result.
func firstSentence(s string) string {
	runes := []rune(s)
	for i, r := range runes {
		if r == '\n' {
			return strings.TrimSpace(string(runes[:i]))
		}
		if r == '.' || r == '!' || r == '?' {
			next := i + 1
			if next >= len(runes) || runes[next] == ' ' || runes[next] == '\n' || runes[next] == '\t' {
				return strings.TrimSpace(string(runes[:next]))
			}
		}
	}
	return strings.TrimSpace(s)
}
