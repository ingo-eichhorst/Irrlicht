// Package hookyaml implements comment-preserving read-modify-write for ONE
// narrow YAML edit: installing, upgrading and removing a single
// marker-delimited region of hook entries nested under one top-level block
// key. It is hookjson's and hooktoml's sibling for YAML (issue #1722), not a
// general YAML merge engine.
//
// Hermes Agent's shell hooks are declared as a `hooks:` block in the user's
// `~/.hermes/config.yaml` — the same file that carries their model, provider
// credentials-by-reference, terminal sandbox policy and every other setting,
// with the comments the shipped `cli-config.yaml.example` teaches them to
// keep. core/go.mod carries no YAML dependency and none is added here: a
// read-modify-marshal through a generic encoder would reformat that entire
// document to install three lines, which is precisely the failure hookjson
// was extracted to stop (`json.MarshalIndent` over a bare map sorting a
// user's keys and HTML-escaping their `&&`).
//
// # The one invariant the scanner rests on
//
// Everything below follows from a single property of a LOADABLE YAML file
// whose root is a block mapping:
//
//	a non-blank line starting at column 0 is a key, a comment, or a
//	document marker — it can never be the continuation of a scalar.
//
// YAML requires the continuation lines of a multi-line flow scalar, and the
// content lines of a block scalar, to be indented MORE than the node they
// belong to. At the root that node sits at indent 0, so every continuation is
// at indent >= 1. A `hooks:` at column 0 is therefore always the real key and
// never text inside somebody's description string. The same argument one
// level down is what lets the block's own event keys be found by indent.
//
// The scanner asserts that invariant rather than assuming it: a column-0 line
// that is none of those three shapes means the file is not the document this
// package models, and the whole document is REFUSED. Refusing is the only
// safe fallback here, exactly as in hooktoml — there is no lossy-but-correct
// encoder to fall back to, and an install that corrupts a user's config is
// far worse than an install that does not happen. A refusal surfaces as
// #1362's "granted but NOT applied, because …" naming the file and the line.
//
// # What is modelled and what is refused
//
// Modelled: a root block mapping; the target key absent, present-and-empty, or
// present with a block mapping of event keys; our own region present, absent,
// or stale; comments and blank lines anywhere.
//
// Refused, each with the line number: a tab in a line's indentation; a `---`
// or `...` document marker (multi-document files); the target key appearing
// twice at the root; the target key carrying an inline value of any kind (a
// flow mapping `hooks: {}`, a flow sequence, a scalar, an anchor `&x`, an
// alias `*x`, or a block-scalar indicator `|`/`>`); the target block being a
// SEQUENCE rather than a mapping; and an anchor, alias or merge key (`<<:`)
// among the block's own keys. Every one of those is a shape whose correct
// edit is not obvious, and guessing at one is how a config gets corrupted.
//
// # Why a marker-delimited region rather than per-entry merging
//
// A region we own outright makes "is this ours" a substring test and makes
// uninstall a byte-range deletion, which is what keeps the property test
// tractable. The cost is that a user who already declares one of the SAME
// event keys we install cannot be merged with — YAML mappings cannot carry a
// duplicate key, and `yaml.safe_load` resolves a duplicate silently to the
// last one, so writing ours beside theirs would DELETE their hook with no
// error anywhere. That collision is detected and refused by name instead.
//
// The region has TWO placements, and the distinction is what makes uninstall
// exact rather than approximately right:
//
//   - When the target key already exists, the region goes INSIDE it and the
//     key line is the user's. Uninstall removes the region and leaves the key,
//     even when the block is then empty — it was empty before too.
//   - When the key does NOT exist, the region is written at the top level and
//     the key line goes INSIDE it. Uninstall then removes the key along with
//     everything else, because irrlicht is what put it there.
//
// The first draft had one placement and a "delete the key if the block is now
// empty" rule, which deleted a `hooks:` a user had written themselves. The
// property test in property_test.go found that on its second iteration, which
// is the whole argument for having one.
//
// # The two normalizations an install can leave behind
//
// Both are semantically neutral to any YAML reader, both are permanent (an
// uninstall does not undo them), and between them they are the ONLY changes
// outside the region that any operation here makes. They are listed because
// "install then uninstall returns the original bytes" is otherwise the claim,
// and a claim with unstated exceptions is worse than a smaller one.
//
//  1. `<key>: {}` — the spelling an agent's own defaults may carry for
//     "nothing configured" — cannot hold a nested mapping, so installing into
//     it rewrites that line to `<key>:` and puts the region inside. The
//     trailing comment rides along, including its column, and so does the
//     line's own terminator — except at end-of-file with no terminator at
//     all, where one is supplied, because the region goes immediately after.
//  2. A file that does not end in a newline gains one, because the region is
//     appended after it and the alternative is fusing the last key onto the
//     BEGIN marker.
package hookyaml

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// markerPrefix leads both region markers. It is a YAML comment, so a config
// carrying one still loads in any YAML parser, and it is the token Inspect
// matches on to decide whether an install is already present.
const markerPrefix = "# irrlicht-managed"

// BeginMarker and EndMarker delimit the region this package owns inside the
// target block, for a given owner token.
//
// The owner token is in both markers rather than only the first: an
// unterminated region and a region belonging to a different owner must not
// look the same, and the END line is what bounds the byte range a rewrite
// replaces.
func BeginMarker(owner string) string {
	return markerPrefix + " BEGIN (" + owner + ") — do not edit; `irrlichd --uninstall-hooks` removes this block"
}

// EndMarker closes the region BeginMarker opens.
func EndMarker(owner string) string {
	return markerPrefix + " END (" + owner + ")"
}

// Entry is one hook definition installed under one event key. Exactly one
// entry per event is modelled, which is all any adapter here installs.
type Entry struct {
	// Event is the agent's own wire name for the lifecycle event, used
	// verbatim as the mapping key.
	Event string
	// Command is the shell command line the agent will run. Rendered as a
	// YAML double-quoted scalar, so it may contain quotes and backslashes.
	Command string
	// TimeoutSeconds bounds the agent's wait. Omitted when zero.
	TimeoutSeconds int
}

// Config describes one install: which file, which top-level key, who owns the
// region, and what goes in it.
type Config struct {
	// Path is the absolute path of the YAML file to edit.
	Path string
	// BlockKey is the top-level mapping key the region is nested under.
	BlockKey string
	// Owner is the token embedded in the region markers.
	Owner string
	// Entries are the hook definitions to install, in order.
	Entries []Entry
	// Sentinel is a substring that appears in every entry this owner writes
	// and in nothing a user would write — a beacon command's
	// `hook-post <adapter> `, for instance.
	//
	// It exists because the markers are COMMENTS, and an agent that rewrites
	// its own config from a parsed structure deletes every comment in it while
	// keeping the data. Hermes' `save_config` does exactly that
	// (`atomic_yaml_write` over a dict) on `hermes config set`, on the setup
	// wizard, and on a dashboard save. After one of those the entries are
	// still there and still firing, but the region that identifies them is
	// gone — so without this, EnsureInstalled would report the surviving
	// entries as a user's own colliding hooks forever, and Uninstall would
	// find nothing to remove and leave them in the user's config with no way
	// to take them out. Empty disables the recovery.
	Sentinel string
	// WriteFile persists the new bytes. Injected so the caller owns atomicity
	// and permissions; AtomicWriteFile is the default implementation.
	WriteFile func(path string, data []byte) error
}

// UnsafeConstructError reports a document this package refuses to edit,
// naming the file and the 1-based line so #1362's surfacing points the user
// at something they can act on.
type UnsafeConstructError struct {
	Path    string
	Line    int
	Message string
}

func (e *UnsafeConstructError) Error() string {
	return fmt.Sprintf("hookyaml: %s:%d: %s", e.Path, e.Line, e.Message)
}

// CollisionError reports that the target block already declares an event key
// the region would define. Separate from UnsafeConstructError because the
// document is perfectly well-formed — the conflict is semantic, and the fix
// is the user's to make.
type CollisionError struct {
	Path  string
	Line  int
	Event string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("hookyaml: %s:%d: %q is already declared in the %s block by something irrlicht did not write; "+
		"refusing to install, because a YAML mapping cannot hold the key twice and the duplicate would silently replace it",
		e.Path, e.Line, e.Event, e.Event)
}

// EnsureInstalled writes cfg's region into the file, creating the block or the
// file if needed. Reports whether anything changed.
func EnsureInstalled(cfg Config) (bool, error) {
	src, err := readAllowingMissing(cfg.Path)
	if err != nil {
		return false, err
	}
	next, changed, err := install(cfg, src)
	if err != nil || !changed {
		return false, err
	}
	return true, cfg.write(next)
}

// Uninstall removes cfg's region, and the block itself when nothing else is
// left in it. Reports whether anything changed. A file we never wrote to, or
// that no longer exists, is left alone.
func Uninstall(cfg Config) (bool, error) {
	src, err := readAllowingMissing(cfg.Path)
	if err != nil {
		return false, err
	}
	if len(src) == 0 {
		return false, nil
	}
	next, changed, err := uninstall(cfg, src)
	if err != nil || !changed {
		return false, err
	}
	return true, cfg.write(next)
}

// Inspect reports whether a region for this owner is present, and whether it
// is byte-identical to what EnsureInstalled would write now.
//
// A document this package refuses is reported as an error rather than as
// "absent": those two have to stay distinguishable, or a config the installer
// cannot touch would be re-reported forever as a missing install.
func Inspect(cfg Config) (present, canonical bool, err error) {
	src, err := readAllowingMissing(cfg.Path)
	if err != nil {
		return false, false, err
	}
	doc, err := scan(cfg, src)
	if err != nil {
		return false, false, err
	}
	if doc.region == nil {
		// Orphaned entries are PRESENT (they are firing) but not canonical:
		// reporting them Missing would make #1372's verifier repair by
		// re-installing, which is the right action, while reporting them
		// absent would leave `--uninstall-hooks` with nothing to find.
		return len(doc.strays) > 0, false, nil
	}
	if len(doc.strays) > 0 {
		return true, false, nil
	}
	want, err := doc.wantRegion(cfg)
	if err != nil {
		return true, false, err
	}
	return true, bytes.Equal(src[doc.region.start:doc.region.end], want), nil
}

func (c Config) write(data []byte) error {
	if c.WriteFile != nil {
		return c.WriteFile(c.Path, data)
	}
	return AtomicWriteFile(c.Path, data)
}

func readAllowingMissing(path string) ([]byte, error) {
	src, err := os.ReadFile(path) // #nosec G304 -- path is the adapter's own resolved config location
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return src, nil
}

// ---------------------------------------------------------------------------
// install / uninstall over the scanned document
// ---------------------------------------------------------------------------

// install is the three-way decision, one branch per placement. Each branch is
// its own function because the three differ in what they render, where they
// splice and what they may rewrite, and reading them interleaved is how the
// end-of-file case below hid.
func install(cfg Config, src []byte) ([]byte, bool, error) {
	doc, err := scan(cfg, src)
	if err != nil {
		return nil, false, err
	}

	// Recover first: entries of ours the agent's own config rewrite orphaned
	// are removed, and everything below then runs against a document with no
	// trace of a previous install. Doing it as a separate pass with a re-scan,
	// rather than folding the deletions into the edit below, keeps every byte
	// offset in `doc` valid for exactly one operation.
	if len(doc.strays) > 0 {
		src = deleteRanges(src, doc.strays)
		doc, err = scan(cfg, src)
		if err != nil {
			return nil, false, err
		}
		if len(doc.strays) > 0 {
			return nil, false, fmt.Errorf("hookyaml: %s still carries an entry marked %q after removing the ones found; refusing to write rather than loop",
				cfg.Path, cfg.Sentinel)
		}
		out, _, err := installInto(cfg, src, doc)
		// changed is true regardless: the strays came out.
		return out, true, err
	}
	return installInto(cfg, src, doc)
}

// installInto is install with recovery already done.
func installInto(cfg Config, src []byte, doc *document) ([]byte, bool, error) {
	if doc.region != nil {
		out, changed, err := rewriteRegion(cfg, src, doc)
		if !changed && err == nil {
			return src, false, nil
		}
		return out, changed, err
	}
	if err := doc.checkCollisions(cfg); err != nil {
		return nil, false, err
	}
	if doc.blockKeyLine < 0 {
		return createBlock(cfg, src)
	}
	return insertIntoBlock(cfg, src, doc)
}

// rewriteRegion replaces an already-present region, in whichever placement it
// occupies. Reports no change when the bytes already match.
func rewriteRegion(cfg Config, src []byte, doc *document) ([]byte, bool, error) {
	region, err := doc.wantRegion(cfg)
	if err != nil {
		return nil, false, err
	}
	if bytes.Equal(src[doc.region.start:doc.region.end], region) {
		return nil, false, nil
	}
	return splice(src, doc.region.start, doc.region.end, region), true, nil
}

// createBlock appends the owned region — markers, block key and entries — at
// the end of the document. The key goes INSIDE the region because irrlicht is
// what put it there, so uninstall takes it away again.
func createBlock(cfg Config, src []byte) ([]byte, bool, error) {
	region, err := renderOwnedRegion(cfg)
	if err != nil {
		return nil, false, err
	}
	var out bytes.Buffer
	out.Write(src)
	if len(src) > 0 && !bytes.HasSuffix(src, []byte("\n")) {
		out.WriteString("\n")
	}
	out.Write(region)
	return out.Bytes(), true, nil
}

// insertIntoBlock puts the region inside a block the USER owns, directly after
// the key line.
func insertIntoBlock(cfg Config, src []byte, doc *document) ([]byte, bool, error) {
	region, err := renderRegion(cfg, doc.blockIndent())
	if err != nil {
		return nil, false, err
	}
	line := doc.lines[doc.blockKeyLine]

	if doc.blockInlineEmpty {
		// `<key>: {}` becomes `<key>:` followed by the region — the one line
		// outside the region any operation here rewrites. See the package doc.
		replacement := []byte(cfg.BlockKey + ":" + doc.blockKeyComment + lineEndingOr(line.ending))
		out := splice(src, line.start, line.end, replacement)
		at := line.start + len(replacement)
		return splice(out, at, at, region), true, nil
	}

	// The key line has to be TERMINATED before the region goes after it. When
	// the block key is the file's last line and the file has no final newline,
	// splicing the region straight in fuses the BEGIN marker onto the key line
	// as a trailing comment — which still parses, so nothing complains, but
	// the marker is no longer a line and uninstall can never find the region
	// again. The property test's "block is the last top-level key" axis found
	// this the first run it produced that position.
	if line.ending == "" {
		region = append([]byte("\n"), region...)
	}
	return splice(src, line.end, line.end, region), true, nil
}

// wantRegion renders the bytes an already-present region should hold, in
// whichever of the two placements it currently occupies. A region does not
// migrate between placements: which one it is in records who owns the block
// key, and rewriting that would either orphan a user's key or leave ours
// behind.
func (d *document) wantRegion(cfg Config) ([]byte, error) {
	if d.regionOwnsBlock {
		return renderOwnedRegion(cfg)
	}
	return renderRegion(cfg, d.blockIndent())
}

func uninstall(cfg Config, src []byte) ([]byte, bool, error) {
	doc, err := scan(cfg, src)
	if err != nil {
		return nil, false, err
	}
	// Both the region and any orphaned entries come out. The block key is
	// inside the region exactly when this install created it, so there is no
	// "should the key go too?" question left to get wrong.
	ranges := append([]byteRange(nil), doc.strays...)
	if doc.region != nil {
		ranges = append(ranges, *doc.region)
	}
	if len(ranges) == 0 {
		return nil, false, nil
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	return deleteRanges(src, ranges), true, nil
}

// lineEndingOr returns the line's own terminator, or "\n" for a final line
// that had none — a rewritten key line must always be terminated, because the
// region goes immediately after it.
func lineEndingOr(ending string) string {
	if ending == "" {
		return "\n"
	}
	return ending
}

func splice(src []byte, start, end int, insert []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(insert))
	out = append(out, src[:start]...)
	out = append(out, insert...)
	return append(out, src[end:]...)
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

// renderRegion produces the exact bytes of the marker-delimited region,
// indented to sit inside a block whose own keys are at blockIndent.
func renderRegion(cfg Config, blockIndent int) ([]byte, error) {
	if len(cfg.Entries) == 0 {
		return nil, errors.New("hookyaml: refusing to render an empty region")
	}
	if strings.TrimSpace(cfg.Owner) == "" {
		return nil, errors.New("hookyaml: refusing to render a region with no owner token")
	}
	pad := strings.Repeat(" ", blockIndent)

	var b strings.Builder
	b.WriteString(pad + BeginMarker(cfg.Owner) + "\n")
	for _, e := range cfg.Entries {
		if !isPlainKey(e.Event) {
			return nil, fmt.Errorf("hookyaml: %q is not a plain YAML key", e.Event)
		}
		if e.Command == "" {
			return nil, fmt.Errorf("hookyaml: %q has an empty command", e.Event)
		}
		b.WriteString(pad + e.Event + ":\n")
		b.WriteString(pad + "  - command: " + Quote(e.Command) + "\n")
		if e.TimeoutSeconds > 0 {
			b.WriteString(pad + "    timeout: " + strconv.Itoa(e.TimeoutSeconds) + "\n")
		}
	}
	b.WriteString(pad + EndMarker(cfg.Owner) + "\n")
	return []byte(b.String()), nil
}

// renderOwnedRegion produces the region for the case where irrlicht creates
// the block: markers at column 0, with the block key line and the entries
// between them, so uninstall takes the key away with everything else.
func renderOwnedRegion(cfg Config) ([]byte, error) {
	if !isPlainKey(cfg.BlockKey) {
		return nil, fmt.Errorf("hookyaml: %q is not a plain YAML key", cfg.BlockKey)
	}
	inner, err := renderRegion(cfg, defaultBlockIndent)
	if err != nil {
		return nil, err
	}
	// renderRegion's own markers are indented to sit inside a block; strip
	// them and re-emit at column 0 around the key. Slicing its output rather
	// than rebuilding the entries keeps ONE renderer for the entry lines.
	body := strings.TrimSuffix(string(inner), strings.Repeat(" ", defaultBlockIndent)+EndMarker(cfg.Owner)+"\n")
	body = strings.TrimPrefix(body, strings.Repeat(" ", defaultBlockIndent)+BeginMarker(cfg.Owner)+"\n")

	var b strings.Builder
	b.WriteString(BeginMarker(cfg.Owner) + "\n")
	b.WriteString(cfg.BlockKey + ":\n")
	b.WriteString(body)
	b.WriteString(EndMarker(cfg.Owner) + "\n")
	return []byte(b.String()), nil
}

// Quote renders s as a YAML double-quoted scalar.
//
// Double-quoted rather than single-quoted or plain, because it is the only
// YAML scalar style with a defined escape for every byte: the command embeds
// an absolute filesystem path the user chose, and a path containing a quote, a
// backslash, a `#` or a leading `-` must not be able to end the scalar or turn
// the line into a comment or a sequence item.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(fmt.Sprintf(`\x%02x`, r))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// isPlainKey reports whether s is an unquoted YAML key made only of
// characters that carry no YAML meaning. `.` is included because hermes'
// own config schema has dotted keys ("no CLI support due to dots in keys",
// hermes_cli/config.py) and refusing them would refuse a real config.
func isPlainKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || c == '-' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

type lineSpan struct {
	start, end int // byte offsets into src; end is past the trailing newline
	text       string
	// ending is the line's own terminator, "\n", "\r\n" or "" on a final
	// line without one. Carried rather than assumed because the ONE line an
	// install rewrites must be re-emitted with the terminator it had: a CRLF
	// document silently gaining one LF line is a diff in every editor and a
	// change this package promises not to make.
	ending  string
	indent  int
	blank   bool
	comment bool
}

type byteRange struct{ start, end int }

type document struct {
	lines []lineSpan

	// blockKeyLine is the index of the `<BlockKey>:` line, or -1.
	blockKeyLine int
	// blockKeys maps each event key declared at the block's own indent to its
	// line index. Empty when the block is absent or empty.
	blockKeys map[string]int
	// blockContentIndent is the indent the block's own keys sit at, or 0 when
	// the block has none yet.
	blockContentIndent int
	// region is the byte range of this owner's marker-delimited region,
	// or nil when absent.
	region *byteRange
	// regionOwnsBlock reports that the region encloses the block key line,
	// i.e. this install created the block. Uninstall then removes both.
	regionOwnsBlock bool
	// strays are entries OUTSIDE the region that carry Config.Sentinel — this
	// owner's own entries, left behind after the agent rewrote its config from
	// a parsed structure and dropped the marker comments. See Config.Sentinel.
	strays []byteRange
	// blockInlineEmpty records `<BlockKey>: {}` — a block that exists and is
	// empty, written in flow style. Installing replaces the key line.
	blockInlineEmpty bool
	// blockKeyComment is the trailing `# …` comment on the block key's own
	// line, preserved across the flow-to-block rewrite above.
	blockKeyComment string
}

// emptyFlowMapping is the only inline value a target key may carry.
const emptyFlowMapping = "{}"

// defaultBlockIndent is the indent used for a block that has no content to
// copy an indent from. Two spaces is what hermes' own shipped example uses.
const defaultBlockIndent = 2

func (d *document) blockIndent() int {
	if d.blockContentIndent > 0 {
		return d.blockContentIndent
	}
	return defaultBlockIndent
}

// checkCollisions refuses an event key the USER declares. A key that is one of
// ours orphaned by the agent's own config rewrite is not a collision — install
// removes it first (see Config.Sentinel), and scan has already emptied
// blockKeys of those.
func (d *document) checkCollisions(cfg Config) error {
	for _, e := range cfg.Entries {
		if line, ok := d.blockKeys[e.Event]; ok {
			return &CollisionError{Path: cfg.Path, Line: line + 1, Event: e.Event}
		}
	}
	return nil
}

func scan(cfg Config, src []byte) (*document, error) {
	doc := &document{blockKeyLine: -1, blockKeys: map[string]int{}}
	doc.lines = splitLines(src)

	if err := doc.findBlockKey(cfg); err != nil {
		return nil, err
	}
	if doc.blockKeyLine < 0 {
		return doc, nil
	}
	if doc.regionOwnsBlock {
		// Everything under the key is inside the region this install wrote, so
		// there are no user keys to collide with and no second region to find.
		return doc, nil
	}
	if err := doc.readBlock(cfg, src); err != nil {
		return nil, err
	}
	return doc, nil
}

func splitLines(src []byte) []lineSpan {
	var out []lineSpan
	for i := 0; i < len(src); {
		j := bytes.IndexByte(src[i:], '\n')
		end := len(src)
		if j >= 0 {
			end = i + j + 1
		}
		raw := string(src[i:end])
		text := strings.TrimRight(raw, "\r\n")
		trimmed := strings.TrimLeft(text, " ")
		out = append(out, lineSpan{
			start:   i,
			end:     end,
			text:    text,
			ending:  raw[len(text):],
			indent:  len(text) - len(trimmed),
			blank:   strings.TrimSpace(text) == "",
			comment: strings.HasPrefix(trimmed, "#"),
		})
		i = end
	}
	return out
}

// findBlockKey locates the top-level BlockKey and, on the way, asserts the
// column-0 invariant this package rests on.
func (d *document) findBlockKey(cfg Config) error {
	w := topLevelWalk{doc: d, cfg: cfg, ownedStart: -1, ownedStartLine: -1}
	for i, l := range d.lines {
		if err := refuseTabIndent(cfg, i, l); err != nil {
			return err
		}
		handled, err := w.marker(i, l)
		if err != nil {
			return err
		}
		if handled || l.blank || l.comment || l.indent > 0 {
			continue
		}
		if err := w.key(i, l); err != nil {
			return err
		}
	}
	return w.finish()
}

// topLevelWalk carries findBlockKey's cross-line state: whether a top-level
// irrlicht region is currently open. Split out because the marker half and the
// key half are two independent grammars over the same line stream, and reading
// them interleaved is what let the region-placement bug hide.
type topLevelWalk struct {
	doc                        *document
	cfg                        Config
	ownedStart, ownedStartLine int
}

// marker handles a column-0 region marker, reporting whether the line was one.
func (w *topLevelWalk) marker(i int, l lineSpan) (bool, error) {
	if l.indent != 0 || !l.comment {
		return false, nil
	}
	switch strings.TrimSpace(l.text) {
	case BeginMarker(w.cfg.Owner):
		if w.ownedStart >= 0 {
			return true, w.refuse(i, "a second irrlicht-managed BEGIN marker before the first was closed")
		}
		w.ownedStart, w.ownedStartLine = l.start, i
		return true, nil
	case EndMarker(w.cfg.Owner):
		if w.ownedStart < 0 {
			return true, w.refuse(i, "an irrlicht-managed END marker with no BEGIN before it")
		}
		if !w.doc.regionOwnsBlock {
			return true, w.refuse(i, fmt.Sprintf(
				"a top-level irrlicht-managed region that does not contain a %q key — irrlicht only writes one at that level to carry the key it created",
				w.cfg.BlockKey))
		}
		w.doc.region = &byteRange{start: w.ownedStart, end: l.end}
		w.ownedStart = -1
		return true, nil
	}
	return false, nil
}

// key handles a column-0 non-comment line: it must be the target key, another
// key, or a shape this package refuses.
func (w *topLevelWalk) key(i int, l lineSpan) error {
	if isDocumentMarker(l.text) {
		return w.refuse(i, "a YAML document marker — this package models a single-document file and will not guess which document to edit")
	}
	key, rest, ok := splitKey(l.text)
	if !ok {
		return w.refuse(i, "a line at column 0 that is not a key, a comment or a document marker — the file is not a plain block mapping and will not be edited")
	}
	if key != w.cfg.BlockKey {
		return nil
	}
	if w.doc.blockKeyLine >= 0 {
		return w.refuse(i, fmt.Sprintf(
			"a second top-level %q key — a duplicate mapping key resolves silently to one of the two, so which one to edit is not knowable",
			w.cfg.BlockKey))
	}
	if err := w.inlineValue(i, rest); err != nil {
		return err
	}
	w.doc.blockKeyLine = i
	w.doc.regionOwnsBlock = w.ownedStart >= 0
	return nil
}

// inlineValue classifies whatever follows the target key on its own line.
func (w *topLevelWalk) inlineValue(i int, rest string) error {
	switch v := strings.TrimSpace(stripComment(rest)); v {
	case "":
		return nil
	case emptyFlowMapping:
		// The one inline value that is modelled, because it is the spelling
		// the agent's OWN default config carries for "no hooks configured".
		// Installing rewrites the line to a block mapping.
		w.doc.blockInlineEmpty = true
		w.doc.blockKeyComment = trailingComment(rest)
		return nil
	default:
		return w.refuse(i, fmt.Sprintf(
			"%q carries the inline value %q; only a block mapping (or the empty %s) is modelled",
			w.cfg.BlockKey, v, emptyFlowMapping))
	}
}

// finish reports a region left open at end of document.
func (w *topLevelWalk) finish() error {
	if w.ownedStart < 0 {
		return nil
	}
	return w.refuse(w.ownedStartLine,
		"a top-level irrlicht-managed BEGIN marker with no matching END — the region's extent is unknown, so rewriting it could delete a user's own configuration")
}

func (w *topLevelWalk) refuse(i int, message string) error {
	return &UnsafeConstructError{Path: w.cfg.Path, Line: i + 1, Message: message}
}

// readBlock walks the lines under the block key, recording its own keys and
// this owner's region.
func (d *document) readBlock(cfg Config, src []byte) error {
	begin, end := BeginMarker(cfg.Owner), EndMarker(cfg.Owner)
	regionStart, regionStartLine := -1, -1
	// openKey/openEnd track the extent of the block-level entry currently
	// being read, so a sentinel-bearing one can be removed whole.
	openKey, openEnd := -1, -1
	openName := ""

	for i := d.blockKeyLine + 1; i < len(d.lines); i++ {
		l := d.lines[i]
		if l.blank || l.comment {
			if trimmed := strings.TrimSpace(l.text); trimmed == begin {
				if regionStart >= 0 {
					return &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
						Message: "a second irrlicht-managed BEGIN marker before the first was closed"}
				}
				regionStart, regionStartLine = l.start, i
			} else if trimmed == end {
				if regionStart < 0 {
					return &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
						Message: "an irrlicht-managed END marker with no BEGIN before it"}
				}
				d.region = &byteRange{start: regionStart, end: l.end}
				regionStart = -1
			}
			continue
		}
		if l.indent == 0 {
			break // the next top-level key ends the block
		}
		if regionStart >= 0 {
			continue // inside our own region; not a user key
		}
		if d.blockContentIndent == 0 {
			d.blockContentIndent = l.indent
		}
		if l.indent != d.blockContentIndent {
			if openKey >= 0 {
				openEnd = l.end // still part of the entry that key opened
			}
			continue // nested deeper than the block's own keys
		}
		d.closeEntry(cfg, src, openKey, openEnd, openName)
		name, err := d.recordBlockKey(cfg, i, l)
		if err != nil {
			return err
		}
		openKey, openEnd, openName = l.start, l.end, name
	}
	d.closeEntry(cfg, src, openKey, openEnd, openName)

	if regionStart >= 0 {
		return &UnsafeConstructError{Path: cfg.Path, Line: regionStartLine + 1,
			Message: "an irrlicht-managed BEGIN marker with no matching END — the region's extent is unknown, so rewriting it could delete a user's own hooks"}
	}
	return nil
}

// closeEntry finishes the block-level entry that started at start, recording
// it as a stray when it carries the owner's sentinel.
func (d *document) closeEntry(cfg Config, src []byte, start, end int, name string) {
	if start < 0 || cfg.Sentinel == "" {
		return
	}
	if !bytes.Contains(src[start:end], []byte(cfg.Sentinel)) {
		return
	}
	d.strays = append(d.strays, byteRange{start: start, end: end})
	// It is ours, so it is not a user key: dropping it here is what stops
	// checkCollisions refusing an install over our own orphaned entry.
	delete(d.blockKeys, name)
}

// deleteRanges removes ranges from src, back to front so earlier offsets stay
// valid. Ranges must not overlap.
func deleteRanges(src []byte, ranges []byteRange) []byte {
	out := src
	for i := len(ranges) - 1; i >= 0; i-- {
		out = splice(out, ranges[i].start, ranges[i].end, nil)
	}
	return out
}

func (d *document) recordBlockKey(cfg Config, i int, l lineSpan) (string, error) {
	body := strings.TrimLeft(l.text, " ")
	if strings.HasPrefix(body, "- ") || body == "-" {
		return "", &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
			Message: fmt.Sprintf("the %s block is a sequence, not a mapping of event names", cfg.BlockKey)}
	}
	if strings.HasPrefix(body, "<<:") {
		return "", &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
			Message: "a YAML merge key inside the block — what the merged mapping contributes is not visible from this file"}
	}
	key, rest, ok := splitKey(body)
	if !ok {
		return "", &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
			Message: "a line inside the block that is not a key — the block is not the plain mapping of event names this package models"}
	}
	if v := strings.TrimSpace(stripComment(rest)); v != "" {
		switch v[0] {
		case '&', '*':
			return "", &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
				Message: fmt.Sprintf("event %q uses a YAML anchor or alias; the entries it resolves to are not visible from this file", key)}
		case '|', '>':
			return "", &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
				Message: fmt.Sprintf("event %q carries a block scalar; the indented lines below it are text, not entries", key)}
		}
	}
	d.blockKeys[key] = i
	return key, nil
}

func refuseTabIndent(cfg Config, i int, l lineSpan) error {
	if strings.HasPrefix(strings.TrimLeft(l.text, " "), "\t") {
		return &UnsafeConstructError{Path: cfg.Path, Line: i + 1,
			Message: "a tab in the line's indentation — YAML forbids it, so this file does not load and will not be edited"}
	}
	return nil
}

func isDocumentMarker(text string) bool {
	t := strings.TrimRight(text, " ")
	return t == "---" || t == "..." || strings.HasPrefix(t, "--- ") || strings.HasPrefix(t, "... ")
}

// splitKey splits "key: rest" into its parts.
//
// A QUOTED key is recognized as a key but reported under its quoted spelling,
// which can never equal a plain BlockKey or event name. That is deliberate:
// it terminates a block and is skipped as a non-match, rather than refusing a
// whole config for a construct that is legal and that this package simply has
// no reason to touch. Anything else is reported as "not a key", which routes
// it to the refusal rather than to a silent mis-edit.
func splitKey(text string) (key, rest string, ok bool) {
	if text != "" && (text[0] == '"' || text[0] == '\'') {
		if i := strings.LastIndex(text, ":"); i > 0 {
			return text[:i], text[i+1:], true
		}
		return "", "", false
	}
	i := strings.IndexByte(text, ':')
	if i < 0 {
		return "", "", false
	}
	key = text[:i]
	if !isPlainKey(key) {
		return "", "", false
	}
	rest = text[i+1:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		// `key:value` is a plain scalar in YAML, not a mapping key.
		return "", "", false
	}
	return key, rest, true
}

// trailingComment returns the `# …` comment at the end of a key's inline
// value, including ALL of the whitespace that separates it from the value.
//
// All of it, not just the one space YAML requires, because the comment is the
// user's and its column is usually deliberate — several keys aligned in a
// group lose their alignment if a rewrite normalizes one of them.
func trailingComment(rest string) string {
	i := strings.Index(rest, " #")
	if i < 0 {
		return ""
	}
	for i > 0 && rest[i-1] == ' ' {
		i--
	}
	return rest[i:]
}

// stripComment removes a trailing `#` comment from a key's inline value.
//
// It stops at the first `#` that follows whitespace, which is YAML's own rule
// for a plain scalar. A `#` inside a quoted value is left alone by requiring
// the preceding space, which is enough for the only use here: deciding
// whether a key has an inline value at all.
func stripComment(rest string) string {
	if strings.HasPrefix(strings.TrimLeft(rest, " "), "#") {
		return ""
	}
	if i := strings.Index(rest, " #"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// AtomicWriteFile writes data to path via a temp file plus rename, creating
// the parent directory. The agent may load the file at any moment, so it must
// never see a partial write.
func AtomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".irrlicht-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
