package hookjson

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// --- comment blanking ---

// TestBlankComments_PreservesOffsets is the property the whole splicer rests on:
// a byte offset taken from the blanked text indexes the same byte of the
// original. If blanking ever changed length, every span in this package would
// silently point somewhere else.
func TestBlankComments_PreservesOffsets(t *testing.T) {
	cases := map[string]string{
		"line comment":            "{\n  // note\n  \"a\": 1\n}\n",
		"block comment":           "{\n  /* note */\n  \"a\": 1\n}\n",
		"multiline block":         "{\n  /* one\n     two */\n  \"a\": 1\n}\n",
		"trailing line comment":   "{\n  \"a\": 1 // note\n}\n",
		"comment-looking string":  "{\n  \"a\": \"// not a comment\"\n}\n",
		"block-looking string":    "{\n  \"a\": \"/* not a comment */\"\n}\n",
		"escaped quote in string": "{\n  \"a\": \"he said \\\" // no\"\n}\n",
		"url in string":           "{\n  \"a\": \"http://example.com\"\n}\n",
		"no comments at all":      "{\n  \"a\": 1\n}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			blanked := blankComments([]byte(src))
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

// TestBlankComments_LeavesStringContentAlone guards the one way a comment
// blanker can destroy data: mistaking a `//` inside a string for a comment.
func TestBlankComments_LeavesStringContentAlone(t *testing.T) {
	src := `{"url": "http://example.com/a", "glob": "/*.go", "cmd": "a && b"}`
	blanked := string(blankComments([]byte(src)))
	if blanked != src {
		t.Errorf("a document with no comments was modified\nsrc:     %q\nblanked: %q", src, blanked)
	}
}

// --- splicing ---

// spliceTo is the codec under test end to end: decode a document, apply a
// mutation to the decoded map, and splice the result back.
func spliceTo(t *testing.T, original string, mutate func(map[string]interface{})) string {
	t.Helper()
	var settings map[string]interface{}
	if err := json.Unmarshal(blankComments([]byte(original)), &settings); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	mutate(settings)
	out, err := spliceSettings([]byte(original), settings)
	if err != nil {
		t.Fatalf("spliceSettings: %v", err)
	}
	if err := json.Unmarshal(blankComments(out), new(interface{})); err != nil {
		t.Fatalf("spliced output is not parseable: %v\n%s", err, out)
	}
	return string(out)
}

func TestSplice_NoChangeIsByteIdentical(t *testing.T) {
	got := spliceTo(t, jsoncSettings, func(map[string]interface{}) {})
	if got != jsoncSettings {
		t.Errorf("an unchanged document was rewritten\n--- want ---\n%s\n--- got ---\n%s", jsoncSettings, got)
	}
}

func TestSplice_ScalarChangeTouchesOnlyThatValue(t *testing.T) {
	got := spliceTo(t, jsoncSettings, func(s map[string]interface{}) {
		s["theme"] = "Dracula"
	})
	if !strings.Contains(got, `"theme": "Dracula"`) {
		t.Errorf("theme not updated\n%s", got)
	}
	if !strings.Contains(got, `"npm run build && npm run list-tools"`) {
		t.Errorf("an untouched value was re-serialized\n%s", got)
	}
	if !strings.Contains(got, "/* Appearance first") {
		t.Errorf("a comment was lost\n%s", got)
	}
	assertKeyOrder(t, got, []string{"theme", "selectedAuthType", "sandbox", "autoAccept"})
}

// TestSplice_TrailingCommentOnLastMember covers the case where the naive
// "append a comma after the last value" rule produces a file that no longer
// parses: the comma would land inside a `//` comment. The comma has to go
// before the comment and the new member after it.
func TestSplice_TrailingCommentOnLastMember(t *testing.T) {
	const src = "{\n  \"a\": 1  // keep me\n}\n"
	got := spliceTo(t, src, func(s map[string]interface{}) {
		s["b"] = 2
	})
	if !strings.Contains(got, "// keep me") {
		t.Errorf("trailing comment lost\n%s", got)
	}
	if strings.Contains(got, "// keep me,") {
		t.Errorf("comma was buried inside the comment\n%s", got)
	}
}

// TestSplice_RoundTripOfAddThenRemove is the invertibility property that makes
// install-then-uninstall lossless, exercised directly on the codec across the
// formatting shapes the separator arithmetic branches on.
func TestSplice_RoundTripOfAddThenRemove(t *testing.T) {
	cases := map[string]string{
		"multiline":                jsoncSettings,
		"trailing comment":         "{\n  \"a\": 1  // keep me\n}\n",
		"empty object":             "{}\n",
		"single line":              `{"a": 1}`,
		"tab indented":             "{\n\t\"a\": 1\n}\n",
		"comment before close":     "{\n  \"a\": 1\n  // dangling\n}\n",
		"blank lines between keys": "{\n  \"a\": 1,\n\n  \"b\": 2\n}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			added := spliceTo(t, src, func(s map[string]interface{}) {
				s["hooks"] = map[string]interface{}{"Stop": []interface{}{"x"}}
			})
			if !strings.Contains(added, `"hooks"`) {
				t.Fatalf("hooks not added\n%s", added)
			}
			back := spliceTo(t, added, func(s map[string]interface{}) {
				delete(s, "hooks")
			})
			if back != src {
				t.Errorf("add+remove was not lossless\n--- want ---\n%q\n--- got ---\n%q\n--- intermediate ---\n%s", src, back, added)
			}
		})
	}
}

// TestSplice_ArrayAppendAndRemove covers the same invertibility one level down,
// where hook matcher groups actually live.
func TestSplice_ArrayAppendAndRemove(t *testing.T) {
	const src = "{\n  \"hooks\": {\n    \"Stop\": [\n      { \"mine\": true }\n    ]\n  }\n}\n"

	added := spliceTo(t, src, func(s map[string]interface{}) {
		hooks := s["hooks"].(map[string]interface{})
		stop := hooks["Stop"].([]interface{})
		hooks["Stop"] = append(stop, map[string]interface{}{"ours": true})
	})
	if !strings.Contains(added, `"ours"`) {
		t.Fatalf("group not appended\n%s", added)
	}
	if !strings.Contains(added, `{ "mine": true }`) {
		t.Errorf("the user's existing group was re-serialized rather than left alone\n%s", added)
	}

	back := spliceTo(t, added, func(s map[string]interface{}) {
		hooks := s["hooks"].(map[string]interface{})
		stop := hooks["Stop"].([]interface{})
		hooks["Stop"] = stop[:len(stop)-1]
	})
	if back != src {
		t.Errorf("append+remove was not lossless\n--- want ---\n%q\n--- got ---\n%q", src, back)
	}
}

// TestSplice_EditInsideArrayElementKeepsSiblings pins that an in-place upgrade
// of one hook entry does not rewrite the entries around it.
func TestSplice_EditInsideArrayElementKeepsSiblings(t *testing.T) {
	const src = "{\n  \"list\": [\n    { \"keep\": \"a && b\" },\n    { \"change\": 1 }\n  ]\n}\n"
	got := spliceTo(t, src, func(s map[string]interface{}) {
		list := s["list"].([]interface{})
		list[1].(map[string]interface{})["change"] = 2
	})
	if !strings.Contains(got, `{ "keep": "a && b" }`) {
		t.Errorf("sibling element was reformatted\n%s", got)
	}
	if !strings.Contains(got, `"change": 2`) {
		t.Errorf("target not updated\n%s", got)
	}
}

func TestSplice_CRLFDocumentKeepsCRLF(t *testing.T) {
	const src = "{\r\n  \"a\": 1\r\n}\r\n"
	got := spliceTo(t, src, func(s map[string]interface{}) { s["b"] = 2 })
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("a bare LF was introduced into a CRLF document\n%q", got)
	}
}

// --- locks: behavior that must NOT change (issue #1371 scope note 4) ---
//
// These pass on main by construction. They are here so that the leniency added
// for comments cannot quietly grow into leniency for broken files, which is a
// separate decision belonging to issue #1362.

func TestMalformedInput_StillErrors(t *testing.T) {
	cases := map[string]string{
		"truncated":        `{"hooks":`,
		"trailing comma":   "{\n  \"a\": 1,\n}\n",
		"single quotes":    `{'a': 1}`,
		"unterminated str": `{"a": "oops}`,
		"unclosed comment": "{\n  /* never closed\n  \"a\": 1\n}\n",
		"bare word":        `{a: 1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadSettings(seedRaw(t, "bad.json", body)); err == nil {
				t.Errorf("ReadSettings(%q): got nil error, want a decode error", body)
			}
		})
	}
}

// TestMalformedInput_IsNeverOverwritten is the consequence that actually matters
// to a user: a config we cannot parse is left exactly as they wrote it.
func TestMalformedInput_IsNeverOverwritten(t *testing.T) {
	const broken = "{\n  \"hooks\": [[[\n"
	path := seedRaw(t, "broken.json", broken)

	if _, err := EnsureInstalled(httpConfig(path)); err == nil {
		t.Error("EnsureInstalled on a malformed file: got nil error, want a decode error")
	}
	if got := readRawFile(t, path); got != broken {
		t.Errorf("malformed file was rewritten\n--- want ---\n%q\n--- got ---\n%q", broken, got)
	}
}

// TestWriteSettings_FreshFileIsPlainEncode pins the no-original path, which is
// the one the existing shape lock covers and the one every brand-new install
// takes.
func TestWriteSettings_FreshFileIsPlainEncode(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/new.json"
	err := WriteSettings(path, map[string]interface{}{"cmd": "a && b"}, func(_ string, data []byte) error {
		return os.WriteFile(path, data, 0o600)
	})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	got := readRawFile(t, path)
	if want := "{\n  \"cmd\": \"a && b\"\n}\n"; got != want {
		t.Errorf("fresh encode = %q, want %q", got, want)
	}
}
