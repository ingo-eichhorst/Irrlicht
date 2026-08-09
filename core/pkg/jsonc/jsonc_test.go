package jsonc

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBlank_ProducesParseableJSONAtIdenticalOffsets pins the two properties
// every caller depends on: the result decodes, and it is the same length with
// the same line structure — which is what lets hookjson's splicer use offsets
// from a comment-blind parser against a file full of comments.
func TestBlank_ProducesParseableJSONAtIdenticalOffsets(t *testing.T) {
	cases := map[string]string{
		"line comment":              "{\n  // note\n  \"a\": 1\n}\n",
		"block comment":             "{\n  /* note */\n  \"a\": 1\n}\n",
		"multiline block":           "{\n  /* one\n     two */\n  \"a\": 1\n}\n",
		"trailing line comment":     "{\n  \"a\": 1 // note\n}\n",
		"comment-looking string":    "{\n  \"a\": \"// not a comment\"\n}\n",
		"block-looking string":      "{\n  \"a\": \"/* not a comment */\"\n}\n",
		"escaped quote in string":   "{\n  \"a\": \"he said \\\" // no\"\n}\n",
		"url in string":             "{\n  \"a\": \"http://example.com\"\n}\n",
		"no comments at all":        "{\n  \"a\": 1\n}\n",
		"comment at eof no newline": "{\n  \"a\": 1\n}\n// trailing",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			blanked := Blank([]byte(src))
			if len(blanked) != len(src) {
				t.Fatalf("length changed: got %d, want %d", len(blanked), len(src))
			}
			if strings.Count(string(blanked), "\n") != strings.Count(src, "\n") {
				t.Errorf("newline count changed\nsrc:     %q\nblanked: %q", src, blanked)
			}
			var v interface{}
			if err := json.Unmarshal(blanked, &v); err != nil {
				t.Errorf("blanked text is not valid JSON: %v\nblanked: %q", err, blanked)
			}
		})
	}
}

// TestBlank_LeavesStringContentAlone guards the one way a comment blanker can
// destroy data: mistaking a `//` inside a string for a comment. A document
// with no comments must come back byte-identical.
func TestBlank_LeavesStringContentAlone(t *testing.T) {
	src := `{"url": "http://example.com/a", "glob": "/*.go", "cmd": "a && b"}`
	if got := string(Blank([]byte(src))); got != src {
		t.Errorf("a document with no comments was modified\nsrc:     %q\nblanked: %q", src, got)
	}
}

// TestBlank_DoesNotWidenJSON is a LOCK, and it is the reason this package is
// safe to share.
//
// hookjson.ReadSettings errors on genuinely malformed input on purpose —
// overwriting such a file would destroy content the user meant to keep
// (hookjson's TestReadSettings_MalformedJSONErrors and
// TestMalformedInput_StillErrors pin that). core/pkg/tailer now runs the same
// blanker, so if blanking ever started accepting a wider dialect, both readers
// would silently widen with it and irrlicht would begin rewriting files it
// only half understands.
//
// Comments are the entire scope. Everything below must still fail to decode
// after blanking.
func TestBlank_DoesNotWidenJSON(t *testing.T) {
	cases := map[string]string{
		"trailing comma":   "{\n  \"a\": 1,\n}\n",
		"single quotes":    `{'a': 1}`,
		"unterminated str": `{"a": "oops}`,
		"unclosed comment": "{\n  /* never closed\n  \"a\": 1\n}\n",
		"bare word":        `{a: 1}`,
		"truncated":        `{"a":`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			var v interface{}
			if err := json.Unmarshal(Blank([]byte(src)), &v); err == nil {
				t.Errorf("Blank(%q) then Unmarshal: got nil error, want a decode error — the blanker must not widen the dialect", src)
			}
		})
	}
}

// TestBlankMask_MarksExactlyTheCommentBytes pins the mask contract hookjson's
// separator arithmetic relies on, including the detail that a newline inside a
// block comment stays a newline in the output but is still marked.
func TestBlankMask_MarksExactlyTheCommentBytes(t *testing.T) {
	src := "{\"a\":1} // tail"
	blanked, mask := BlankMask([]byte(src))

	if len(mask) != len(src) {
		t.Fatalf("mask length %d, want %d", len(mask), len(src))
	}
	const commentStart = 8 // index of the first '/'
	for i := range src {
		want := i >= commentStart
		if mask[i] != want {
			t.Errorf("mask[%d] = %v, want %v (byte %q)", i, mask[i], want, src[i])
		}
	}
	if got := string(blanked); got != "{\"a\":1}        " {
		t.Errorf("blanked = %q, want the comment replaced by spaces", got)
	}
}

func TestBlankMask_BlockCommentKeepsNewlinesButMarksThem(t *testing.T) {
	src := "{\n  /* one\n     two */\n  \"a\": 1\n}\n"
	blanked, mask := BlankMask([]byte(src))

	if strings.Count(string(blanked), "\n") != strings.Count(src, "\n") {
		t.Errorf("newlines not preserved\nblanked: %q", blanked)
	}
	// The newline that sits inside the block comment must be marked even
	// though it survives into the blanked text.
	inner := strings.Index(src, "one\n") + len("one")
	if src[inner] != '\n' {
		t.Fatalf("test setup: expected a newline at %d, got %q", inner, src[inner])
	}
	if !mask[inner] {
		t.Error("newline inside a block comment is not marked as comment")
	}
	if blanked[inner] != '\n' {
		t.Errorf("newline inside a block comment was blanked to %q, want it preserved", blanked[inner])
	}
}

// TestBlank_EmptyInput is the boundary the byte loops are easiest to get wrong.
func TestBlank_EmptyInput(t *testing.T) {
	blanked, mask := BlankMask(nil)
	if len(blanked) != 0 || len(mask) != 0 {
		t.Errorf("BlankMask(nil) = %q, %v; want empty, empty", blanked, mask)
	}
}
