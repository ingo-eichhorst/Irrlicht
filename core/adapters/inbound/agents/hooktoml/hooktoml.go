// Package hooktoml implements comment-preserving read-modify-write for a
// narrow, purpose-built pair of TOML edits: appending/upgrading/removing ONE
// sentinel-tagged [[hooks]] array-of-tables entry, and setting/clearing ONE
// top-level boolean scalar. It is hookjson's sibling for TOML (issue #1718),
// not a general TOML merge engine.
//
// mistral-vibe's hooks.toml holds exactly one hook entry this daemon
// installs (post_agent_turn), and its config.toml's enable_experimental_hooks
// is exactly one boolean gate the entry cannot fire without — so a general
// nested-structure differ (hookjson's shape, built for Claude Code's
// event -> matcher-group -> entry nesting shared by claudecode/codex/
// gemini-cli) would be solving a problem this adapter does not have. Per
// AGENTS.md's rule ("code that emits bytes from a structural diff gets a
// property test"), this package earns the same property-test treatment
// hookjson's splicer does (property_test.go), scoped to the two structural
// axes it actually varies: appending/upgrading/removing a sentinel-tagged
// array-of-tables block, and setting/clearing a sentinel-free top-level
// scalar.
//
// The strategy mirrors hookjson's: never re-serialize what did not change.
// Both operations work as byte-range edits over the ORIGINAL file, computed
// by a purpose-built line-oriented scanner rather than a general TOML
// parser — core/go.mod carries no TOML dependency and none is added here.
// A read-modify-marshal through a generic TOML encoder is exactly the
// "cost of this ticket" #1718 exists to avoid: it would delete the user's
// comments the same way json.MarshalIndent over a bare map did before
// hookjson existed.
//
// A MULTI-LINE ARRAY is modelled (issue #1753); a triple-quoted string, an
// unterminated single-line string and a multi-line inline table are not, and
// make the scanner refuse the WHOLE document rather than guess. The
// multi-line array had to move from the second list to the first because it
// is not exotic at all: mistral-vibe's OWN writer emits them — twelve in the
// config.toml measured when #1753 was filed — so refusing them meant refusing
// every realistic user config, with the hooks permission still reading
// `granted` and no hook ever firing. Modelling one costs a single integer of
// state carried across lines (see scanDocument): a logical line continues
// while its bracket depth is above zero, and NOTHING inside it is classified
// as a header, a comment or a key.
//
// Unlike hookjson, which falls back to a lossy whole-document re-encode when
// a splice is not safe, there is no safe fallback encoder here: a generic
// map[string]interface{}-style TOML dump is precisely the read-modify-marshal
// this package exists to avoid, so it is never reached for as a fallback.
// "Cannot splice safely" therefore means "do not write," not "write something
// worse" — refusing an install into an exotic hand-crafted hooks.toml is the
// safe direction; a corrupted one is not. What #1753 changed is WHICH
// documents land in that bucket, not what happens to the ones that do — and
// for those, the refusal now names the file and the line (UnsafeConstructError)
// so the "granted but NOT applied, because …" the wizards already render
// (#1362) points at something the user can act on.
package hooktoml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrUnsafe is the sentinel every refusal in this package wraps: the document
// contains a construct the line-oriented scanner does not model with
// confidence — a triple-quoted string, an unterminated single-line string, or
// a multi-line inline table. The caller must not write; there is no lossy
// fallback (see the package doc).
//
// Callers test it with errors.Is. The value a caller actually receives is an
// *UnsafeConstructError, which names the file, the line and the construct.
var ErrUnsafe = errors.New("hooktoml: file contains a construct this splicer does not model safely")

// UnsafeConstructError is the error every refusal in this package returns —
// ErrUnsafe with the three facts a user needs to act on it.
//
// It exists because of the way this class of failure reaches a user. The
// consent effect's error is recorded verbatim by
// services.PermissionService.runClosureEffect and rendered by both wizards as
// "granted but NOT applied, because <reason>" (#1362), and re-answering the
// permission re-runs the effect — so the reason string IS the instruction,
// and the gesture that reads it is the gesture that retries it (#1365
// established the shape for a version floor). A reason naming neither the
// file nor the line, as this package's original single sentinel did, tells a
// user their vibe hooks are dead and nothing else.
type UnsafeConstructError struct {
	// Path is the file that could not be scanned, "" for a document handed
	// to this package in memory.
	Path string
	// Line is the 1-based line the refusal was decided on, 0 when the
	// construct is document-wide.
	Line int
	// Construct names what was found, in words a user can search their own
	// file for.
	Construct string
}

func (e *UnsafeConstructError) Error() string {
	where := e.Path
	if where == "" {
		where = "the file"
	}
	if e.Line > 0 {
		where = fmt.Sprintf("%s:%d", where, e.Line)
	}
	return fmt.Sprintf("hooktoml: %s contains %s, which this splicer does not model safely — "+
		"refusing rather than guessing; simplify that line (or make the edit by hand) and grant again",
		where, e.Construct)
}

func (e *UnsafeConstructError) Unwrap() error { return ErrUnsafe }

func unsafeAt(path string, line int, construct string) error {
	return &UnsafeConstructError{Path: path, Line: line, Construct: construct}
}

// --- line-level scanning primitives ---

// lineScan is what one physical line tells the scanner. brackets and braces
// are net counts OUTSIDE strings and comments; unterminated means a quote was
// still open when the line ended, which in TOML can only be a multi-line
// string this package does not model.
type lineScan struct {
	stripped     []byte
	unterminated bool
	brackets     int
	braces       int
}

// scanLine scans one line (no embedded '\n'), respecting single-line basic
// ("...") and literal ('...') strings, and reports the comment-stripped
// content plus the three facts scanDocument carries across lines.
//
// Bracket and brace counts are reported SEPARATELY and are not symmetric in
// how callers treat them: a bracket run that stays open is a multi-line
// array, which #1753 taught this package to model, while an open brace is a
// multi-line inline table, which it does not — TOML 1.0 does not permit one,
// so a document containing it is malformed or written to a dialect this
// scanner has never seen, and either way is the last place to start guessing.
func scanLine(line []byte) lineScan {
	inBasic, inLiteral := false, false
	var sc lineScan
	commentAt := -1
loop:
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inBasic:
			if c == '\\' {
				i++ // skip the escaped character too
				continue
			}
			if c == '"' {
				inBasic = false
			}
		case inLiteral:
			if c == '\'' {
				inLiteral = false
			}
		default:
			switch c {
			case '"':
				inBasic = true
			case '\'':
				inLiteral = true
			case '#':
				commentAt = i
				break loop
			case '[':
				sc.brackets++
			case ']':
				sc.brackets--
			case '{':
				sc.braces++
			case '}':
				sc.braces--
			}
		}
	}
	if commentAt >= 0 {
		sc.stripped = line[:commentAt]
	} else {
		sc.stripped = line
	}
	sc.unterminated = inBasic || inLiteral
	return sc
}

// stripComment returns line's comment-stripped content — scanLine's first
// result, for the two readers (scalarValue, FieldValue) that want the text of
// a fragment and nothing else.
func stripComment(line []byte) []byte { return scanLine(line).stripped }

func isArrayHeaderLine(t []byte) bool {
	return len(t) >= 4 && bytes.HasPrefix(t, []byte("[[")) && bytes.HasSuffix(t, []byte("]]"))
}

func isTableHeaderLine(t []byte) bool {
	return len(t) >= 2 && bytes.HasPrefix(t, []byte("[")) && !bytes.HasPrefix(t, []byte("[[")) &&
		bytes.HasSuffix(t, []byte("]")) && !bytes.HasSuffix(t, []byte("]]"))
}

// --- section scanning (used by the array-of-tables operations) ---

// section is one top-level element of a TOML document: the preamble (kind 0,
// everything before the first header), a [table] header block (kind 1), or
// an [[array-of-tables]] header block (kind 2). start is the byte offset of
// the header's own line (0 for the preamble); end is the offset of the next
// header's line, or len(src) — so every byte of the document belongs to
// exactly one section, header line included, up to (not including) the next
// header.
type section struct {
	kind       byte
	name       string
	start, end int
}

// scannedLine is one line of a pre-classified document — the first pass
// scanSections runs before it decides section boundaries.
type scannedLine struct {
	start         int
	isHeader      bool
	headerKind    byte
	headerName    string
	isCommentOnly bool // the whole line (leading whitespace aside) is a '#' comment
	// continuation is true when this line is INSIDE a multi-line array
	// opened on an earlier line. Such a line is deliberately classified as
	// nothing at all: a `[[x]]`-shaped or `#`-shaped element of an array is
	// an element, not a header and not a standalone comment, and treating it
	// as either is how a scanner that merely stopped refusing multi-line
	// arrays would start CORRUPTING them.
	continuation bool
}

// tripleQuoteAt reports the byte offset of the first triple-quote in src, or
// -1. Checked document-wide because scanLine's single-line model cannot see
// one at all: three isolated double quotes in a row look, one line at a time,
// like an empty string followed by an opening quote.
func tripleQuoteAt(src []byte) int {
	basic := bytes.Index(src, []byte(`"""`))
	literal := bytes.Index(src, []byte(`'''`))
	switch {
	case basic < 0:
		return literal
	case literal < 0:
		return basic
	case basic < literal:
		return basic
	default:
		return literal
	}
}

// lineNumberAt returns the 1-based line number of byte offset off in src.
func lineNumberAt(src []byte, off int) int {
	if off < 0 || off > len(src) {
		return 0
	}
	return 1 + bytes.Count(src[:off], []byte("\n"))
}

// scanDocument splits src into scannedLines, carrying ONE integer of state
// across lines — the open-bracket depth — so a multi-line array reads as a
// single logical line rather than as a refusal (issue #1753). Everything it
// still cannot model makes it refuse the whole document, now naming the file
// and the line: a triple-quoted string, a quote left open at end of line, a
// brace left open at end of line (a multi-line inline table, which TOML 1.0
// does not permit), a stray `]` that drives the depth negative, and an array
// still open at end of file.
//
// path is used only to build that error; it is never opened here.
func scanDocument(path string, src []byte) ([]scannedLine, error) {
	if off := tripleQuoteAt(src); off >= 0 {
		return nil, unsafeAt(path, lineNumberAt(src, off), "a multi-line (triple-quoted) string")
	}
	var lines []scannedLine
	pos, lineNo, depth := 0, 0, 0
	// openedAt is the line the currently-open array started on, so an array
	// that is never closed is reported at its OPENING line rather than at end
	// of file: the opener is the line a user has to go and look at, and in a
	// 392-line config the two are nowhere near each other.
	openedAt := 0
	for pos <= len(src) {
		end := pos
		for end < len(src) && src[end] != '\n' {
			end++
		}
		lineNo++
		raw := src[pos:end]
		sc := scanLine(raw)
		if sc.unterminated {
			return nil, unsafeAt(path, lineNo, "an unterminated quote")
		}
		if sc.braces != 0 {
			return nil, unsafeAt(path, lineNo, "an inline table spanning more than one line")
		}

		li := scannedLine{start: pos, continuation: depth > 0}
		if depth == 0 {
			trimmed := bytes.TrimSpace(sc.stripped)
			switch {
			case isArrayHeaderLine(trimmed):
				li.isHeader, li.headerKind = true, 2
				li.headerName = string(bytes.TrimSpace(trimmed[2 : len(trimmed)-2]))
			case isTableHeaderLine(trimmed):
				li.isHeader, li.headerKind = true, 1
				li.headerName = string(bytes.TrimSpace(trimmed[1 : len(trimmed)-1]))
			default:
				rawTrimmed := bytes.TrimSpace(raw)
				li.isCommentOnly = len(rawTrimmed) > 0 && rawTrimmed[0] == '#'
			}
		}
		lines = append(lines, li)

		if depth == 0 && sc.brackets > 0 {
			openedAt = lineNo
		}
		depth += sc.brackets
		if depth < 0 {
			return nil, unsafeAt(path, lineNo, "an unbalanced ']'")
		}
		if end >= len(src) {
			break
		}
		pos = end + 1
	}
	if depth != 0 {
		return nil, unsafeAt(path, openedAt, "an array that is never closed")
	}
	return lines, nil
}

// scanSections splits src into sections. A header's section START is walked
// backward over any contiguous, unbroken run of comment-only lines
// immediately preceding it — so a standalone comment directly above a
// [[hooks]] or [table] header travels WITH that header's section rather
// than with whatever came before, the same way hookjson's trailerEnd keeps
// a TRAILING comment attached to the value before it, on the other side of
// the value. Without this, uninstalling (or rewriting) a sentinel-bearing
// block whose install had annotated it with an explanatory comment left
// that comment behind as an orphan — caught by
// TestEnsureAndUninstall_PropertyRandomMutations, which is why every
// generated sentinel block in that test carries one.
func scanSections(path string, src []byte) ([]section, error) {
	lines, err := scanDocument(path, src)
	if err != nil {
		return nil, err
	}

	headerTrueStart := make([]int, len(lines))
	for idx, li := range lines {
		if !li.isHeader {
			continue
		}
		start := li.start
		for j := idx - 1; j >= 0 && lines[j].isCommentOnly; j-- {
			start = lines[j].start
		}
		headerTrueStart[idx] = start
	}

	var sections []section
	cur := section{kind: 0, start: 0}
	for idx, li := range lines {
		if li.isHeader {
			cur.end = headerTrueStart[idx]
			sections = append(sections, cur)
			cur = section{kind: li.headerKind, name: li.headerName, start: headerTrueStart[idx]}
		}
	}
	cur.end = len(src)
	sections = append(sections, cur)
	return sections, nil
}

// firstHeaderOffset returns the byte offset of the first table or
// array-of-tables header line in src, or len(src) if there is none —
// the only place a NEW top-level scalar may be inserted, since TOML
// requires every bare key/value pair to precede all table sections.
func firstHeaderOffset(path string, src []byte) (int, error) {
	sections, err := scanSections(path, src)
	if err != nil {
		return 0, err
	}
	if len(sections) > 1 {
		return sections[1].start, nil
	}
	return len(src), nil
}

// --- reading/writing ---

func readOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func spliceReplace(orig []byte, start, end int, replacement []byte) []byte {
	out := make([]byte, 0, len(orig)-(end-start)+len(replacement))
	out = append(out, orig[:start]...)
	out = append(out, replacement...)
	out = append(out, orig[end:]...)
	return out
}

// appendBlock adds block after orig's existing content, separated by
// exactly one blank line (two newlines) — and never rewrites a single byte
// of orig, only adds. That is a deliberate simplification versus hookjson's
// exact separator arithmetic: TOML array-of-tables entries need no comma or
// other separator between them (each [[header]] is self-delimiting), so
// there is no `,,`-shaped defect class to guard against here, and the only
// cost of the simplification is cosmetic — a file that already ended in
// blank lines gains one more rather than being trimmed to exactly one.
func appendBlock(orig, block []byte) []byte {
	if len(orig) == 0 {
		return block
	}
	out := make([]byte, 0, len(orig)+2+len(block))
	out = append(out, orig...)
	if out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, '\n')
	out = append(out, block...)
	return out
}

// Quote renders s as a TOML basic string. TOML basic-string escaping
// matches JSON's for every character this package's own installers ever
// produce (backslash, double quote, control characters) — TOML additionally
// permits a literal tab unescaped, which JSON does not, but escaping one is
// still legal TOML, so encoding a superset of what is required stays safe,
// never wrong. HTML-escaping is turned off for the same reason hookjson's
// encodeValue turns it off: the beacon command this package renders
// contains a bare `>` (`>/dev/null`) and `||`, and an unescaped rendering
// reads as the shell wrote it rather than as >.
func Quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Encode on a string never errors.
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

// --- [[hooks]]-shaped array-of-tables operations ---

// HookConfig describes one adapter's [[hooks]]-array install to this
// package. Every field is required.
type HookConfig struct {
	// Path is the TOML file holding the [[hooks]] array — mistral-vibe's
	// $VIBE_HOME/hooks.toml.
	Path string

	// Sentinel is the substring that identifies an irrlicht-managed
	// [[hooks]] block: any block whose raw text contains it is treated as
	// ours, so a caller needs no field-level parsing to recognize an
	// existing install — mirroring hookjson's identical technique for its
	// own sentinel, folded into the "command" field's own value rather
	// than declared separately.
	Sentinel string

	// Entry renders the canonical [[hooks]] block to install: starting
	// with "[[hooks]]\n", ending with exactly one trailing "\n", every
	// value TOML-quoted via Quote. Called fresh per call so no caller
	// can share or mutate a cached rendering.
	Entry func() []byte

	// IsCanonical reports whether an existing sentinel-bearing block's raw
	// bytes are already the current form. A sentinel-bearing block that is
	// not canonical is rewritten in place by Entry(), never duplicated.
	IsCanonical func(section []byte) bool

	// WriteFile persists the edited file.
	WriteFile func(path string, data []byte) error
}

// findOurBlock returns the sentinel-bearing [[hooks]] section, if any.
func findOurBlock(orig []byte, sections []section, sentinel string) (section, []byte, bool) {
	for _, s := range sections {
		if s.kind != 2 || s.name != "hooks" {
			continue
		}
		body := orig[s.start:s.end]
		if bytes.Contains(body, []byte(sentinel)) {
			return s, body, true
		}
	}
	return section{}, nil, false
}

// EnsureInstalled merges cfg's [[hooks]] block into the file at cfg.Path,
// creating it if absent. Returns whether the file was modified.
func EnsureInstalled(cfg HookConfig) (bool, error) {
	orig, err := readOrEmpty(cfg.Path)
	if err != nil {
		return false, err
	}
	sections, err := scanSections(cfg.Path, orig)
	if err != nil {
		return false, err
	}
	if s, body, found := findOurBlock(orig, sections, cfg.Sentinel); found {
		if cfg.IsCanonical(body) {
			return false, nil
		}
		out := spliceReplace(orig, s.start, s.end, cfg.Entry())
		return true, cfg.WriteFile(cfg.Path, out)
	}
	out := appendBlock(orig, cfg.Entry())
	return true, cfg.WriteFile(cfg.Path, out)
}

// Uninstall removes cfg's sentinel-bearing [[hooks]] block from the file at
// cfg.Path, leaving every other block and every other byte untouched.
// Returns whether the file was modified.
func Uninstall(cfg HookConfig) (bool, error) {
	orig, err := readOrEmpty(cfg.Path)
	if err != nil {
		return false, err
	}
	if len(orig) == 0 {
		return false, nil
	}
	sections, err := scanSections(cfg.Path, orig)
	if err != nil {
		return false, err
	}
	s, _, found := findOurBlock(orig, sections, cfg.Sentinel)
	if !found {
		return false, nil
	}
	out := make([]byte, 0, len(orig)-(s.end-s.start))
	out = append(out, orig[:s.start]...)
	out = append(out, orig[s.end:]...)
	return true, cfg.WriteFile(cfg.Path, out)
}

// Inspect reports, without writing anything, whether cfg's sentinel-bearing
// block is present and — if present — whether it is canonical. The
// building block for an adapter's read-only Verify (issue #1372).
func Inspect(cfg HookConfig) (present, canonical bool, err error) {
	orig, err := readOrEmpty(cfg.Path)
	if err != nil {
		return false, false, err
	}
	sections, err := scanSections(cfg.Path, orig)
	if err != nil {
		return false, false, err
	}
	_, body, found := findOurBlock(orig, sections, cfg.Sentinel)
	if !found {
		return false, false, nil
	}
	return true, cfg.IsCanonical(body), nil
}

// HasAnyHooksBlock reports whether the file at path holds at least one
// [[hooks]] block, ours or not — what an uninstaller consults to decide
// whether it is safe to also clear the experimental-hooks gate: a user's
// OWN hand-written [[hooks]] entries still need that gate held true, so it
// must be cleared only when hooks.toml ends up with none at all.
func HasAnyHooksBlock(path string) (bool, error) {
	orig, err := readOrEmpty(path)
	if err != nil {
		return false, err
	}
	sections, err := scanSections(path, orig)
	if err != nil {
		return false, err
	}
	for _, s := range sections {
		if s.kind == 2 && s.name == "hooks" {
			return true, nil
		}
	}
	return false, nil
}

// HookBlocks reads the file at path and returns the raw bytes of every
// `[[hooks]]` block it contains, in file order — ours and anyone else's
// alike. An absent file reads as zero blocks, not an error: that is the
// ordinary "nothing installed yet" state, the same convention every other
// read-only function in this package follows.
//
// This is the seam contracttesting's #1178 contract (issue #1178, widened
// for TOML in #1734) consumes as HookInstaller.ReadEntries — it exists
// because hooktoml has no "parse to a generic structure" phase a seam could
// otherwise plug a traversal into (see the package doc): the file is never
// decoded into anything but byte ranges, so the READ step itself is what a
// caller outside this package needs, not a traversal over a structure this
// package does not produce. It shares scanSections with every writer in this
// file, so it refuses (ErrUnsafe) on exactly what they refuse on — in
// particular an unterminated single-line string inside a block reads as
// "cannot scan this document" rather than as a block silently missing its
// command field, which is the distinction
// core/internal/contracttesting/hook_endpoint_toml_test.go's
// TestReferenceTOMLReadEntries_RejectsUnterminatedCommand exists to pin for a
// reference installer and this function inherits for free.
func HookBlocks(path string) ([][]byte, error) {
	orig, err := readOrEmpty(path)
	if err != nil {
		return nil, err
	}
	sections, err := scanSections(path, orig)
	if err != nil {
		return nil, err
	}
	var blocks [][]byte
	for _, s := range sections {
		if s.kind == 2 && s.name == "hooks" {
			blocks = append(blocks, orig[s.start:s.end])
		}
	}
	return blocks, nil
}

// FieldValue extracts a `key = "value"` assignment's decoded string value
// from fragment — typically one block HookBlocks returned — scanning line by
// line and respecting the same backslash-escape and comment-stripping rules
// every writer in this package already uses (lineSafety), so it decodes
// exactly what Quote encoded. Returns ("", false) when key is absent from
// fragment or its value is not a plain double-quoted single-line string.
//
// The seam's HookInstaller.EndpointOfRaw is the intended caller: extracting
// a field back out of raw TOML text is the same operation IsCanonical
// already performs by whole-block comparison, applied to one named field
// instead of the whole block.
func FieldValue(fragment []byte, key string) (string, bool) {
	for _, raw := range bytes.Split(fragment, []byte("\n")) {
		stripped := stripComment(raw)
		line := bytes.TrimSpace(stripped)
		k, v, ok := bytes.Cut(line, []byte("="))
		if !ok || string(bytes.TrimSpace(k)) != key {
			continue
		}
		return unquoteBasicString(bytes.TrimSpace(v))
	}
	return "", false
}

// unquoteBasicString decodes a TOML basic ("...") string, ignoring any
// trailing comment already stripped by lineSafety — the same decode
// vibe/config.go's tomlString hand-rolls for a single known key, generalized
// here to any fragment this package is asked to read a field out of.
func unquoteBasicString(v []byte) (string, bool) {
	if len(v) == 0 || v[0] != '"' {
		return "", false
	}
	p, err := strconv.QuotedPrefix(string(v))
	if err != nil {
		return "", false
	}
	s, err := strconv.Unquote(p)
	if err != nil {
		return "", false
	}
	return s, true
}

// --- top-level scalar operations ---

// topLevelKeyLine finds a `key = value` assignment before the first table
// header, returning its LOGICAL line's byte range. The search stops at the
// first [table] or [[array]] header, matching TOML's own rule that top-level
// key/value pairs must precede every table section — so a same-named key
// INSIDE some unrelated table is correctly never matched.
//
// "Logical" is what #1753 changed and it is load-bearing twice over. Skipping
// a preceding multi-line array is what lets this function reach a header at
// all in a config vibe wrote — `applied_migrations = [` sits at the TOP level
// of one, before every header, so the scan used to refuse on line 44 of 392
// and the experimental-hooks gate was never written. And returning the range
// through the value's CLOSING line is what keeps the caller's splice honest
// if the key it is asked about ever holds a multi-line value itself:
// replacing only the opening line would leave the remaining elements behind
// as orphaned syntax.
//
// A triple-quoted string anywhere in the document is refused here exactly as
// scanDocument refuses it, so the two entry points into this file agree on
// which documents are scannable; without it a `"""` inside a table would be
// invisible to this preamble-only scan.
func topLevelKeyLine(path string, src []byte, key string) (start, end int, found bool, err error) {
	if off := tripleQuoteAt(src); off >= 0 {
		return 0, 0, false, unsafeAt(path, lineNumberAt(src, off), "a multi-line (triple-quoted) string")
	}
	pos, lineNo, depth := 0, 0, 0
	logicalStart, matching, openedAt := 0, false, 0
	for pos <= len(src) {
		lineEnd := pos
		for lineEnd < len(src) && src[lineEnd] != '\n' {
			lineEnd++
		}
		lineNo++
		sc := scanLine(src[pos:lineEnd])
		if sc.unterminated {
			return 0, 0, false, unsafeAt(path, lineNo, "an unterminated quote")
		}
		if sc.braces != 0 {
			return 0, 0, false, unsafeAt(path, lineNo, "an inline table spanning more than one line")
		}
		if depth == 0 {
			trimmed := bytes.TrimSpace(sc.stripped)
			if isArrayHeaderLine(trimmed) || isTableHeaderLine(trimmed) {
				return 0, 0, false, nil
			}
			logicalStart = pos
			k, _, ok := bytes.Cut(trimmed, []byte("="))
			matching = ok && string(bytes.TrimSpace(k)) == key
		}
		if depth == 0 && sc.brackets > 0 {
			openedAt = lineNo
		}
		depth += sc.brackets
		if depth < 0 {
			return 0, 0, false, unsafeAt(path, lineNo, "an unbalanced ']'")
		}
		if depth == 0 && matching {
			return logicalStart, lineEnd, true, nil
		}
		if lineEnd >= len(src) {
			break
		}
		pos = lineEnd + 1
	}
	if depth != 0 {
		return 0, 0, false, unsafeAt(path, openedAt, "an array that is never closed")
	}
	return 0, 0, false, nil
}

// scalarValue extracts the (comment-stripped, trimmed) value text of a
// `key = value` line already known to span [start,end).
func scalarValue(src []byte, start, end int) string {
	line := src[start:end]
	_, eq, ok := bytes.Cut(line, []byte("="))
	if !ok {
		return ""
	}
	stripped := stripComment(eq)
	return strings.TrimSpace(string(stripped))
}

// setTopLevelBool sets key's top-level value to want ("true" or "false"),
// appending a fresh assignment before the first table header if key is
// absent. Returns whether it modified anything.
func setTopLevelBool(path, key string, want bool, writeFile func(string, []byte) error) (bool, error) {
	orig, err := readOrEmpty(path)
	if err != nil {
		return false, err
	}
	wantStr := "false"
	if want {
		wantStr = "true"
	}
	start, end, found, err := topLevelKeyLine(path, orig, key)
	if err != nil {
		return false, namingTheEditItWanted(err, key, wantStr)
	}
	if found {
		if scalarValue(orig, start, end) == wantStr {
			return false, nil
		}
		out := spliceReplace(orig, start, end, []byte(key+" = "+wantStr))
		return true, writeFile(path, out)
	}
	if !want {
		// Absent already reads as false (mistral-vibe's own default), so
		// there is nothing to clear.
		return false, nil
	}
	insertAt, err := firstHeaderOffset(path, orig)
	if err != nil {
		return false, namingTheEditItWanted(err, key, wantStr)
	}
	line := key + " = " + wantStr + "\n"
	// A guard appendBlock also needs and gets, for the identical reason:
	// inserting right after content that does not itself end in '\n' would
	// glue our line onto the end of the PREVIOUS one instead of starting a
	// new line — caught by TestSetTopLevelBool_PropertyRandomMutations
	// against a document with no trailing newline and no table to insert
	// before (insertAt == len(orig) in that case, but the same gap exists
	// at any insertion point that lands right after a newline-less line).
	if insertAt > 0 && orig[insertAt-1] != '\n' {
		line = "\n" + line
	}
	out := spliceReplace(orig, insertAt, insertAt, []byte(line))
	return true, writeFile(path, out)
}

// namingTheEditItWanted appends the edit this package was ASKED to make to a
// refusal, and leaves every other error alone.
//
// "Simplify that line and grant again" tells a user where to look but not what
// the daemon was trying to do there — and for the one scalar this package
// writes, that is the whole instruction: a user who knows irrlicht wanted
// `enable_experimental_hooks = true` can make the one-line edit themselves and
// have working hooks without touching the construct at all. The wrap keeps
// errors.Is(ErrUnsafe) and errors.As(*UnsafeConstructError) working, so no
// caller has to learn about it.
func namingTheEditItWanted(err error, key, wantStr string) error {
	if !errors.Is(err, ErrUnsafe) {
		return err
	}
	return fmt.Errorf("%w — the edit it wanted to make is `%s = %s`", err, key, wantStr)
}

// EnsureBoolTrue sets `key = true` at the top level of the file at path,
// creating the file (or appending before its first table header) if
// necessary. Returns whether it modified anything.
func EnsureBoolTrue(path, key string, writeFile func(string, []byte) error) (bool, error) {
	return setTopLevelBool(path, key, true, writeFile)
}

// ClearBoolIfPresent sets an EXISTING top-level key to false, leaving an
// absent key absent (mistral-vibe's own default already reads as false, so
// there is nothing to add). Returns whether it modified anything.
func ClearBoolIfPresent(path, key string, writeFile func(string, []byte) error) (bool, error) {
	return setTopLevelBool(path, key, false, writeFile)
}

// TopLevelBool reports the top-level value of key: true/false if present
// and exactly "true"/"false", false with found=false if absent, and an
// error if the value is present but unparseable as a bare TOML boolean or
// the document is unsafe to scan.
func TopLevelBool(path, key string) (value, found bool, err error) {
	orig, err := readOrEmpty(path)
	if err != nil {
		return false, false, err
	}
	start, end, ok, err := topLevelKeyLine(path, orig, key)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, nil
	}
	switch scalarValue(orig, start, end) {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	default:
		return false, true, nil
	}
}

// AtomicWriteFile writes data to path via a temp file + rename, so a reader
// (or vibe itself, mid-launch) never observes a half-written file. Creates
// the parent directory. Exported so the vibe adapter's Config.WriteFile can
// share one implementation instead of hand-rolling a second copy of the
// same atomic-write shape claudecode/codex/copilot/geminicli each carry.
func AtomicWriteFile(path string, data []byte) error {
	dir := parentDir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".irrlicht-hooktoml-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// parentDir is filepath.Dir without importing path/filepath twice over for
// one call — kept local so this file's only extra import is os.
func parentDir(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return "."
	}
	if i == 0 {
		return "/"
	}
	return path[:i]
}
