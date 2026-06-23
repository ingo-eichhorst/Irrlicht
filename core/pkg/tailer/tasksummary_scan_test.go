package tailer

import (
	"strings"
	"testing"
	"time"
)

var summaryObservedAt = time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)

func TestScanTaskSummary_HappyPath(t *testing.T) {
	text := `Starting. <!-- {"marker":"irrlicht-summary","summary":"Add a logout button to the navbar"} --> Next.`
	s := ScanTaskSummary(text, summaryObservedAt)
	if s == nil {
		t.Fatal("expected summary, got nil")
	}
	if s.Text != "Add a logout button to the navbar" {
		t.Errorf("Text = %q", s.Text)
	}
	if s.ObservedAt != summaryObservedAt.Unix() {
		t.Errorf("ObservedAt = %d, want %d", s.ObservedAt, summaryObservedAt.Unix())
	}
}

func TestScanTaskSummary_MarkerKeyVariants(t *testing.T) {
	for _, marker := range []string{"irrlicht-summary", "irrlicht_summary"} {
		text := `<!-- {"marker":"` + marker + `","summary":"do the thing"} -->`
		if ScanTaskSummary(text, summaryObservedAt) == nil {
			t.Errorf("marker key %q should be accepted", marker)
		}
	}
}

func TestScanTaskSummary_TextKeyDrift(t *testing.T) {
	for _, key := range []string{"summary", "text", "title"} {
		text := `<!-- {"marker":"irrlicht-summary","` + key + `":"investigate the flaky test"} -->`
		s := ScanTaskSummary(text, summaryObservedAt)
		if s == nil || s.Text != "investigate the flaky test" {
			t.Errorf("alias %q: s = %+v", key, s)
		}
	}
}

func TestScanTaskSummary_MalformedIgnored(t *testing.T) {
	for name, text := range map[string]string{
		"truncated json":  `<!-- {"marker":"irrlicht-summary","summary": -->`,
		"not json":        `<!-- hello world -->`,
		"no summary key":  `<!-- {"marker":"irrlicht-summary"} -->`,
		"empty summary":   `<!-- {"marker":"irrlicht-summary","summary":""} -->`,
		"foreign marker":  `<!-- {"marker":"irrlicht-eta","summary":"x"} -->`,
		"missing marker":  `<!-- {"summary":"no marker key here"} -->`,
		"non-string text": `<!-- {"marker":"irrlicht-summary","summary":42} -->`,
	} {
		if s := ScanTaskSummary(text, summaryObservedAt); s != nil {
			t.Errorf("%s: expected nil, got %+v", name, s)
		}
	}
}

func TestScanTaskSummary_CleansWhitespaceAndControlChars(t *testing.T) {
	text := "<!-- {\"marker\":\"irrlicht-summary\",\"summary\":\"  fix\\nthe\\t bug  \"} -->"
	s := ScanTaskSummary(text, summaryObservedAt)
	if s == nil || s.Text != "fix the bug" {
		t.Fatalf("s = %+v, want collapsed single line", s)
	}
}

func TestScanTaskSummary_CapsLength(t *testing.T) {
	long := strings.Repeat("a", maxTaskSummaryRunes+50)
	text := `<!-- {"marker":"irrlicht-summary","summary":"` + long + `"} -->`
	s := ScanTaskSummary(text, summaryObservedAt)
	if s == nil {
		t.Fatal("expected summary, got nil")
	}
	if got := len([]rune(s.Text)); got > maxTaskSummaryRunes {
		t.Errorf("length = %d runes, want <= %d", got, maxTaskSummaryRunes)
	}
}

func TestScanTaskSummary_LatestWins(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-summary","summary":"first task"} -->
prose
<!-- {"marker":"irrlicht-summary","summary":"second task"} -->`
	s := ScanTaskSummary(text, summaryObservedAt)
	if s == nil || s.Text != "second task" {
		t.Fatalf("s = %+v, want latest marker", s)
	}
}

func TestScanTaskSummary_LatestInvalidDoesNotShadowEarlierValid(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-summary","summary":"real task"} -->
<!-- {"marker":"irrlicht-summary","summary":""} -->`
	s := ScanTaskSummary(text, summaryObservedAt)
	if s == nil || s.Text != "real task" {
		t.Fatalf("s = %+v, want earlier valid marker", s)
	}
}

func TestScanTaskSummary_NoMarker(t *testing.T) {
	if s := ScanTaskSummary("plain prose, no comments at all", summaryObservedAt); s != nil {
		t.Fatalf("expected nil, got %+v", s)
	}
}
