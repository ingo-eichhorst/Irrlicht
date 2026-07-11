// waiting_cue.go implements the heuristics behind IsWaitingForUserInput: is
// an agent that finished its turn actually blocked on the user? Two
// detectors are OR'd — a literal trailing question (ExtractQuestionSnippet)
// and an imperative or implicit cue like "let me know what you think"
// (ExtractWaitingCue) — for sessions where no user-blocking tool is open.
// See issue #381 for the cue coverage matrix this was built against.
package session

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// trailingMarkdownNoise are characters that commonly appear AFTER a
// question mark when models wrap questions in markdown or punctuation.
// e.g. `**Question?**` (bold), `*Question?*` (italic), `_Question?_`,
// `~~Question?~~`, “ `Question?` “ (inline code), `"Question?"`
// (quoted), `(yes/no?)` (parenthetical), `[link?]` (bracketed), and
// trailing whitespace.
const trailingMarkdownNoise = "*_~`\"')] \t\n\r"

// markdownWrapper is the subset of trailingMarkdownNoise excluding whitespace —
// characters that wrap a sentence terminator like `?**` or `?]` without
// breaking the sentence.
const markdownWrapper = "*_~`\"')]"

// IsWaitingForUserInput returns true when the agent finished its turn and the
// last assistant message either ends in a literal question or carries an
// imperative cue (e.g. "let me know if it's right before I commit",
// "verify locally and reply with …") — both indicate the agent is gated on
// the user even though no user-blocking tool is open.
//
// Two detectors are OR'd: ExtractQuestionSnippet (literal `?`, fired
// anywhere in the text) and ExtractWaitingCue (cue regexes against the
// trailing 1–2 sentences). See issue #381 for the cue coverage matrix.
func (m *SessionMetrics) IsWaitingForUserInput() bool {
	if m == nil {
		return false
	}
	if ExtractQuestionSnippet(m.LastAssistantText) != "" {
		return true
	}
	return ExtractWaitingCue(m.LastAssistantText) != ""
}

// ExtractQuestionSnippet returns the first non-rhetorical question sentence
// found in text, or an empty string when none is present. It preserves any
// trailing markdown wrappers (e.g. `**Question?**`) so the rendered snippet
// still reads naturally. URL fragments and other non-sentence `?` occurrences
// are skipped because the question mark must be followed by whitespace,
// end-of-string, or markdown wrappers leading to either.
//
// First-question-wins is preferred over last-question because agents typically
// lead with the actual question and follow with examples or status notes; a
// bullet list of options ending in `?` would otherwise hijack the snippet.
//
// Rhetorical questions — Q&A pairs like "Why do programmers prefer dark mode?
// Because light attracts bugs." — are skipped: the agent isn't actually
// waiting on the user. Detection is heuristic (the next sentence starts with
// an answer marker like "Because"); false negatives are preferred over
// false positives in mid-paragraph waiting detection.
func ExtractQuestionSnippet(text string) string {
	if text == "" {
		return ""
	}
	sentences := splitSentences(text)
	for i, s := range sentences {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		stripped := strings.TrimRight(trimmed, trailingMarkdownNoise)
		if stripped == "" {
			continue
		}
		if !endsWithQuestionMark(stripped) {
			continue
		}
		if isRhetorical(sentences, i) {
			continue
		}
		return trimmed
	}
	return ""
}

// waitingCuePatterns matches imperative or implicit waiting cues — non-
// question requests that should still register as a waiting turn. Bucketed
// from the coverage matrix in issue #381: A direct ask, B approval framing,
// C action gates, D curated imperatives (multi-word to minimise FPs),
// E trailing soft asks.
//
// The set favours recall: first-person intent statements that share an
// imperative shape ("Let me verify it's right.", "I'll check the logs.",
// "before I merged") may also match. That tradeoff is intentional per
// #381 — under-detecting a real waiting state defeats the dashboard's
// purpose, while a transient false-positive resolves on the next event.
var waitingCuePatterns = []*regexp.Regexp{
	// A. Direct ask (second-person verb)
	regexp.MustCompile(`(?i)\b(?:let me know|lmk|tell me|ping me|email me|reach out|holler|shout|hit me up)\b`),
	regexp.MustCompile(`(?i)\b(?:could|can|would|will) you\b`),
	// B. Approval / review framings
	regexp.MustCompile(`(?i)\bawaiting\b`),
	regexp.MustCompile(`(?i)\b(?:waiting for|need|needs|require[ds]?) your\b`),
	regexp.MustCompile(`(?i)\bready for (?:your|review|feedback|approval|sign[- ]?off)\b`),
	regexp.MustCompile(`(?i)\bgive me (?:the )?(?:go[- ]?ahead|green light|nod|ok|signal|word)\b`),
	regexp.MustCompile(`(?i)\byour (?:call|choice|move|turn|pick|shout)\b`),
	regexp.MustCompile(`(?i)\b(?:sign[- ]?off|green[- ]?light|go[- ]?ahead)\b`),
	// C. Action gates
	regexp.MustCompile(`(?i)\bbefore (?:I|we)\s+\w+\b`),
	regexp.MustCompile(`(?i)\bonce you(?:'ve| have)?\b`),
	regexp.MustCompile(`(?i)\bI'?ll wait\b`),
	regexp.MustCompile(`(?i)\bstop me if\b`),
	// D. Curated imperatives — multi-word and verb+determiner shapes keep
	// the false-positive surface small ("test failures", "review of the
	// diff", "drop in coverage" don't fire because the determiner gate
	// fails). Bare verb forms only; gerunds ("Trying with caution.",
	// "Verifying locally.") aren't covered — rare in turn-enders.
	regexp.MustCompile(`(?i)\btake a look\b`),
	regexp.MustCompile(`(?i)\b(?:sanity|double)[- ]check\b`),
	regexp.MustCompile(`(?i)\b(?:please|kindly)\s+\w+\b`),
	regexp.MustCompile(`(?i)\b(?:try|confirm|verify|check|review|approve|test|drop|paste|share|reply)\s+(?:the|that|this|it|whether|if)\b`),
	regexp.MustCompile(`(?i)^(?:try|confirm|verify|check|review|approve|test|drop|paste|share|reply)\s+(?:\w+ly|with)\b`),
	// E. Trailing soft asks
	regexp.MustCompile(`(?i)\bwdyt\b`),
	regexp.MustCompile(`(?i)\bthoughts\b[.?!]?\s*$`),
	regexp.MustCompile(`(?i)\b(?:any|any other) (?:feedback|thoughts|concerns|questions)\b`),
	regexp.MustCompile(`(?i)\bis (?:that|this) (?:right|correct|ok|okay|fine|good|what you wanted)\b`),
}

// waitingCueExclusions veto an otherwise-matched cue when the sentence names
// an explicitly non-human wait target. "its"/"their"/"it" as the object of
// wait/waiting unambiguously refer to something already named elsewhere in
// the text (an agent, a job, a process) — never to the user being addressed
// directly, unlike "for you"/"for your". Scoped narrowly to "wait(ing)
// for/on <pronoun>" so it can't accidentally veto an unrelated cue earlier
// in the same sentence. See #897: "I'll wait for its completion
// notification" must not register as waiting on the user.
var waitingCueExclusions = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bwait(?:ing)?\s+(?:for|on)\s+(?:its|their|it)\b`),
}

func isSelfReferentialWait(s string) bool {
	for _, re := range waitingCueExclusions {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// ExtractWaitingCue returns the trailing sentence that carries an imperative
// or implicit waiting cue, or "" when none is present. Unlike
// ExtractQuestionSnippet it does not require a literal '?'; it matches the
// last 1–2 non-empty sentences against waitingCuePatterns. Restricting the
// scan to the tail prevents earlier paragraph content (status notes, code
// snippets) from triggering a spurious waiting verdict.
func ExtractWaitingCue(text string) string {
	if text == "" {
		return ""
	}
	// Walk the tail newest-first so when both the last and second-to-last
	// sentence match a cue, the more recent (and usually more natural for
	// display) sentence is returned.
	tail := lastNonEmptySentences(splitSentences(text), 2)
	for i := len(tail) - 1; i >= 0; i-- {
		s := tail[i]
		stripped := strings.TrimLeft(strings.TrimRight(s, trailingMarkdownNoise), markdownWrapper)
		if stripped == "" {
			continue
		}
		if isSelfReferentialWait(stripped) {
			continue
		}
		for _, re := range allWaitingCuePatterns {
			if re.MatchString(stripped) {
				return s
			}
		}
	}
	return ""
}

// lastNonEmptySentences returns up to n trailing sentences from the input,
// trimmed of surrounding whitespace and with empty entries skipped, in their
// original order.
func lastNonEmptySentences(sentences []string, n int) []string {
	out := make([]string, 0, n)
	for i := len(sentences) - 1; i >= 0 && len(out) < n; i-- {
		t := strings.TrimSpace(sentences[i])
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// answerPrefixes flag a sentence as starting with an explanatory answer to a
// preceding question. Conservative on purpose — connectives that strongly
// imply "this sentence answers the previous question" rather than continuing
// the agent's status report. False negatives (rhetorical Qs we miss) are
// preferable to false positives that would re-break #236's mid-paragraph
// detection.
var answerPrefixes = []string{
	"because ", "because,", "because:",
	"since ", "since,",
}

// isRhetorical reports whether the question at sentences[qIdx] is answered
// by a subsequent sentence in the same paragraph — i.e. a Q&A pair like
// "Why do programmers prefer dark mode? Because light attracts bugs."
func isRhetorical(sentences []string, qIdx int) bool {
	for k := qIdx + 1; k < len(sentences); k++ {
		next := strings.TrimSpace(sentences[k])
		if next == "" {
			continue
		}
		return looksLikeAnswer(next)
	}
	return false
}

func looksLikeAnswer(s string) bool {
	s = strings.TrimLeft(s, markdownWrapper)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, p := range answerPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	for _, p := range i18nAnswerPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// questionTerminators are the runes that end a question sentence across
// scripts: ASCII `?`, full-width `？` (CJK), `؟` (Arabic), and `;`
// (Greek question mark — NOT the ASCII semicolon it visually resembles).
// See issue #933: question detection was ASCII-`?`-only, missing every
// non-Latin question mark.
const questionTerminators = "?？؟;"

// cjkSentenceTerminators additionally end a *sentence* (for splitSentences)
// without requiring a following whitespace/EOS — CJK text is typically
// written without spaces between sentences, unlike Latin/Arabic/Greek.
const cjkSentenceTerminators = "？！。"

func endsWithQuestionMark(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(questionTerminators, r)
}

func isCJKSentenceTerminator(r rune) bool {
	return strings.ContainsRune(cjkSentenceTerminators, r)
}

// splitSentences splits text on sentence terminators (`.`, `!`, `?`, and
// their CJK/Arabic/Greek counterparts) and newlines. A Latin/Arabic/Greek
// terminator only ends a sentence when followed by whitespace, end-of-string,
// or markdown wrappers leading to either — so URL `?` and abbreviations like
// `e.g.` don't split. CJK terminators (`？！。`) end a sentence unconditionally
// since CJK text is conventionally written without inter-sentence spaces.
// Each returned sentence retains its terminator and any wrapper characters.
func splitSentences(text string) []string {
	var sentences []string
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == '\n':
			sentences = append(sentences, text[start:i])
			start = i + size
			i += size
			continue
		case r == '.' || r == '!' || r == '?' || isCJKSentenceTerminator(r) || r == '؟' || r == ';':
			j := i + size
			for j < len(text) && strings.IndexByte(markdownWrapper, text[j]) >= 0 {
				j++
			}
			if isCJKSentenceTerminator(r) || j == len(text) || isSentenceBreak(text[j]) {
				sentences = append(sentences, text[start:j])
				start = j
				i = j
				continue
			}
		}
		i += size
	}
	if start < len(text) {
		sentences = append(sentences, text[start:])
	}
	return sentences
}

func isSentenceBreak(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
