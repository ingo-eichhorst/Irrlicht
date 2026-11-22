package hooktoml

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// scalar.go holds the top-level scalar operations, split out of hooktoml.go
// (#2171) so each file keeps one concern: hooktoml.go edits [[hooks]]
// array-of-tables blocks, this file edits a `key = value` line before the
// first table header. Both share the scanning primitives in hooktoml.go.

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
		if err := refusalFor(path, lineNo, sc, depth+sc.brackets); err != nil {
			return 0, 0, false, err
		}
		if depth == 0 {
			trimmed := bytes.TrimSpace(sc.stripped)
			if _, _, isHeader := headerOf(trimmed); isHeader {
				return 0, 0, false, nil
			}
			logicalStart = pos
			k, _, ok := bytes.Cut(trimmed, []byte("="))
			matching = ok && string(bytes.TrimSpace(k)) == key
			if sc.brackets > 0 {
				openedAt = lineNo
			}
		}
		depth += sc.brackets
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

// namingTheEditItWanted appends the edit this package was ASKED to make to a
// refusal, and leaves every other error alone.
//
// "Simplify that line and grant again" tells a user where to look but not what
// the daemon was trying to do there — and for a scalar edit, that is the whole
// instruction: a user who knows irrlicht wanted `enable_experimental_hooks =
// true`, or wanted that line gone, can make the one-line edit themselves
// without touching the construct at all. The wrap keeps errors.Is(ErrUnsafe)
// and errors.As(*UnsafeConstructError) working, so no caller has to learn
// about it.
func namingTheEditItWanted(err error, edit string) error {
	if !errors.Is(err, ErrUnsafe) {
		return err
	}
	return fmt.Errorf("%w — the edit it wanted to make is %s", err, edit)
}

// EnsureBoolTrue sets `key = true` at the top level of the file at path,
// creating the file (or appending before its first table header) if
// necessary. Returns whether it modified anything.
func EnsureBoolTrue(path, key string, writeFile func(string, []byte) error) (bool, error) {
	orig, err := readOrEmpty(path)
	if err != nil {
		return false, err
	}
	edit := "`" + key + " = true`"
	start, end, found, err := topLevelKeyLine(path, orig, key)
	if err != nil {
		return false, namingTheEditItWanted(err, edit)
	}
	if found {
		if scalarValue(orig, start, end) == "true" {
			return false, nil
		}
		out := spliceReplace(orig, start, end, []byte(key+" = true"))
		return true, writeFile(path, out)
	}
	insertAt, err := firstHeaderOffset(path, orig)
	if err != nil {
		return false, namingTheEditItWanted(err, edit)
	}
	line := key + " = true\n"
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

// mayHoldKey is the one short-circuit every key lookup that tolerates an
// unscannable document shares (#2171 review): a document that does not
// contain the key's text anywhere cannot assign it, so it need not be scanned,
// and is not refused even when the scanner would refuse it.
func mayHoldKey(src []byte, key string) bool {
	return bytes.Contains(src, []byte(key))
}

// DeleteTopLevelKeyIfPresent removes key's top-level assignment from the
// file at path: the whole logical line, through the closing line of a
// multi-line value, and its line terminator. Every other byte is left alone,
// and an absent file or key writes nothing. Returns whether it modified
// anything.
//
// It exists for a key the CLI has dropped (#2171): Vibe 3.1.4 warns about
// enable_experimental_hooks at every start whatever its value (measured in
// that ticket's manual check), so setting it false would keep the warning.
//
// Every top-level copy is removed, not only the first. A duplicate key is
// invalid TOML, but the caller's Verify reads "the key is still there" as
// damage, and a repair that left a second copy behind would be re-run on
// every #1372 pass without ever converging.
//
// A document without the key's text is left alone unscanned (mayHoldKey):
// the delete needs no edit there, and reporting a refusal for one would read
// as a failed install.
func DeleteTopLevelKeyIfPresent(path, key string, writeFile func(string, []byte) error) (bool, error) {
	orig, err := readOrEmpty(path)
	if err != nil || !mayHoldKey(orig, key) {
		return false, err
	}
	out := orig
	for {
		start, end, found, err := topLevelKeyLine(path, out, key)
		if err != nil {
			return false, namingTheEditItWanted(err, "deleting the top-level `"+key+"` line")
		}
		if !found {
			break
		}
		if end < len(out) {
			end++ // the line's own '\n'
		}
		out = spliceReplace(out, start, end, nil)
	}
	if len(out) == len(orig) {
		return false, nil
	}
	return true, writeFile(path, out)
}

// HasTopLevelKey reports whether key is assigned at the top level of the file
// at path, at any value, and so whether DeleteTopLevelKeyIfPresent would
// change it: the same mayHoldKey short-circuit, so the two never disagree
// about a document they leave alone. An absent file has no key.
func HasTopLevelKey(path, key string) (bool, error) {
	orig, err := readOrEmpty(path)
	if err != nil || !mayHoldKey(orig, key) {
		return false, err
	}
	_, _, found, err := topLevelKeyLine(path, orig, key)
	return found, err
}

// TopLevelBool reports the top-level value of key: true/false if present and
// exactly "true"/"false"; found=true with value=false for any other value;
// found=false if absent; and an error only when the document is unsafe to
// scan. Unlike HasTopLevelKey it always scans, which is what lets
// TestScannerRefusals_Corpus hold it to the same refusals as HookBlocks.
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
	return scalarValue(orig, start, end) == "true", true, nil
}
