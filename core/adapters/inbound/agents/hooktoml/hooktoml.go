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
// Anything the scanner cannot confidently classify — a triple-quoted
// string, a multi-line array, an unterminated single-line string — makes it
// refuse the WHOLE document rather than guess. Unlike hookjson, which falls
// back to a lossy whole-document re-encode when a splice is not safe, there
// is no safe fallback encoder here: a generic map[string]interface{}-style
// TOML dump is precisely the read-modify-marshal this package exists to
// avoid, so it is never reached for as a fallback. "Cannot splice safely"
// therefore means "do not write," not "write something worse" — refusing an
// install into an exotic hand-crafted hooks.toml is the safe direction; a
// corrupted one is not.
package hooktoml

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// ErrUnsafe means this document contains a construct the line-oriented
// scanner does not model with confidence — a triple-quoted string, an
// unterminated single-line string, or a multi-line array. The caller must
// not write; there is no lossy fallback (see the package doc).
var ErrUnsafe = errors.New("hooktoml: file contains a construct this splicer does not model safely (multi-line string, multi-line array, or unterminated quote) — refusing rather than guessing")

// --- line-level scanning primitives ---

// lineSafety scans one line (no embedded '\n'), respecting single-line basic
// ("...") and literal ('...') strings, and reports the comment-stripped
// content plus whether the line looks like it continues onto the next one —
// an unterminated quote (the start of some multi-line construct this
// scanner does not model) or an unbalanced '['/']' count outside strings
// (a multi-line array). continues is the signal every caller in this
// package refuses on rather than guesses through.
func lineSafety(line []byte) (stripped []byte, continues bool) {
	inBasic, inLiteral := false, false
	depth := 0
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
				depth++
			case ']':
				depth--
			}
		}
	}
	if commentAt >= 0 {
		stripped = line[:commentAt]
	} else {
		stripped = line
	}
	return stripped, inBasic || inLiteral || depth != 0
}

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
	continues     bool
}

// classifyLines splits src into scannedLines, refusing (ErrUnsafe) rather
// than guessing at any construct lineSafety cannot classify — including a
// document-wide check for triple-quoted strings, which lineSafety's
// single-line model cannot see at all (three isolated double quotes in a
// row look, one line at a time, like an empty string followed by an opening
// quote).
func classifyLines(src []byte) ([]scannedLine, error) {
	if bytes.Contains(src, []byte(`"""`)) || bytes.Contains(src, []byte(`'''`)) {
		return nil, ErrUnsafe
	}
	var lines []scannedLine
	pos := 0
	for pos <= len(src) {
		end := pos
		for end < len(src) && src[end] != '\n' {
			end++
		}
		raw := src[pos:end]
		stripped, continues := lineSafety(raw)
		trimmed := bytes.TrimSpace(stripped)
		li := scannedLine{start: pos, continues: continues}
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
		lines = append(lines, li)
		if end >= len(src) {
			break
		}
		pos = end + 1
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
func scanSections(src []byte) ([]section, error) {
	lines, err := classifyLines(src)
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
		if li.continues {
			return nil, ErrUnsafe
		}
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
func firstHeaderOffset(src []byte) (int, error) {
	sections, err := scanSections(src)
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
	sections, err := scanSections(orig)
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
	sections, err := scanSections(orig)
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
	sections, err := scanSections(orig)
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
	sections, err := scanSections(orig)
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

// --- top-level scalar operations ---

// topLevelKeyLine finds a `key = value` assignment before the first table
// header, returning its line's byte range. The search stops at the first
// [table] or [[array]] header, matching TOML's own rule that top-level
// key/value pairs must precede every table section — so a same-named key
// INSIDE some unrelated table is correctly never matched.
func topLevelKeyLine(src []byte, key string) (start, end int, found bool, err error) {
	pos := 0
	for pos <= len(src) {
		lineEnd := pos
		for lineEnd < len(src) && src[lineEnd] != '\n' {
			lineEnd++
		}
		stripped, continues := lineSafety(src[pos:lineEnd])
		trimmed := bytes.TrimSpace(stripped)
		if isArrayHeaderLine(trimmed) || isTableHeaderLine(trimmed) {
			return 0, 0, false, nil
		}
		if continues {
			return 0, 0, false, ErrUnsafe
		}
		if k, _, ok := bytes.Cut(trimmed, []byte("=")); ok && string(bytes.TrimSpace(k)) == key {
			return pos, lineEnd, true, nil
		}
		if lineEnd >= len(src) {
			break
		}
		pos = lineEnd + 1
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
	stripped, _ := lineSafety(eq)
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
	start, end, found, err := topLevelKeyLine(orig, key)
	if err != nil {
		return false, err
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
	insertAt, err := firstHeaderOffset(orig)
	if err != nil {
		return false, err
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
	start, end, ok, err := topLevelKeyLine(orig, key)
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
