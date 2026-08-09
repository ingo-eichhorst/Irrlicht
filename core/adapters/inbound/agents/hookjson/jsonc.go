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
//   - comments vanish. gemini-cli's ~/.gemini/settings.json is JSONC: the CLI
//     reads it through stripJsonComments and writes it back through the
//     comment-json package precisely to keep them. Today they don't even
//     survive the *read* — encoding/json rejects the file outright, so an
//     install into a commented config fails rather than merging;
//   - key order becomes alphabetical, silently reshuffling a file the user
//     organized deliberately;
//   - `<`, `>` and `&` are HTML-escaped into their six-character \u00xx
//     escapes, so a perfectly ordinary `npm run build && npm run list-tools`
//     comes back with each ampersand replaced by a backslash-u-0026.
//
// The strategy is to never re-serialize what we did not change. Writing works
// on byte ranges of the original file: the wanted document is structurally
// compared against the original, and only the ranges that actually differ are
// replaced. Everything else — every comment, every blank line, the user's key
// order, their indentation, and their unescaped `&&` — is passed through as the
// original bytes, because it literally is the original bytes.
//
// Two pieces make that possible:
//
//   - blankComments overwrites each comment with spaces *in place*. The result
//     is valid JSON that encoding/json can parse, and because blanking
//     preserves length, every byte offset in it still indexes the same byte of
//     the original. That is what lets a parser that has never heard of comments
//     hand back offsets usable against a file full of them.
//   - parseSpans records, for every value in the document, the byte range it
//     occupies. It uses encoding/json's own Decoder token stream rather than a
//     hand-written JSON parser, so the only bespoke lexing in this file is the
//     comment blanker.
//
// Deliberately NOT changed here: what happens on genuinely malformed input.
// A file that is not valid JSON once its comments are blanked still errors, and
// is never overwritten — see ReadSettings. Issue #1362 is making that error
// visible to the user rather than swallowed; it is correct behavior, not a
// defect, and there is a test pinning it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
)

// errUnsupportedShape marks a document whose top level is something we have no
// safe in-place edit for. The caller falls back to a whole-document encode.
var errUnsupportedShape = errors.New("hookjson: unsupported document shape")

// --- comment blanking ---

// blankComments returns a copy of src with every JSONC comment overwritten by
// spaces. Newlines inside block comments are kept so line numbers — and, more
// importantly, byte offsets — are identical to src. The result is what gets
// parsed; the original is what gets copied from.
func blankComments(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

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
			i = blankLineComment(src, out, i) - 1
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = blankBlockComment(src, out, i) - 1
		}
	}
	return out
}

// blankLineComment blanks from i up to (not including) the next newline and
// returns the index it stopped at.
func blankLineComment(src, out []byte, i int) int {
	for i < len(src) && src[i] != '\n' {
		out[i] = ' '
		i++
	}
	return i
}

// blankBlockComment blanks from i through the closing `*/` (or to end of input
// when unterminated, which then fails the parse) and returns the index just
// past it. Newlines survive so offsets and line structure are unchanged.
func blankBlockComment(src, out []byte, i int) int {
	for i < len(src) {
		if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
			out[i], out[i+1] = ' ', ' '
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
	n, err := parseNode(dec, blanked)
	if err != nil {
		return nil, err
	}
	return n, nil
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
	return nil, errUnsupportedShape
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
			return nil, errUnsupportedShape
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

// containerIndents returns the indent for a container's items and for its
// closing bracket. Observed indentation wins over a computed one, so an
// irregularly formatted file keeps its own shape.
func containerIndents(blanked []byte, n *jsonNode, depth int, lay layout) (item string, closing string) {
	closing = strings.Repeat(lay.indent, depth)
	if observed, ok := indentOfLine(blanked, n.start); ok {
		closing = observed
	}
	item = closing + lay.indent
	if len(n.members) > 0 {
		if observed, ok := indentOfLine(blanked, n.members[0].keyStart); ok {
			item = observed
		}
	} else if len(n.elems) > 0 {
		if observed, ok := indentOfLine(blanked, n.elems[0].start); ok {
			item = observed
		}
	}
	return item, closing
}

// --- edits ---

type edit struct {
	start, end int
	text       []byte
}

// applyEdits splices edits into orig. Insertions share a position with the edit
// they follow, so the sort must be stable to keep them in the order emitted.
func applyEdits(orig []byte, edits []edit) []byte {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out bytes.Buffer
	prev := 0
	for _, e := range edits {
		if e.start < prev {
			continue // defensive: overlapping edits, keep the earlier one
		}
		out.Write(orig[prev:e.start])
		out.Write(e.text)
		prev = e.end
	}
	out.Write(orig[prev:])
	return out.Bytes()
}

// --- encoding ---

// encodeValue renders v the way this package writes JSON: two-space-style
// indentation taken from the file, and no HTML escaping, so `&&` stays `&&`.
// prefix is the indentation of the line the value starts on; the first line
// carries none because it follows a `"key": `.
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
// taken when there is no original file to preserve. The two-space indent and
// trailing newline are the shape a hand-edited config expects, and are pinned
// by TestWriteSettings_ShapeAndDelegation.
func encodeDocument(settings map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil { // Encode appends the newline
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeKey(key string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(key); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// renderMembers renders `"k": v` pairs joined by a comma and a newline, each
// continuation line indented by indent.
func renderMembers(keys []string, want map[string]interface{}, indent string, lay layout) ([]byte, error) {
	var buf bytes.Buffer
	for i, k := range keys {
		if i > 0 {
			buf.WriteString("," + lay.newline + indent)
		}
		key, err := encodeKey(k)
		if err != nil {
			return nil, err
		}
		value, err := encodeValue(want[k], indent, lay)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteString(": ")
		buf.Write(value)
	}
	return buf.Bytes(), nil
}

func renderElems(values []interface{}, indent string, lay layout) ([]byte, error) {
	var buf bytes.Buffer
	for i, v := range values {
		if i > 0 {
			buf.WriteString("," + lay.newline + indent)
		}
		value, err := encodeValue(v, indent, lay)
		if err != nil {
			return nil, err
		}
		buf.Write(value)
	}
	return buf.Bytes(), nil
}

// --- separator arithmetic ---
//
// Insertion and removal are written as exact inverses of each other, which is
// what makes install-then-uninstall byte-identical. Appending an item emits a
// comma at the end of the previous item's value and the item itself after any
// trailing same-line comment; removing the tail deletes exactly those two
// ranges. The comma has to go before the comment and the content after it —
// putting the comma after would bury it inside a `//` comment and produce a
// file that no longer parses.

// nextComma returns the offset of the separating comma at or after from, or -1.
// Blanked comments read as spaces, so a comment sitting between a value and its
// comma is skipped like whitespace.
func nextComma(blanked []byte, from int) int {
	for i := from; i < len(blanked); i++ {
		switch blanked[i] {
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

// endOfSameLineTrailer returns the offset just past a comment trailing on the
// same line as from, or from itself when there is none. Comment bytes are the
// ones where the blanked copy differs from the original.
func endOfSameLineTrailer(orig, blanked []byte, from int) int {
	last := from
	for i := from; i < len(orig) && orig[i] != '\n'; i++ {
		if blanked[i] != orig[i] {
			last = i + 1
			continue
		}
		if orig[i] == ' ' || orig[i] == '\t' || orig[i] == '\r' {
			continue
		}
		break
	}
	return last
}

// itemSpan is one member or element of a container, reduced to the two offsets
// the separator arithmetic needs.
type itemSpan struct{ start, end int }

// removeItems emits the edits that delete idx (ascending) from a container.
// Consecutive removals are handled as one run so their spans cannot overlap.
func removeItems(orig, blanked []byte, items []itemSpan, idx []int, edits *[]edit) bool {
	for run := 0; run < len(idx); {
		i := idx[run]
		j := i
		for run+1 < len(idx) && idx[run+1] == j+1 {
			run++
			j = idx[run]
		}
		run++
		if !removeRun(orig, blanked, items, i, j, edits) {
			return false
		}
	}
	return true
}

// removeRun deletes items[i..j], keeping the container's remaining separators
// valid. Which side the comma comes from depends on whether anything survives
// before or after the run.
func removeRun(orig, blanked []byte, items []itemSpan, i, j int, edits *[]edit) bool {
	if i > 0 {
		// Take the comma that precedes the run, and everything after it up to
		// the end of the run — leaving a same-line comment on the previous
		// item's line where the user put it.
		comma := nextComma(blanked, items[i-1].end)
		if comma < 0 {
			return false
		}
		*edits = append(*edits, edit{start: endOfSameLineTrailer(orig, blanked, comma+1), end: items[j].end})
		if j == len(items)-1 {
			*edits = append(*edits, edit{start: comma, end: comma + 1})
		}
		return true
	}
	// Nothing survives before the run, so take the comma that follows it.
	comma := nextComma(blanked, items[j].end)
	if comma < 0 {
		return false
	}
	*edits = append(*edits, edit{start: items[0].start, end: endOfSameLineTrailer(orig, blanked, comma+1)})
	return true
}

// appendItems emits the edits that add body after the last item of a container.
func appendItems(orig, blanked []byte, last itemSpan, body []byte, indent string, lay layout, edits *[]edit) {
	*edits = append(*edits, edit{start: last.end, end: last.end, text: []byte(",")})
	at := endOfSameLineTrailer(orig, blanked, last.end)
	*edits = append(*edits, edit{start: at, end: at, text: []byte(lay.newline + indent + string(body))})
}

// fillEmptyContainer replaces a container's whole interior, used when it had no
// items to hang a separator off. Emptying it again restores the original `{}`.
func fillEmptyContainer(n *jsonNode, body []byte, itemIndent, closeIndent string, lay layout, edits *[]edit) {
	text := ""
	if len(body) > 0 {
		text = lay.newline + itemIndent + string(body) + lay.newline + closeIndent
	}
	*edits = append(*edits, edit{start: n.start + 1, end: n.end - 1, text: []byte(text)})
}

// --- structural diff ---

// spliceSettings rewrites original so that it decodes to want, changing as few
// bytes as it can. Everything it does not have to touch stays byte-for-byte.
func spliceSettings(original []byte, want map[string]interface{}) ([]byte, error) {
	blanked := blankComments(original)
	root, err := parseSpans(blanked)
	if err != nil {
		return nil, err
	}

	normalized, err := normalize(want)
	if err != nil {
		return nil, err
	}

	lay := detectLayout(original, blanked)
	var edits []edit
	if err := diffValue(original, blanked, root, normalized, 0, lay, &edits); err != nil {
		return nil, err
	}
	return applyEdits(original, edits), nil
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

func decodeNode(blanked []byte, n *jsonNode) (interface{}, error) {
	return decodeBytes(blanked[n.start:n.end])
}

// diffValue emits the edits that turn the value at n into want. A subtree that
// already matches produces no edits at all, which is the whole mechanism by
// which comments, ordering and escaping survive: untouched bytes are never
// rewritten, so there is nothing for a serializer to get wrong.
func diffValue(orig, blanked []byte, n *jsonNode, want interface{}, depth int, lay layout, edits *[]edit) error {
	have, err := decodeNode(blanked, n)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(have, want) {
		return nil
	}

	switch n.kind {
	case '{':
		if m, ok := want.(map[string]interface{}); ok {
			return diffObject(orig, blanked, n, m, depth, lay, edits)
		}
	case '[':
		if s, ok := want.([]interface{}); ok {
			return diffArray(orig, blanked, n, s, depth, lay, edits)
		}
	}
	return replaceNode(blanked, n, want, depth, lay, edits)
}

// replaceNode rewrites a value wholesale — a scalar that changed, or a value
// whose type changed out from under the original shape.
func replaceNode(blanked []byte, n *jsonNode, want interface{}, depth int, lay layout, edits *[]edit) error {
	prefix := strings.Repeat(lay.indent, depth)
	if observed, ok := indentOfLine(blanked, n.start); ok {
		prefix = observed
	}
	text, err := encodeValue(want, prefix, lay)
	if err != nil {
		return err
	}
	*edits = append(*edits, edit{start: n.start, end: n.end, text: text})
	return nil
}

func diffObject(orig, blanked []byte, n *jsonNode, want map[string]interface{}, depth int, lay layout, edits *[]edit) error {
	itemIndent, closeIndent := containerIndents(blanked, n, depth, lay)

	kept := make(map[string]bool, len(n.members))
	var removed []int
	for i, m := range n.members {
		value, ok := want[m.key]
		if !ok {
			removed = append(removed, i)
			continue
		}
		kept[m.key] = true
		if err := diffValue(orig, blanked, m.value, value, depth+1, lay, edits); err != nil {
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

	body, err := renderMembers(added, want, itemIndent, lay)
	if err != nil {
		return err
	}

	items := make([]itemSpan, len(n.members))
	for i, m := range n.members {
		items[i] = itemSpan{start: m.keyStart, end: m.value.end}
	}
	return applyContainerEdits(orig, blanked, n, items, removed, len(added), body, itemIndent, closeIndent, lay, edits)
}

func diffArray(orig, blanked []byte, n *jsonNode, want []interface{}, depth int, lay layout, edits *[]edit) error {
	itemIndent, closeIndent := containerIndents(blanked, n, depth, lay)

	have := make([]interface{}, len(n.elems))
	for i, e := range n.elems {
		v, err := decodeNode(blanked, e)
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
		return replaceNode(blanked, n, want, depth, lay, edits)
	}

	for _, p := range plan.paired {
		if err := diffValue(orig, blanked, n.elems[p.have], want[p.want], depth+1, lay, edits); err != nil {
			return err
		}
	}

	body, err := renderElems(plan.appended, itemIndent, lay)
	if err != nil {
		return err
	}

	items := make([]itemSpan, len(n.elems))
	for i, e := range n.elems {
		items[i] = itemSpan{start: e.start, end: e.end}
	}
	return applyContainerEdits(orig, blanked, n, items, plan.removed, len(plan.appended), body, itemIndent, closeIndent, lay, edits)
}

// applyContainerEdits is the shared tail of diffObject and diffArray: given
// which items go away and what text gets appended, emit the edits. The branches
// exist because a container with nothing left in it has no separator to attach
// to, so its interior is rewritten instead.
func applyContainerEdits(orig, blanked []byte, n *jsonNode, items []itemSpan, removed []int, addedCount int, body []byte, itemIndent, closeIndent string, lay layout, edits *[]edit) error {
	emptied := len(removed) == len(items)

	if emptied {
		fillEmptyContainer(n, body, itemIndent, closeIndent, lay, edits)
		return nil
	}
	if len(removed) > 0 {
		if !removeItems(orig, blanked, items, removed, edits) {
			return errUnsupportedShape
		}
	}
	if addedCount > 0 {
		appendItems(orig, blanked, items[len(items)-1], body, itemIndent, lay, edits)
	}
	return nil
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
	matches := longestCommonSubsequence(have, want)

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

	for _, m := range matches {
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
