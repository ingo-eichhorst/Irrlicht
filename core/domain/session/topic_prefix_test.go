package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDeriveTopicPrefix(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \n\t ", ""},
		{"short prompt kept whole", "Fix the login loop", "Fix the login loop"},
		{"trims trailing stopword", "Add OAuth login to the web dashboard", "Add OAuth login"},
		{"caps at five words", "one two three four five six seven", "one two three four five"},
		{"strips leading slash command", "/ir:exec 1042 push this through", "1042 push this through"},
		{"slash command only leaves nothing usable after cap", "/help", ""},
		{"collapses whitespace", "DB    migration\tready now", "DB migration ready now"},
		{"drops trailing punctuation", "Auth refactor:", "Auth refactor"},
		{"keeps single non-stopword word", "the", "the"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveTopicPrefix(tc.prompt); got != tc.want {
				t.Errorf("DeriveTopicPrefix(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestDeriveTopicPrefix_CapsLongSingleWord(t *testing.T) {
	// A pasted URL is one whitespace-delimited "word"; it must not blow the
	// topic budget (issue #1186).
	url := "https://example.com/" + strings.Repeat("a", 200)
	got := DeriveTopicPrefix(url)
	if utf8.RuneCountInString(got) > maxTopicPrefixRunes {
		t.Errorf("DeriveTopicPrefix(url) = %d runes, want <= %d", utf8.RuneCountInString(got), maxTopicPrefixRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("DeriveTopicPrefix(url) = %q, want a trailing ellipsis when capped", got)
	}
}

func TestQuestionHasTopicPrefix(t *testing.T) {
	cases := []struct {
		name     string
		question string
		topic    string
		want     bool
	}{
		{"question starts with topic", "Add OAuth login now or later?", "Add OAuth login", true},
		{"case-insensitive start match", "add oauth LOGIN now?", "Add OAuth login", true},
		{"punctuation boundary after topic", "Add OAuth login, or add tests first?", "Add OAuth login", true},
		{"short topic is not a mid-word prefix", "Additional context needed?", "Add", false},
		{"question carries its own colon lead", "Auth refactor: ship now or wait?", "Something else", true},
		{"no prefix present", "Should I use PKCE or implicit?", "Auth refactor", false},
		{"mid-sentence colon is not a topic lead", "Should I use this ratio 3:2 or 16:9?", "Layout", false},
		{"long lead before colon is not a topic", "This is a very long clause that runs well past a short topic: decide?", "X", false},
		{"empty topic and no colon lead", "Should I proceed?", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuestionHasTopicPrefix(tc.question, tc.topic); got != tc.want {
				t.Errorf("QuestionHasTopicPrefix(%q, %q) = %v, want %v", tc.question, tc.topic, got, tc.want)
			}
		})
	}
}
