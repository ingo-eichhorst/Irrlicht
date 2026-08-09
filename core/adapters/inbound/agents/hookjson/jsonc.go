package hookjson

// Lossless read-modify-write for hand-maintained JSON(C) settings files
// (issue #1371).
//
// The problem this file solves is that the merge logic in hookjson.go works on
// a generic map[string]interface{} — which is the right shape for the merge,
// and the wrong shape for a file the user edits by hand. A map has no comments,
// no key order and no formatting, so re-serializing one with
// json.MarshalIndent destroys all three at once:
//
//   - key order becomes alphabetical, silently reshuffling a file the user
//     organized deliberately;
//   - `<`, `>` and `&` are HTML-escaped into their six-character \u00xx
//     escapes, so a perfectly ordinary `npm run build && npm run list-tools`
//     comes back with each ampersand replaced by a backslash-u-0026 — which
//     ~/.claude/settings.json gets today, since the statusline command this
//     package also writes ends in `>/dev/null 2>&1`;
//   - comments do not survive, and in fact did not survive the *read* either:
//     encoding/json rejects them outright, so an install into a commented
//     config failed and merged nothing.
//
// The first two bite the files this package writes today —
// ~/.claude/settings.json and ~/.codex/hooks.json, both strict JSON. The third
// is why the work happened now: it is a prerequisite for installing hooks into
// gemini-cli's ~/.gemini/settings.json (#1355 Phase D), which is JSONC by
// design — the CLI reads it through stripJsonComments and writes it back
// through the comment-json package specifically to keep comments. No caller
// reads that file yet; this package is being made ready for the one that will.
//
// The strategy is to never re-serialize what we did not change. Writing works
// on byte ranges of the original file: the wanted document is structurally
// compared against the original, and only the ranges that actually differ are
// replaced. Everything else — every comment, every blank line, the user's key
// order, their indentation, and their unescaped `&&` — is passed through as the
// original bytes, because it literally is the original bytes.
//
// Three pieces make that possible:
//
//   - blankComments overwrites each comment with spaces *in place*, and records
//     which bytes were comment. The blanked text is valid JSON that
//     encoding/json can parse, and because blanking preserves length, every byte
//     offset in it still indexes the original. That is what lets a parser which
//     has never heard of comments hand back offsets usable against a file full
//     of them.
//   - parseSpans records, for every value in the document, the byte range it
//     occupies. It uses encoding/json's own Decoder token stream rather than a
//     hand-written JSON parser, so the only bespoke lexing here is the blanker.
//   - the splicer diffs wanted-against-original and emits byte edits.
//
// Because that last part is hand-rolled arithmetic over a file the user cares
// about, the result is verified before it is returned: the spliced bytes are
// re-decoded and compared against the document they are supposed to represent,
// and any mismatch falls back to a whole-document encode. That trades the
// user's comments for their data, which is the right way round — an
// unparseable ~/.claude/settings.json breaks the agent and every subsequent
// install, and the error is currently swallowed on the way out (#1362).
//
// Deliberately NOT changed here: what happens on genuinely malformed input.
// A file that is not valid JSON once its comments are blanked still errors, and
// is never overwritten — see ReadSettings. Issue #1362 is making that error
// visible to the user rather than swallowed; it is correct behavior, not a
// defect, and there are tests pinning it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
)

// errNoSafeSplice means this document cannot be edited in place with confidence
// — an unexpected top-level shape, overlapping edits, or a spliced result that
// failed its own verification. The caller re-encodes the whole document
// instead, losing comments but never content.
var errNoSafeSplice = errors.New("hookjson: cannot splice safely")

// --- comment blanking ---

// blankComments returns a copy of src with every JSONC comment overwritten by
// spaces.
func blankComments(src []byte) []byte {
	blanked, _ := blankCommentsMask(src)
	return blanked
}

// blankCommentsMask also reports which bytes belonged to a comment. Newlines
// inside a block comment are left as newlines in the blanked text — so line
// structure and byte offsets are identical to src — but they are still marked,
// which is how the separator arithmetic knows a comment spans lines.
func blankCommentsMask(src []byte) ([]byte, []bool) {
	out := make([]byte, len(src))
	copy(out, src)
	mask := make([]bool, len(src))

	inString := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			switch c {
			case '\\':
				i++ // an escaped byte can never close the string
			case '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = blankLineComment(src, out, mask, i) - 1
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = blankBlockComment(src, out, mask, i) - 1
		}
	}
	return out, mask
}

// blankLineComment blanks from i up to (not including) the next newline and
// returns the index it stopped at.
func blankLineComment(src, out []byte, mask []bool, i int) int {
	for i < len(src) && src[i] != '\n' {
		out[i] = ' '
		mask[i] = true
		i++
	}
	return i
}

// blankBlockComment blanks from i through the closing `*/` (or to end of input
// when unterminated, which then fails the parse) and returns the index just
// past it.
func blankBlockComment(src, out []byte, mask []bool, i int) int {
	for i < len(src) {
		mask[i] = true
		if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
			out[i], out[i+1] = ' ', ' '
			mask[i+1] = true
			return i + 2
		}
		if src[i] != '\n' {
			out[i] = ' '
		}
		i++
	}
	return i
}

// --- span-recording parse ---

// jsonNode is one value in the document plus the byte range it occupies.
// Offsets index the blanked text, which by construction indexes the original.
type jsonNode struct {
	kind    byte // '{', '[', or 's' for any scalar
	start   int
	end     int
	members []jsonMember // kind '{', in document order
	elems   []*jsonNode  // kind '[', in document order
}

type jsonMember struct {
	key      string
	keyStart int // offset of the opening quote of the key
	value    *jsonNode
}

// parseSpans decodes blanked (valid JSON) recording every value's byte range.
func parseSpans(blanked []byte) (*jsonNode, error) {
	dec := json.NewDecoder(bytes.NewReader(blanked))
	dec.UseNumber()
	return parseNode(dec, blanked)
}

// tokenStart returns the offset of the next token, given the offset just past
// the previous one. Only whitespace and structural punctuation can sit between
// two tokens, and no value ever begins with those — so skipping them lands
// exactly on the token. Blanked comments are spaces here, so they skip too.
func tokenStart(src []byte, from int) int {
	for from < len(src) {
		switch src[from] {
		case ' ', '\t', '\r', '\n', ',', ':':
			from++
		default:
			return from
		}
	}
	return from
}

func parseNode(dec *json.Decoder, src []byte) (*jsonNode, error) {
	start := tokenStart(src, int(dec.InputOffset()))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}

	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return &jsonNode{kind: 's', start: start, end: int(dec.InputOffset())}, nil
	}

	switch delim {
	case '{':
		return parseObject(dec, src, start)
	case '[':
		return parseArray(dec, src, start)
	}
	return nil, errNoSafeSplice
}

func parseObject(dec *json.Decoder, src []byte, start int) (*jsonNode, error) {
	n := &jsonNode{kind: '{', start: start}
	for dec.More() {
		keyStart := tokenStart(src, int(dec.InputOffset()))
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errNoSafeSplice
		}
		value, err := parseNode(dec, src)
		if err != nil {
			return nil, err
		}
		n.members = append(n.members, jsonMember{key: key, keyStart: keyStart, value: value})
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, err
	}
	n.end = int(dec.InputOffset())
	return n, nil
}

func parseArray(dec *json.Decoder, src []byte, start int) (*jsonNode, error) {
	n := &jsonNode{kind: '[', start: start}
	for dec.More() {
		elem, err := parseNode(dec, src)
		if err != nil {
			return nil, err
		}
		n.elems = append(n.elems, elem)
	}
	if _, err := dec.Token(); err != nil { // the closing ']'
		return nil, err
	}
	n.end = int(dec.InputOffset())
	return n, nil
}

// --- layout ---

// layout is the formatting of the file we are editing, so inserted text looks
// like the user wrote it rather than like a serializer emitted it.
type layout struct {
	newline string
	indent  string // one indentation unit
}

func detectLayout(orig, blanked []byte) layout {
	lay := layout{newline: "\n", indent: "  "}
	if bytes.Contains(orig, []byte("\r\n")) {
		lay.newline = "\r\n"
	}
	// The first line that is indented and carries content gives the unit: at
	// that point we are exactly one level deep. Comment lines are all-spaces in
	// the blanked text and skip themselves.
	for _, line := range bytes.Split(blanked, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		body := bytes.TrimLeft(line, " \t")
		if len(body) == 0 || len(body) == len(line) {
			continue
		}
		lay.indent = string(line[:len(line)-len(body)])
		break
	}
	return lay
}

func lineStart(src []byte, pos int) int {
	if i := bytes.LastIndexByte(src[:pos], '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// indentOfLine returns the whitespace a value is indented by, and false when
// the value does not start its own line (so there is no indent to copy).
func indentOfLine(src []byte, pos int) (string, bool) {
	ls := lineStart(src, pos)
	i := ls
	for i < pos && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i != pos {
		return "", false
	}
	return string(src[ls:pos]), true
}

// throughLineEnd extends pos past the rest of its line when only whitespace
// remains on it, so removing an item takes its line with it instead of leaving
// a blank one behind. Returns pos unchanged when something else is on the line.
func throughLineEnd(orig []byte, pos int) int {
	i := pos
	for i < len(orig) && (orig[i] == ' ' || orig[i] == '\t' || orig[i] == '\r') {
		i++
	}
	if i < len(orig) && orig[i] == '\n' {
		return i + 1
	}
	return pos
}

// --- edits ---

type edit struct {
	start, end int
	text       []byte
}

// applyEdits splices edits into orig. Insertions share a position with the edit
// they follow, so the sort must be stable to keep them in the order emitted.
// Overlapping edits mean the arithmetic that produced them is wrong; rather
// than silently dropping one and writing a plausible-looking file, it reports
// failure so the caller can fall back to a whole-document encode.
func applyEdits(orig []byte, edits []edit) ([]byte, bool) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out bytes.Buffer
	prev := 0
	for _, e := range edits {
		if e.start < prev {
			return nil, false
		}
		out.Write(orig[prev:e.start])
		out.Write(e.text)
		prev = e.end
	}
	out.Write(orig[prev:])
	return out.Bytes(), true
}

// --- encoding ---

// encodeValue renders v the way this package writes JSON: indentation taken
// from the file, and no HTML escaping, so `&&` stays `&&`. prefix is the
// indentation of the line the value starts on; the first line carries none
// because it follows a `"key": `.
//
// Every byte of JSON this package emits goes through here, so
// SetEscapeHTML(false) — the one flag this whole change exists to set — is
// written in exactly one place. A second encoder configured somewhere else is
// how the escaping comes back.
func encodeValue(v interface{}, prefix string, lay layout) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, lay.indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if lay.newline != "\n" {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte(lay.newline))
	}
	return out, nil
}

// encodeDocument renders a whole settings document from scratch — the path
// taken when there is no original file to preserve, and the fallback when an
// in-place edit cannot be made safely. The two-space indent and trailing
// newline are the shape a hand-edited config expects, and are pinned by
// TestWriteSettings_ShapeAndDelegation.
func encodeDocument(settings map[string]interface{}) ([]byte, error) {
	out, err := encodeValue(settings, "", layout{newline: "\n", indent: "  "})
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// --- the splicer ---

// splicer carries the document being edited: the original bytes that get
// copied, the blanked copy that gets parsed, the comment mask the separator
// arithmetic consults, and the formatting to match when inserting.
type splicer struct {
	orig    []byte
	blanked []byte
	mask    []bool
	lay     layout
}

// spliceSettings rewrites original so that it decodes to want, changing as few
// bytes as it can. Everything it does not have to touch stays byte-for-byte.
// Returns errNoSafeSplice when it cannot do that with confidence.
func spliceSettings(original []byte, want map[string]interface{}) ([]byte, error) {
	blanked, mask := blankCommentsMask(original)
	root, err := parseSpans(blanked)
	if err != nil {
		return nil, err
	}

	normalized, err := normalize(want)
	if err != nil {
		return nil, err
	}

	s := &splicer{orig: original, blanked: blanked, mask: mask, lay: detectLayout(original, blanked)}
	var edits []edit
	if err := s.diffValue(root, normalized, 0, &edits); err != nil {
		return nil, err
	}

	out, ok := applyEdits(original, edits)
	if !ok {
		return nil, errNoSafeSplice
	}
	if !decodesTo(out, normalized) {
		// The edit arithmetic produced something that is not the document we
		// were asked for. Never write it: a corrupted settings.json breaks the
		// agent and every later install, whereas re-encoding merely costs the
		// comments this package exists to protect.
		return nil, errNoSafeSplice
	}
	return out, nil
}

// decodesTo reports whether spliced bytes really represent want.
func decodesTo(spliced []byte, want interface{}) bool {
	got, err := decodeBytes(blankComments(spliced))
	if err != nil {
		return false
	}
	return reflect.DeepEqual(got, want)
}

// normalize round-trips a value through the decoder so it compares like-for-like
// against values decoded out of the file: an int the merge code wrote as 5 and a
// 5 read off disk must not look like a change. UseNumber keeps integer literals
// exact rather than routing them through float64.
func normalize(v interface{}) (interface{}, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeBytes(data)
}

func decodeBytes(data []byte) (interface{}, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out interface{}
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *splicer) decodeNode(n *jsonNode) (interface{}, error) {
	return decodeBytes(s.blanked[n.start:n.end])
}

// --- separator arithmetic ---
//
// Appending an item emits a comma at the end of the last surviving item's value
// and the item itself after any comment trailing that value; removing the tail
// deletes exactly those two ranges, which is what makes install-then-uninstall
// byte-identical. The comma has to go before the comment and the content after
// it — putting the comma after would bury it inside a `//` comment and produce
// a file that no longer parses.

// nextComma returns the offset of the separating comma at or after from, or -1.
// Blanked comments read as whitespace, so a comment sitting between a value and
// its comma is skipped like whitespace.
func (s *splicer) nextComma(from int) int {
	for i := from; i < len(s.blanked); i++ {
		switch s.blanked[i] {
		case ',':
			return i
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return -1
		}
	}
	return -1
}

// trailerEnd returns the offset just past any comments trailing on the same
// line as from, or from itself when there are none.
//
// A block comment is consumed whole even when it spans lines. Stopping at the
// first newline would return an offset *inside* the comment, and splicing there
// either buries the inserted member in the comment or deletes its closing `*/`
// and swallows the rest of the file.
func (s *splicer) trailerEnd(from int) int {
	last := from
	for i := from; i < len(s.orig); {
		if s.mask[i] {
			for i < len(s.orig) && s.mask[i] {
				i++
			}
			last = i
			continue
		}
		if s.orig[i] == ' ' || s.orig[i] == '\t' || s.orig[i] == '\r' {
			i++
			continue
		}
		break // a newline, or real content, ends the trailer
	}
	return last
}

// itemSpan is one member or element of a container, reduced to the two offsets
// the separator arithmetic needs.
type itemSpan struct{ start, end int }

// itemSpans reduces a container's members or elements to those offsets. An
// object's item starts at its key, an array's at the value itself; both end at
// the end of the value.
func (n *jsonNode) itemSpans() []itemSpan {
	if n.kind == '{' {
		items := make([]itemSpan, len(n.members))
		for i, m := range n.members {
			items[i] = itemSpan{start: m.keyStart, end: m.value.end}
		}
		return items
	}
	items := make([]itemSpan, len(n.elems))
	for i, e := range n.elems {
		items[i] = itemSpan{start: e.start, end: e.end}
	}
	return items
}

// removeItems emits the edits that delete idx (ascending) from a container.
// Consecutive removals are handled as one run so their spans cannot overlap.
func (s *splicer) removeItems(items []itemSpan, idx []int, edits *[]edit) bool {
	for run := 0; run < len(idx); {
		i := idx[run]
		j := i
		for run+1 < len(idx) && idx[run+1] == j+1 {
			run++
			j = idx[run]
		}
		run++
		if !s.removeRun(items, i, j, edits) {
			return false
		}
	}
	return true
}

// removeRun deletes items[i..j], leaving exactly one comma between every pair
// of survivors.
//
// Which comma goes with the run depends on what survives around it. When
// nothing survives after the run, the comma *before* it must go or the last
// survivor is left with a trailing comma. When something does survive after the
// run, that same comma is what separates the survivors on either side, so the
// comma *after* the run goes instead — taking both would leave the survivors
// unseparated, and taking neither writes `,,` into the user's file.
func (s *splicer) removeRun(items []itemSpan, i, j int, edits *[]edit) bool {
	if j == len(items)-1 && i > 0 {
		comma := s.nextComma(items[i-1].end)
		if comma < 0 {
			return false
		}
		start := s.trailerEnd(comma + 1)

		// Anything trailing the removed value goes with it. Leaving it behind
		// would splice it onto the line above, and a `/* … */` landing after a
		// `//` on that line is no longer a block comment — its closing `*/`
		// becomes stray text and the file stops parsing.
		end := s.trailerEnd(items[j].end)

		// Start from the end of the line above the run rather than from the
		// previous item's comma, so a standalone comment the user wrote between
		// the two is left where it is instead of being swallowed. In the common
		// case the two positions are the same byte, which is what keeps this the
		// exact inverse of an append.
		//
		// items[i], not items[j]: the run begins at i, and anchoring on the last
		// item of a multi-item run would delete only that one and leave the rest
		// of the run in the file.
		if _, ownLine := indentOfLine(s.blanked, items[i].start); ownLine {
			if ls := lineStart(s.blanked, items[i].start); ls > start {
				start = ls - 1
			}
		}
		*edits = append(*edits, edit{start: start, end: end})
		*edits = append(*edits, edit{start: comma, end: comma + 1})
		return true
	}

	comma := s.nextComma(items[j].end)
	if comma < 0 {
		return false
	}
	start, end := items[i].start, s.trailerEnd(comma+1)
	// When the run owns its lines outright, take the lines rather than the
	// values, so nothing is left behind but correctly indented survivors.
	if indent, ok := indentOfLine(s.blanked, start); ok {
		start -= len(indent)
		end = throughLineEnd(s.orig, end)
	}
	*edits = append(*edits, edit{start: start, end: end})
	return true
}

// appendItems emits the edits that add body after the last surviving item.
func (s *splicer) appendItems(last itemSpan, body []byte, indent string, edits *[]edit) {
	*edits = append(*edits, edit{start: last.end, end: last.end, text: []byte(",")})
	at := s.trailerEnd(last.end)
	*edits = append(*edits, edit{start: at, end: at, text: []byte(s.lay.newline + indent + string(body))})
}

// fillEmptyContainer replaces a container's whole interior, used when it has no
// surviving item to hang a separator off. Emptying it again restores `{}`.
func (s *splicer) fillEmptyContainer(n *jsonNode, body []byte, itemIndent, closeIndent string, edits *[]edit) {
	text := ""
	if len(body) > 0 {
		text = s.lay.newline + itemIndent + string(body) + s.lay.newline + closeIndent
	}
	*edits = append(*edits, edit{start: n.start + 1, end: n.end - 1, text: []byte(text)})
}

// containerIndents returns the indent for a container's items and for its
// closing bracket. Observed indentation wins over a computed one, so an
// irregularly formatted file keeps its own shape.
func (s *splicer) containerIndents(n *jsonNode, depth int) (item string, closing string) {
	closing = strings.Repeat(s.lay.indent, depth)
	if observed, ok := indentOfLine(s.blanked, n.start); ok {
		closing = observed
	}
	item = closing + s.lay.indent
	if len(n.members) > 0 {
		if observed, ok := indentOfLine(s.blanked, n.members[0].keyStart); ok {
			item = observed
		}
	} else if len(n.elems) > 0 {
		if observed, ok := indentOfLine(s.blanked, n.elems[0].start); ok {
			item = observed
		}
	}
	return item, closing
}

// --- structural diff ---

// diffValue emits the edits that turn the value at n into want. A subtree that
// already matches produces no edits at all, which is the whole mechanism by
// which comments, ordering and escaping survive: untouched bytes are never
// rewritten, so there is nothing for a serializer to get wrong.
func (s *splicer) diffValue(n *jsonNode, want interface{}, depth int, edits *[]edit) error {
	have, err := s.decodeNode(n)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(have, want) {
		return nil
	}

	switch n.kind {
	case '{':
		if m, ok := want.(map[string]interface{}); ok {
			return s.diffObject(n, m, depth, edits)
		}
	case '[':
		if a, ok := want.([]interface{}); ok {
			return s.diffArray(n, a, depth, edits)
		}
	}
	return s.replaceNode(n, want, depth, edits)
}

// replaceNode rewrites a value wholesale — a scalar that changed, or a value
// whose type changed out from under the original shape.
func (s *splicer) replaceNode(n *jsonNode, want interface{}, depth int, edits *[]edit) error {
	prefix := strings.Repeat(s.lay.indent, depth)
	if observed, ok := indentOfLine(s.blanked, n.start); ok {
		prefix = observed
	}
	text, err := encodeValue(want, prefix, s.lay)
	if err != nil {
		return err
	}
	*edits = append(*edits, edit{start: n.start, end: n.end, text: text})
	return nil
}

func (s *splicer) diffObject(n *jsonNode, want map[string]interface{}, depth int, edits *[]edit) error {
	itemIndent, closeIndent := s.containerIndents(n, depth)

	kept := make(map[string]bool, len(n.members))
	var removed []int
	for i, m := range n.members {
		value, ok := want[m.key]
		if !ok {
			removed = append(removed, i)
			continue
		}
		kept[m.key] = true
		if err := s.diffValue(m.value, value, depth+1, edits); err != nil {
			return err
		}
	}

	added := make([]string, 0, len(want))
	for k := range want {
		if !kept[k] {
			added = append(added, k)
		}
	}
	sort.Strings(added) // map iteration is random; the file must not be

	var buf bytes.Buffer
	for i, k := range added {
		if i > 0 {
			buf.WriteString("," + s.lay.newline + itemIndent)
		}
		key, err := encodeValue(k, itemIndent, s.lay)
		if err != nil {
			return err
		}
		value, err := encodeValue(want[k], itemIndent, s.lay)
		if err != nil {
			return err
		}
		buf.Write(key)
		buf.WriteString(": ")
		buf.Write(value)
	}

	return s.applyContainerEdits(n, n.itemSpans(), removed, buf.Bytes(), itemIndent, closeIndent, edits)
}

func (s *splicer) diffArray(n *jsonNode, want []interface{}, depth int, edits *[]edit) error {
	itemIndent, closeIndent := s.containerIndents(n, depth)

	have := make([]interface{}, len(n.elems))
	for i, e := range n.elems {
		v, err := s.decodeNode(e)
		if err != nil {
			return err
		}
		have[i] = v
	}

	plan, ok := alignArray(have, want)
	if !ok {
		// An insertion somewhere other than the tail has no separator we can
		// hang an edit off without guessing. Rewriting the array wholesale
		// costs only the comments inside this one array, and never corrupts.
		return s.replaceNode(n, want, depth, edits)
	}

	for _, p := range plan.paired {
		if err := s.diffValue(n.elems[p.have], want[p.want], depth+1, edits); err != nil {
			return err
		}
	}

	var buf bytes.Buffer
	for i, v := range plan.appended {
		if i > 0 {
			buf.WriteString("," + s.lay.newline + itemIndent)
		}
		value, err := encodeValue(v, itemIndent, s.lay)
		if err != nil {
			return err
		}
		buf.Write(value)
	}

	return s.applyContainerEdits(n, n.itemSpans(), plan.removed, buf.Bytes(), itemIndent, closeIndent, edits)
}

// applyContainerEdits is the shared tail of diffObject and diffArray: given
// which items go away and what text gets appended, emit the edits.
func (s *splicer) applyContainerEdits(n *jsonNode, items []itemSpan, removed []int, body []byte, itemIndent, closeIndent string, edits *[]edit) error {
	// removed is always ascending, distinct and in range — diffObject appends
	// while ranging over members, alignArray appends monotonically — so "nothing
	// survives" is a count rather than a search. body is non-empty exactly when
	// something is being added, since every encoded item is at least one byte.
	if len(removed) == len(items) {
		// Nothing survives, so there is no separator to attach to: the
		// container's whole interior is rewritten instead.
		s.fillEmptyContainer(n, body, itemIndent, closeIndent, edits)
		return nil
	}

	// When the tail is being removed and new items are arriving in the same
	// pass, the new items simply take the tail's place. The comma that already
	// separates the last survivor stays exactly where it is — deleting it with
	// the tail and then re-adding it for the append means two edits at one
	// offset, which is an overlap, and anchoring the append on the removed item
	// puts the comma at an offset the deletion splices away.
	if tail := trailingRun(len(items), removed); tail >= 0 && len(body) > 0 {
		comma := s.nextComma(items[tail-1].end)
		if comma < 0 {
			return errNoSafeSplice
		}
		*edits = append(*edits, edit{
			start: s.trailerEnd(comma + 1),
			end:   items[len(items)-1].end,
			text:  []byte(s.lay.newline + itemIndent + string(body)),
		})
		earlier := removed[:len(removed)-(len(items)-tail)]
		if len(earlier) > 0 && !s.removeItems(items, earlier, edits) {
			return errNoSafeSplice
		}
		return nil
	}

	if len(removed) > 0 && !s.removeItems(items, removed, edits) {
		return errNoSafeSplice
	}
	if len(body) > 0 {
		s.appendItems(items[len(items)-1], body, itemIndent, edits)
	}
	return nil
}

// trailingRun returns the first index of the run of removed items that reaches
// the last item, or -1 when the last item survives. Callers reach it only when
// something survives, so the returned index is always >= 1.
func trailingRun(count int, removed []int) int {
	if len(removed) == 0 || removed[len(removed)-1] != count-1 {
		return -1
	}
	start := count - 1
	for k := len(removed) - 2; k >= 0 && removed[k] == start-1; k-- {
		start--
	}
	return start
}

// --- array alignment ---

type arrayPair struct{ have, want int }

type arrayPlan struct {
	paired   []arrayPair   // same position, different content: recurse
	removed  []int         // indices of have that go away
	appended []interface{} // new values, all at the tail
}

// alignArray works out how have became want. It matches equal elements with a
// longest-common-subsequence, pairs up what is left position-wise so an element
// edited in place is recursed into rather than replaced (which would drop any
// comments inside it), and reports the rest as removals plus tail appends.
//
// It returns false when a new element belongs anywhere but the tail. That never
// arises from this package's own merges — hook groups are appended and removed,
// never spliced into the middle — and the caller falls back to rewriting the
// array rather than guessing at a separator.
func alignArray(have, want []interface{}) (arrayPlan, bool) {
	var plan arrayPlan
	ai, bi := 0, 0
	advance := func(ma, mb int) bool {
		da, db := ma-ai, mb-bi
		paired := da
		if db < paired {
			paired = db
		}
		for k := 0; k < paired; k++ {
			plan.paired = append(plan.paired, arrayPair{have: ai + k, want: bi + k})
		}
		for k := paired; k < da; k++ {
			plan.removed = append(plan.removed, ai+k)
		}
		if db > paired && ma != len(have) {
			return false // an insertion that is not at the tail
		}
		for k := paired; k < db; k++ {
			plan.appended = append(plan.appended, want[bi+k])
		}
		return true
	}

	for _, m := range longestCommonSubsequence(have, want) {
		if !advance(m.have, m.want) {
			return arrayPlan{}, false
		}
		ai, bi = m.have+1, m.want+1
	}
	if !advance(len(have), len(want)) {
		return arrayPlan{}, false
	}
	return plan, true
}

// longestCommonSubsequence returns the matched index pairs of the longest run of
// deep-equal elements common to a and b, in order. Arrays here hold a handful of
// hook groups, so the quadratic table is free.
func longestCommonSubsequence(a, b []interface{}) []arrayPair {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if reflect.DeepEqual(a[i], b[j]) {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			table[i][j] = table[i+1][j]
			if table[i][j+1] > table[i][j] {
				table[i][j] = table[i][j+1]
			}
		}
	}

	var out []arrayPair
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case reflect.DeepEqual(a[i], b[j]):
			out = append(out, arrayPair{have: i, want: j})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}
