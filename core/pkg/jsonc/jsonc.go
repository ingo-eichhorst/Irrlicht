// Package jsonc blanks comments out of JSONC so that encoding/json can read
// it, without changing anything else about the bytes.
//
// It exists because two unrelated readers of the same file have to agree on
// what a valid document is. `hookjson` (an adapter) writes hooks into
// ~/.claude/settings.json and, since #1371, tolerates comments there;
// `core/pkg/tailer` reads the same file to recover the operator's default
// model. When only one of them tolerated comments, a user with a commented
// settings.json got hooks installed successfully and an empty model with no
// error anywhere (#1391).
//
// The obvious way to share the code — have `tailer` import `hookjson` — does
// not merely bend the hexagonal layering, it does not compile:
// `core/domain/agent` imports `core/pkg/tailer`, and `hookjson` imports
// `core/domain/agent`, so the edge closes an import cycle. Hence a leaf
// package under core/pkg/ that both sides can depend on.
//
// Scope is deliberately narrow: comments, and nothing else. This is NOT a
// permissive JSON dialect. Trailing commas, single-quoted strings and bare
// keys stay hard errors in whatever decoder runs next, exactly as they were —
// a shared blanker is only safe to adopt if it cannot quietly widen what its
// callers accept. See TestBlank_DoesNotWidenJSON.
package jsonc

// Blank returns a copy of src with every JSONC comment overwritten by spaces.
func Blank(src []byte) []byte {
	blanked, _ := BlankMask(src)
	return blanked
}

// BlankMask also reports which bytes belonged to a comment. Newlines inside a
// block comment are left as newlines in the blanked text — so line structure
// and byte offsets are identical to src — but they are still marked, which is
// how hookjson's separator arithmetic knows a comment spans lines.
//
// Length is preserved on purpose: every byte offset into the blanked text
// still indexes the original, which is what lets a parser that has never heard
// of comments hand back offsets usable against a file full of them.
func BlankMask(src []byte) ([]byte, []bool) {
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
