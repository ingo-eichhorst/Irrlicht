package costreport

import (
	"strings"
	"testing"
)

// TestDocCommentForCatchesEveryKnownShape is DocCommentFor's committed mutation
// evidence. The want:false rows carry the value, per #1450: a matcher that
// returned the whole file would satisfy every positive row here.
func TestDocCommentForCatchesEveryKnownShape(t *testing.T) {
	const src = `package p

// alpha's doc.
// regenerate: go test -run TestX
const alpha = 1

const beta = 2

// gamma's doc, with no pointer.
type gamma struct{}

// a floating comment, separated by a blank line.

const delta = 4
`
	cases := []struct {
		what      string
		symbol    string
		wantFound bool
		wantIn    string
		wantNotIn string
	}{
		{
			what:      "a declaration with a doc comment carrying the marker",
			symbol:    "const alpha",
			wantFound: true,
			wantIn:    RegenerateMarker,
		},
		{
			what:      "a declaration with no doc comment at all is FOUND and empty — not missing",
			symbol:    "const beta",
			wantFound: true,
			wantNotIn: RegenerateMarker,
		},
		{
			what:      "a type declaration is reached the same way a const is",
			symbol:    "type gamma",
			wantFound: true,
			wantNotIn: RegenerateMarker,
		},
		{
			what:      "a blank line ends the doc block, so a floating comment is not adopted",
			symbol:    "const delta",
			wantFound: true,
			wantNotIn: "floating comment",
		},
		{
			what:      "a symbol that is not there is reported absent, never as an empty doc",
			symbol:    "const epsilon",
			wantFound: false,
		},
		{
			// The limit, pinned rather than stated: the match is a prefix of a
			// trimmed line, so a declaration named as a substring of another is
			// not distinguished. Learned from a test instead of from an incident.
			what:      "a prefix match is what this is, and `const alph` reaches `const alpha`",
			symbol:    "const alph",
			wantFound: true,
			wantIn:    RegenerateMarker,
		},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			doc, found := DocCommentFor(src, tc.symbol)
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v (doc = %q)", found, tc.wantFound, doc)
			}
			if tc.wantIn != "" && !strings.Contains(doc, tc.wantIn) {
				t.Errorf("doc does not contain %q: %q", tc.wantIn, doc)
			}
			if tc.wantNotIn != "" && strings.Contains(doc, tc.wantNotIn) {
				t.Errorf("doc wrongly contains %q: %q", tc.wantNotIn, doc)
			}
		})
	}
}
