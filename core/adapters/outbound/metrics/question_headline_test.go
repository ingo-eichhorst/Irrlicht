package metrics

import (
	"strings"
	"testing"

	"irrlicht/core/adapters/outbound/compaction"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/pkg/tailer"
)

// These exercise the production question-headline wiring end to end — the
// deterministic compactor behind the shared converter — for issue #1186. The
// unit-level pieces (topic derivation, verbatim compaction, routing) are
// tested in their own packages; this proves they compose as shipped.
func questionHeadlineFor(m *tailer.SessionMetrics) string {
	return replayengine.NewMetricsConverter(compaction.DeterministicCompactor{}).Convert(m).QuestionHeadline
}

func TestQuestionHeadline_MarkerKeptVerbatim_MultiSentence(t *testing.T) {
	// AC1: an agent-authored marker carrying a topic prefix across two
	// sentences survives whole — sentence selection would drop the topic.
	m := &tailer.SessionMetrics{
		LastAssistantText: "a long rambling status with a question buried in it?",
		TaskQuestion:      &tailer.TaskQuestion{Text: "Auth refactor: 2 endpoints still untested. Ship now or add tests first?", ObservedAt: 100},
	}
	got := questionHeadlineFor(m)
	want := "Auth refactor: 2 endpoints still untested. Ship now or add tests first?"
	if got != want {
		t.Errorf("QuestionHeadline = %q, want the marker verbatim with its prefix intact", got)
	}
}

func TestQuestionHeadline_RegexPath_ComposesTopicPrefix(t *testing.T) {
	// AC2: no marker — prefix a 3–5 word topic from the first prompt onto the
	// extracted question, joined by ": ".
	m := &tailer.SessionMetrics{
		FirstUserText:     "Add OAuth login to the web dashboard",
		LastAssistantText: "Here's the plan. Should I use PKCE or the implicit flow?",
	}
	got := questionHeadlineFor(m)
	want := "Add OAuth login: Should I use PKCE or the implicit flow?"
	if got != want {
		t.Errorf("QuestionHeadline = %q, want %q", got, want)
	}
}

func TestQuestionHeadline_RegexPath_StripsSlashCommandFromPrefix(t *testing.T) {
	// AC3: a slash-command first prompt must not seed the topic with "/ir:exec".
	m := &tailer.SessionMetrics{
		FirstUserText:     "/ir:exec 1042 push this through",
		LastAssistantText: "Should I proceed?",
	}
	got := questionHeadlineFor(m)
	if strings.HasPrefix(got, "/ir:exec") {
		t.Errorf("QuestionHeadline = %q, want no leading slash command in the topic", got)
	}
	if !strings.HasSuffix(got, ": Should I proceed?") {
		t.Errorf("QuestionHeadline = %q, want a topic prefix then the question", got)
	}
}

func TestQuestionHeadline_RegexPath_NoDoublePrefix(t *testing.T) {
	// AC4: the question already opens with the derived topic — don't prepend a
	// second copy.
	m := &tailer.SessionMetrics{
		FirstUserText:     "Add OAuth login to the web dashboard",
		LastAssistantText: "Add OAuth login now, or add tests first?",
	}
	got := questionHeadlineFor(m)
	want := "Add OAuth login now, or add tests first?"
	if got != want {
		t.Errorf("QuestionHeadline = %q, want no double topic prefix (%q)", got, want)
	}
}

func TestQuestionHeadline_RegexPath_CapKeepsQuestion(t *testing.T) {
	// The composed headline stays within the rune budget and never truncates
	// away the question to leave only the topic.
	longQuestion := "Should I " + strings.Repeat("carefully weigh every option ", 20) + "now?"
	m := &tailer.SessionMetrics{
		FirstUserText:     "Refactor the auth layer",
		LastAssistantText: longQuestion,
	}
	got := questionHeadlineFor(m)
	if !strings.HasPrefix(got, "Refactor the auth layer: Should I ") {
		t.Errorf("QuestionHeadline = %q, want the topic + start of the question preserved", got)
	}
	if strings.HasSuffix(got, "auth layer:") || strings.HasSuffix(got, "auth layer: ") {
		t.Errorf("QuestionHeadline = %q, cap left only the topic", got)
	}
	if runes := []rune(got); len(runes) > 200 {
		t.Errorf("QuestionHeadline = %d runes, want <= 200", len(runes))
	}
}
