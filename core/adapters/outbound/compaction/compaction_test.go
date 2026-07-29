package compaction

import (
	"strings"
	"testing"

	"irrlicht/core/ports/outbound"
)

func TestNoopCompactor_Identity(t *testing.T) {
	in := "## Heading\n\nsome **bold** text with a `code` span and a question?"
	for _, kind := range []outbound.CompactKind{outbound.CompactIntent, outbound.CompactQuestion} {
		if got := (NoopCompactor{}).Compact(in, kind); got != in {
			t.Errorf("kind %d: Compact = %q, want identity", kind, got)
		}
	}
}

func TestDeterministic_Empty(t *testing.T) {
	for _, kind := range []outbound.CompactKind{outbound.CompactIntent, outbound.CompactQuestion} {
		if got := (DeterministicCompactor{}).Compact("", kind); got != "" {
			t.Errorf("kind %d: Compact(\"\") = %q, want empty", kind, got)
		}
		if got := (DeterministicCompactor{}).Compact("   \n\t  ", kind); got != "" {
			t.Errorf("kind %d: Compact(whitespace) = %q, want empty", kind, got)
		}
	}
}

func TestDeterministic_Intent_FirstSentence(t *testing.T) {
	in := "Add a logout button to the navbar. Then wire it to the auth service. And test it."
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactIntent)
	if got != "Add a logout button to the navbar." {
		t.Errorf("Compact = %q, want first sentence only", got)
	}
}

func TestDeterministic_Question_UsesQuestionSnippet(t *testing.T) {
	in := "Here's the plan with lots of detail. Should I run the migration now? I can also wait."
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactQuestion)
	if got != "Should I run the migration now?" {
		t.Errorf("Compact = %q, want the question sentence", got)
	}
}

func TestDeterministic_QuestionVerbatim_KeepsAllSentences(t *testing.T) {
	// A multi-sentence, already-composed "<topic>: <question>" marker must
	// survive whole — CompactQuestion would sentence-select and drop the topic
	// (issue #1186).
	in := "Auth refactor: 2 endpoints still untested. Ship now or add tests first?"
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactQuestionVerbatim)
	if got != in {
		t.Errorf("Compact(verbatim) = %q, want the whole line kept", got)
	}
	// Contrast: CompactQuestion selects a single sentence and loses the topic.
	if q := (DeterministicCompactor{}).Compact(in, outbound.CompactQuestion); q == in {
		t.Errorf("CompactQuestion should sentence-select, got the whole line %q", q)
	}
}

func TestDeterministic_QuestionVerbatim_StripsAndCaps(t *testing.T) {
	// Verbatim still strips noise/markdown, collapses whitespace and caps runes.
	in := "Topic:   fix the **flaky** `TestThing`   now?"
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactQuestionVerbatim)
	if got != "Topic: fix the flaky TestThing now?" {
		t.Errorf("Compact(verbatim) = %q, want stripped + whitespace-collapsed", got)
	}

	long := "Topic: " + strings.Repeat("word ", 100)
	capped := (DeterministicCompactor{}).Compact(long, outbound.CompactQuestionVerbatim)
	runes := []rune(capped)
	if len(runes) > maxHeadlineRunes {
		t.Errorf("verbatim len = %d runes, want <= %d", len(runes), maxHeadlineRunes)
	}
	if !strings.HasPrefix(capped, "Topic: ") {
		t.Errorf("verbatim cap dropped the leading topic: %q", capped)
	}
}

func TestDeterministic_Question_FallsBackToFirstLine(t *testing.T) {
	in := "Let me know how you want to proceed.\nSecond line should be ignored."
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactQuestion)
	if got != "Let me know how you want to proceed." {
		t.Errorf("Compact = %q, want first non-empty line when no literal question", got)
	}
}

func TestDeterministic_StripsCodeCommentsAndReminders(t *testing.T) {
	in := "Refactor the parser.\n" +
		"```go\nfunc x() {}\n```\n" +
		"<!-- {\"marker\":\"irrlicht-summary\",\"summary\":\"noise\"} -->\n" +
		"<system-reminder>do not surface this</system-reminder>"
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactIntent)
	if got != "Refactor the parser." {
		t.Errorf("Compact = %q, want noise stripped", got)
	}
	if strings.Contains(got, "marker") || strings.Contains(got, "system-reminder") || strings.Contains(got, "func x") {
		t.Errorf("Compact = %q, leaked stripped noise", got)
	}
}

func TestDeterministic_StripsInlineMarkdown(t *testing.T) {
	in := "Fix the **flaky** `TestThing` and see [the docs](https://example.com)?"
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactQuestion)
	want := "Fix the flaky TestThing and see the docs?"
	if got != want {
		t.Errorf("Compact = %q, want %q", got, want)
	}
}

func TestDeterministic_CapsToMaxRunesWithEllipsis(t *testing.T) {
	long := "Implement " + strings.Repeat("a very long intent ", 20) // no sentence terminator
	got := (DeterministicCompactor{}).Compact(long, outbound.CompactIntent)
	runes := []rune(got)
	if len(runes) > maxHeadlineRunes {
		t.Errorf("len = %d runes, want <= %d", len(runes), maxHeadlineRunes)
	}
	if runes[len(runes)-1] != '…' {
		t.Errorf("Compact = %q, want trailing ellipsis", got)
	}
}

func TestDeterministic_CollapsesWhitespace(t *testing.T) {
	in := "Add   the\tbutton now."
	got := (DeterministicCompactor{}).Compact(in, outbound.CompactIntent)
	if got != "Add the button now." {
		t.Errorf("Compact = %q, want collapsed whitespace", got)
	}
}
