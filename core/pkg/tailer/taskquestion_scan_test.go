package tailer

import (
	"strings"
	"testing"
	"time"
)

var questionObservedAt = time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)

func TestScanTaskQuestion_HappyPath(t *testing.T) {
	text := `Done. <!-- {"marker":"irrlicht-question","question":"Run the migration now?"} --> Waiting.`
	q := ScanTaskQuestion(text, questionObservedAt)
	if q == nil {
		t.Fatal("expected question, got nil")
	}
	if q.Text != "Run the migration now?" {
		t.Errorf("Text = %q", q.Text)
	}
	if q.ObservedAt != questionObservedAt.Unix() {
		t.Errorf("ObservedAt = %d, want %d", q.ObservedAt, questionObservedAt.Unix())
	}
}

func TestScanTaskQuestion_MarkerKeyVariants(t *testing.T) {
	for _, marker := range []string{"irrlicht-question", "irrlicht_question"} {
		text := `<!-- {"marker":"` + marker + `","question":"proceed?"} -->`
		if ScanTaskQuestion(text, questionObservedAt) == nil {
			t.Errorf("marker key %q should be accepted", marker)
		}
	}
}

func TestScanTaskQuestion_TextKeyDrift(t *testing.T) {
	for _, key := range []string{"question", "text"} {
		text := `<!-- {"marker":"irrlicht-question","` + key + `":"which option?"} -->`
		q := ScanTaskQuestion(text, questionObservedAt)
		if q == nil || q.Text != "which option?" {
			t.Errorf("alias %q: q = %+v", key, q)
		}
	}
}

func TestScanTaskQuestion_MalformedIgnored(t *testing.T) {
	for name, text := range map[string]string{
		"truncated json":  `<!-- {"marker":"irrlicht-question","question": -->`,
		"not json":        `<!-- hello world -->`,
		"no question key": `<!-- {"marker":"irrlicht-question"} -->`,
		"empty question":  `<!-- {"marker":"irrlicht-question","question":""} -->`,
		"foreign marker":  `<!-- {"marker":"irrlicht-summary","question":"x"} -->`,
		"missing marker":  `<!-- {"question":"no marker key here"} -->`,
		"non-string text": `<!-- {"marker":"irrlicht-question","question":42} -->`,
	} {
		if q := ScanTaskQuestion(text, questionObservedAt); q != nil {
			t.Errorf("%s: expected nil, got %+v", name, q)
		}
	}
}

func TestScanTaskQuestion_CleansWhitespaceAndControlChars(t *testing.T) {
	text := "<!-- {\"marker\":\"irrlicht-question\",\"question\":\"  run\\nthe\\t migration?  \"} -->"
	q := ScanTaskQuestion(text, questionObservedAt)
	if q == nil || q.Text != "run the migration?" {
		t.Fatalf("q = %+v, want collapsed single line", q)
	}
}

func TestScanTaskQuestion_CapsLength(t *testing.T) {
	long := strings.Repeat("a", maxTaskSummaryRunes+50)
	text := `<!-- {"marker":"irrlicht-question","question":"` + long + `"} -->`
	q := ScanTaskQuestion(text, questionObservedAt)
	if q == nil {
		t.Fatal("expected question, got nil")
	}
	if got := len([]rune(q.Text)); got > maxTaskSummaryRunes {
		t.Errorf("length = %d runes, want <= %d", got, maxTaskSummaryRunes)
	}
}

func TestScanTaskQuestion_LatestWins(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-question","question":"first?"} -->
prose
<!-- {"marker":"irrlicht-question","question":"second?"} -->`
	q := ScanTaskQuestion(text, questionObservedAt)
	if q == nil || q.Text != "second?" {
		t.Fatalf("q = %+v, want latest marker", q)
	}
}

func TestScanTaskQuestion_LatestInvalidDoesNotShadowEarlierValid(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-question","question":"real?"} -->
<!-- {"marker":"irrlicht-question","question":""} -->`
	q := ScanTaskQuestion(text, questionObservedAt)
	if q == nil || q.Text != "real?" {
		t.Fatalf("q = %+v, want earlier valid marker", q)
	}
}

func TestScanTaskQuestion_NoMarker(t *testing.T) {
	if q := ScanTaskQuestion("plain prose, no comments at all", questionObservedAt); q != nil {
		t.Fatalf("expected nil, got %+v", q)
	}
}
