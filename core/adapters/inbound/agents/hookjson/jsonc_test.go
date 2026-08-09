package hookjson

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/rand"
	"os"
	"reflect"
	"slices"
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

// The plain-truncated case lives in TestReadSettings_MalformedJSONErrors; these
// are the shapes a JSONC-tolerant reader might newly be tempted to accept.
func TestMalformedInput_StillErrors(t *testing.T) {
	cases := map[string]string{
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

// TestWriteSettings_FreshFileIsPlainEncode complements
// TestWriteSettings_ShapeAndDelegation rather than repeating it: that one pins
// the shape through a fake writer, this one goes through a real file on disk
// (the no-original branch of WriteSettings' own re-read) and adds the assertion
// that a fresh encode does not HTML-escape either.
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

// --- regressions from the review of PR #1381 ---
//
// Every test in this section was seen red against the first version of the
// splicer. They cover the separator arithmetic in positions the original suite
// never exercised: it only ever removed the *tail* of a container, which is the
// one position where the arithmetic happened to be right.

// TestSplice_RemoveMiddleMember: removing an item with survivors on both sides
// must consume exactly one of the two commas around it. Keeping both wrote
// `"a": 1,,` — an unparseable settings.json — and it fired on ~11% of randomly
// shaped documents.
func TestSplice_RemoveMiddleMember(t *testing.T) {
	const src = "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n"
	got := spliceTo(t, src, func(s map[string]interface{}) { delete(s, "b") })
	if want := "{\n  \"a\": 1,\n  \"c\": 3\n}\n"; got != want {
		t.Errorf("middle removal\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

func TestSplice_RemoveMiddleArrayElement(t *testing.T) {
	const src = "{\n  \"list\": [\n    1,\n    2,\n    3\n  ]\n}\n"
	got := spliceTo(t, src, func(s map[string]interface{}) {
		list := s["list"].([]interface{})
		s["list"] = []interface{}{list[0], list[2]}
	})
	if want := "{\n  \"list\": [\n    1,\n    3\n  ]\n}\n"; got != want {
		t.Errorf("middle element removal\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

// TestSplice_RemoveHeadMember: the head has no preceding comma, so the trailing
// one goes instead — and the item's whole line goes with it rather than leaving
// a blank, wrongly-indented line behind.
func TestSplice_RemoveHeadMember(t *testing.T) {
	const src = "{\n  \"a\": 1,\n  \"b\": 2\n}\n"
	got := spliceTo(t, src, func(s map[string]interface{}) { delete(s, "a") })
	if want := "{\n  \"b\": 2\n}\n"; got != want {
		t.Errorf("head removal\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

// TestSplice_AppendAnchorsOnLastSurvivor: when the last item is removed in the
// same pass that adds one, the new comma must be emitted at the last *surviving*
// item. Anchoring on the removed one put the comma at an offset the deletion
// splices away, landing it inside the previous item's `//` trailer — which
// comments out the separator and breaks the file.
func TestSplice_AppendAnchorsOnLastSurvivor(t *testing.T) {
	const src = "{\n  \"a\": 1, // trail\n  \"b\": 2\n}\n"
	got := spliceTo(t, src, func(s map[string]interface{}) {
		delete(s, "b")
		s["z"] = 9
	})
	if !strings.Contains(got, "// trail") {
		t.Errorf("trailing comment lost\n%s", got)
	}
	if strings.Contains(got, "// trail,") {
		t.Errorf("separator was buried inside the comment\n%s", got)
	}
	if !strings.Contains(got, `"z": 9`) {
		t.Errorf("new member missing\n%s", got)
	}
}

// TestSplice_MultiLineBlockCommentTrailer: a `/* … */` opening on a value's line
// and closing on a later one must be treated as one unit. Stopping the trailer
// scan at the first newline returned an offset inside the comment, which either
// swallowed the inserted member or deleted the closing `*/` and with it the rest
// of the document.
func TestSplice_MultiLineBlockCommentTrailer(t *testing.T) {
	t.Run("insert after it", func(t *testing.T) {
		const src = "{\n  \"a\": 1 /* multi\n     line */\n}\n"
		got := spliceTo(t, src, func(s map[string]interface{}) { s["b"] = 2 })
		if !strings.Contains(got, "/* multi") || !strings.Contains(got, "line */") {
			t.Errorf("block comment damaged\n%s", got)
		}
		if !strings.Contains(got, `"b": 2`) {
			t.Errorf("new member missing or swallowed by the comment\n%s", got)
		}
	})

	t.Run("remove past it", func(t *testing.T) {
		const src = "{\n  \"a\": 1, /* multi\n     line */\n  \"b\": 2\n}\n"
		got := spliceTo(t, src, func(s map[string]interface{}) { delete(s, "b") })
		if !strings.Contains(got, "/* multi") || !strings.Contains(got, "line */") {
			t.Errorf("block comment damaged\n%s", got)
		}
		if strings.Contains(got, `"b"`) {
			t.Errorf("member not removed\n%s", got)
		}
	})
}

// --- property test ---

// TestSplice_PropertyRandomMutations is the test that earns its keep. Seven
// hand-written round-trip cases all passed while the splicer was writing `,,`
// into 11% of documents, because every one of them removed the *tail* of a
// container — the single position where the arithmetic happened to be correct.
// A generator does not share the author's blind spot.
//
// The property: for a random JSONC document and a random mutation of it, the
// splice must succeed (not fall back), must decode to exactly the wanted
// document, and must still contain every comment it started with. Falling back
// is safe but lossy, so "no fallbacks" is the assertion that keeps the codec
// honest — a wrong edit trips spliceSettings' own verification and shows up
// here as a fallback rather than as silent corruption.
//
// The seed is fixed so a failure is reproducible; the failure message prints
// the document and the mutation needed to reproduce it by hand.
func TestSplice_PropertyRandomMutations(t *testing.T) {
	rng := rand.New(rand.NewSource(20260809))
	const iterations = 2000

	for i := 0; i < iterations; i++ {
		doc := randomDocument(rng)
		original := renderJSONC(rng, doc)

		want, err := decodeBytes(blankComments([]byte(original)))
		if err != nil {
			t.Fatalf("iteration %d: generated document does not parse: %v\n%s", i, err, original)
		}
		wantMap, ok := want.(map[string]interface{})
		if !ok {
			t.Fatalf("iteration %d: generated document is not an object\n%s", i, original)
		}
		// More than one mutation per document on purpose: a single edit can never
		// produce a removal run longer than one item, and "delete several
		// adjacent members at once" is exactly what uninstalling does — the
		// claudecode adapter removes seven hook events in one pass.
		mutation, keepsComments := "", true
		for m, n := 0, 1+rng.Intn(3); m < n; m++ {
			desc, keeps := mutateDocument(rng, wantMap)
			mutation += desc + "; "
			keepsComments = keepsComments && keeps
		}

		out, err := spliceSettings([]byte(original), wantMap)
		if err != nil {
			t.Fatalf("iteration %d: splice fell back (%v) after %s\n--- original ---\n%s", i, err, mutation, original)
		}

		got, err := decodeBytes(blankComments(out))
		if err != nil {
			t.Fatalf("iteration %d: output does not parse: %v after %s\n--- original ---\n%s\n--- got ---\n%s", i, err, mutation, original, out)
		}
		// Compare against the normalized form: values the mutation introduced are
		// plain Go types, while everything decoded from a file is a json.Number,
		// and that difference is a property of the harness, not of the splice.
		normalizedWant, err := normalize(wantMap)
		if err != nil {
			t.Fatalf("iteration %d: normalize: %v", i, err)
		}
		if !reflect.DeepEqual(got, normalizedWant) {
			t.Fatalf("iteration %d: output is not the wanted document after %s\n--- original ---\n%s\n--- got ---\n%s", i, mutation, original, out)
		}
		// A mutation that deletes or replaces a whole subtree legitimately takes
		// that subtree's comments with it, so comment survival is only asserted
		// for the mutations that cannot destroy one.
		if keepsComments {
			if before, after := strings.Count(original, commentMark), strings.Count(string(out), commentMark); before != after {
				t.Fatalf("iteration %d: %d of %d comments lost after %s\n--- original ---\n%s\n--- got ---\n%s",
					i, before-after, before, mutation, original, out)
			}
		}
	}
}

// commentMark appears in every generated comment, so comment survival is a
// count rather than a parse.
const commentMark = "zc"

// randomDocument builds a small object tree: scalars of every JSON type, nested
// objects and arrays, with enough keys that middle positions come up often.
func randomDocument(rng *rand.Rand) map[string]interface{} {
	return randomObject(rng, 0)
}

func randomObject(rng *rand.Rand, depth int) map[string]interface{} {
	out := map[string]interface{}{}
	for i, n := 0, 1+rng.Intn(4); i < n; i++ {
		out[randomKey(rng, i)] = randomValue(rng, depth)
	}
	return out
}

func randomKey(rng *rand.Rand, i int) string {
	return string(rune('a'+rng.Intn(6))) + string(rune('0'+i))
}

func randomValue(rng *rand.Rand, depth int) interface{} {
	kinds := 5
	if depth >= 2 {
		kinds = 3 // stop nesting
	}
	switch rng.Intn(kinds) {
	case 0:
		return float64(rng.Intn(100))
	case 1:
		// Strings deliberately carry the characters the old writer escaped and
		// the sequences the blanker must not mistake for comments.
		return []string{"a && b", "x >/dev/null 2>&1", "http://e.com/p", "/* no */", "// no", "plain"}[rng.Intn(6)]
	case 2:
		return rng.Intn(2) == 0
	case 3:
		return randomObject(rng, depth+1)
	default:
		var arr []interface{}
		for i, n := 0, rng.Intn(4); i < n; i++ {
			arr = append(arr, randomValue(rng, depth+1))
		}
		return arr
	}
}

// renderJSONC marshals doc and then sprinkles comments through it — full-line,
// trailing, and multi-line block — which is what makes the offset arithmetic
// and the trailer scan interesting.
func renderJSONC(rng *rand.Rand, doc map[string]interface{}) string {
	plain, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	var out strings.Builder
	for _, line := range strings.Split(string(plain), "\n") {
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		switch rng.Intn(6) {
		case 0:
			out.WriteString(indent + "// " + commentMark + " leading\n")
		case 1:
			out.WriteString(indent + "/* " + commentMark + " block\n" + indent + "   continues */\n")
		case 2:
			out.WriteString("\n") // a blank line the splice must also preserve
		}
		out.WriteString(line)
		// A trailing comment is only safe on a line that ends a token; every
		// MarshalIndent line does, so the only rule is not to append twice.
		if rng.Intn(4) == 0 {
			out.WriteString(" // " + commentMark + " trailing")
		} else if rng.Intn(8) == 0 {
			out.WriteString(" /* " + commentMark + " multi\n   line */")
		}
		out.WriteString("\n")
	}
	return out.String()
}

// mutateDocument applies one random edit in place. It returns a description, so
// a failure names the operation that produced it, and whether that operation is
// one after which every comment in the document must still be present.
func mutateDocument(rng *rand.Rand, doc map[string]interface{}) (string, bool) {
	// Arrays get their own mutations rather than only ever being replaced
	// wholesale as an object member. Arrays are what the hook machinery actually
	// edits — `hooks[event]` is an array of matcher groups, appended to on
	// install and filtered on uninstall — so a generator that only mutates
	// object members would inherit the very blind spot this test exists to
	// escape.
	if sites := arraySites(doc, "$"); len(sites) > 0 && rng.Intn(3) == 0 {
		return mutateArray(rng, sites[rng.Intn(len(sites))])
	}

	obj, path := randomContainer(rng, doc, "$")
	keys := sortedKeys(obj)

	if len(keys) == 0 || rng.Intn(3) == 0 {
		key := "new" + string(rune('a'+rng.Intn(4)))
		obj[key] = randomValue(rng, 2)
		return "add " + path + "." + key, true
	}
	key := keys[rng.Intn(len(keys))]
	_, wasScalar := obj[key].(map[string]interface{})
	_, wasArray := obj[key].([]interface{})
	scalar := !wasScalar && !wasArray

	if rng.Intn(2) == 0 {
		delete(obj, key)
		// Removing a member also removes any comment attached to it, which is
		// the right thing to do and impossible to distinguish here from a
		// comment that should have survived.
		return "delete " + path + "." + key, false
	}
	obj[key] = randomValue(rng, 2)
	return "replace " + path + "." + key, scalar
}

// randomContainer descends into a random nested object so mutations land at
// every depth, not only the top level.
func randomContainer(rng *rand.Rand, obj map[string]interface{}, path string) (map[string]interface{}, string) {
	for _, k := range sortedKeys(obj) {
		child, ok := obj[k].(map[string]interface{})
		if ok && rng.Intn(3) == 0 {
			return randomContainer(rng, child, path+"."+k)
		}
	}
	return obj, path
}

// arraySite is one array reachable from the document, addressed through the
// object that holds it so the mutation can write a new slice back.
type arraySite struct {
	parent map[string]interface{}
	key    string
	path   string
}

func arraySites(obj map[string]interface{}, path string) []arraySite {
	var out []arraySite
	for _, k := range sortedKeys(obj) {
		switch v := obj[k].(type) {
		case []interface{}:
			out = append(out, arraySite{parent: obj, key: k, path: path + "." + k})
			for i, e := range v {
				if child, ok := e.(map[string]interface{}); ok {
					out = append(out, arraySites(child, fmt.Sprintf("%s.%s[%d]", path, k, i))...)
				}
			}
		case map[string]interface{}:
			out = append(out, arraySites(v, path+"."+k)...)
		}
	}
	return out
}

// mutateArray appends to, removes from, or edits one element of an array. The
// remove branch is what produces multi-element removal runs, which is where the
// separator arithmetic is hardest.
func mutateArray(rng *rand.Rand, site arraySite) (string, bool) {
	arr := site.parent[site.key].([]interface{})

	if len(arr) == 0 || rng.Intn(3) == 0 {
		site.parent[site.key] = append(slices.Clone(arr), randomValue(rng, 2))
		return "array-append " + site.path, true
	}
	at := rng.Intn(len(arr))
	if rng.Intn(2) == 0 {
		site.parent[site.key] = slices.Delete(slices.Clone(arr), at, at+1)
		return fmt.Sprintf("array-remove %s[%d]", site.path, at), false
	}
	next := slices.Clone(arr)
	next[at] = randomValue(rng, 2)
	site.parent[site.key] = next
	// Not comment-preserving, even when the old element was a scalar: if the
	// new value happens to equal another element, the alignment reads the change
	// as a removal plus a mid-array insertion, and a mid-array insertion is the
	// documented case where the whole array is rewritten
	// (TestSplice_MidArrayInsertRewritesOnlyThatArray).
	return fmt.Sprintf("array-replace %s[%d]", site.path, at), false
}

func sortedKeys(obj map[string]interface{}) []string {
	return slices.Sorted(maps.Keys(obj))
}

// TestSplice_MidArrayInsertRewritesOnlyThatArray exercises the alignment's
// deliberate safety valve. A new element anywhere but the tail has no separator
// to hang an edit off without guessing, so the array is re-encoded whole —
// costing that array's comments and nothing else. The branch is unreachable
// from this package's own merges (hook groups are appended and removed, never
// spliced into the middle), which is exactly why it needs a test of its own:
// nothing else would ever run it.
func TestSplice_MidArrayInsertRewritesOnlyThatArray(t *testing.T) {
	const src = "{\n  // keep me\n  \"list\": [\n    1, // inner comment\n    3\n  ],\n  \"other\": \"a && b\" // also keep me\n}\n"

	got := spliceTo(t, src, func(s map[string]interface{}) {
		list := s["list"].([]interface{})
		s["list"] = []interface{}{list[0], json.Number("2"), list[1]}
	})

	if !strings.Contains(got, "// keep me") || !strings.Contains(got, "// also keep me") {
		t.Errorf("the fallback reformatted more than the one array\n%s", got)
	}
	if !strings.Contains(got, `"a && b"`) {
		t.Errorf("an untouched value outside the array was re-serialized\n%s", got)
	}
	if strings.Contains(got, "// inner comment") {
		t.Errorf("expected the rewritten array to lose its own comment; if this now passes,\n"+
			"the alignment learned mid-array insertion and this test should assert preservation\n%s", got)
	}
}

// TestSplice_HugeArrayDoesNotAllocateQuadratically pins the bound on the
// alignment table. The lengths feeding it come from the user's settings file,
// and the table costs len(a)·len(b) ints — CodeQL flagged the unbounded make()
// as a high-severity allocation-size overflow (PR #1381). Past the bound the
// array is rewritten rather than aligned, which is correct but lossy for that
// array's comments, so this asserts the document still comes out right.
func TestSplice_HugeArrayDoesNotAllocateQuadratically(t *testing.T) {
	big := make([]interface{}, maxAlignedArray+1)
	for i := range big {
		big[i] = json.Number("0")
	}
	if _, ok := alignArray(big, append(slices.Clone(big), json.Number("1"))); ok {
		t.Fatalf("alignArray accepted %d elements; the quadratic table must be bounded", len(big))
	}

	// End to end: an oversized array still splices to the right document.
	var src strings.Builder
	src.WriteString("{\n  // keep me\n  \"list\": [")
	for i := range big {
		if i > 0 {
			src.WriteString(", ")
		}
		src.WriteString("0")
	}
	src.WriteString("]\n}\n")

	got := spliceTo(t, src.String(), func(s map[string]interface{}) {
		s["list"] = append(s["list"].([]interface{}), json.Number("1"))
	})
	if !strings.Contains(got, "// keep me") {
		t.Errorf("the fallback reformatted more than the oversized array\n%.200s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "]\n}") {
		t.Errorf("unexpected shape after the oversized-array fallback\n%.200s", got)
	}
}
