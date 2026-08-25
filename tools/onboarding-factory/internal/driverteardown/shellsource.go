package driverteardown

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Source is one shell file parsed far enough to answer the three questions
// this package asks of a recording driver: where its top-level functions
// begin and end, which `if` conditions enclose a given statement, and what
// the words of each statement are.
//
// It is deliberately NOT a shell parser. It is a lexer plus two structural
// passes, and every place it can be wrong is a place it refuses instead:
// an unterminated function body, an unbalanced `if`/`fi`, a `tmux
// new-session` with no `-s`. AGENTS.md's rule is that a verification
// mechanism must fail loudly when it cannot run, and the cheapest way to
// honour that in a text-matching checker is to make every structural
// assumption an assertion.
type Source struct {
	Path string

	lines []string    // physical lines; lines[n-1] is line n
	stmts []Statement // logical statements, in file order
	funcs []Region    // top-level function bodies, in file order
}

// Statement is one logical shell command: a physical line plus every line
// joined to it by a trailing backslash, split at the shell operators that
// separate commands (`;` `&&` `||` `|` `&`).
type Statement struct {
	Line  int      // 1-based line of the first physical line it came from
	Text  string   // the joined logical line this statement belongs to
	Words []string // shell words; quotes and expansions kept intact
	// Prefix is the code of the commands earlier in the SAME logical line that
	// this one is conditional on — the `[[ … ]]` of `[[ … ]] && tmux
	// kill-session …`. It is comment-free, so a comment NAMING the flag beside
	// an ungated teardown cannot be read as a gate on it.
	Prefix string
	// Conds are the `if`/`elif` conditions enclosing this statement, outermost
	// first. Empty means no `if` encloses it.
	Conds []string
	// Depth is how many COMPOUND statements enclose this one — `if`/`fi`,
	// `case`/`esac`, a `do`/`done` loop body, a `{ … }` group, and a function
	// body. Depth 0 with Func "" is the only position that runs on every path
	// through the script, which is what INV-4's "the epilogue set it" rests on.
	//
	// Conds alone was NOT enough, and the gap was measured rather than
	// imagined: `EXIT_REASON="nonzero(2)"` inside a `case` arm inside the step
	// loop has no enclosing `if`, so a Conds-only rule read it as running
	// unconditionally and passed all ten drivers on a fail-closed reading they
	// had not earned.
	Depth int
	// Func is the name of the top-level function whose body contains this
	// statement, or "" when the statement is at top level.
	Func string
}

// Region is one top-level function body, inclusive of its `name() {` line and
// its closing `}` line.
type Region struct {
	Name       string
	Start, End int
}

var (
	funcOpenRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)[ \t]*\([ \t]*\)[ \t]*\{`)
	heredocRe  = regexp.MustCompile(`<<-?[ \t]*(['"]?)([A-Za-z_][A-Za-z0-9_]*)(['"]?)`)
	assignRe   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(\[[^\]]*\])?=(.*)$`)
	// A word that is exactly one variable expansion: "$X", $X, "${X}",
	// "${X[$i]}", "$1". Anything else is treated as composed text.
	soleVarRe = regexp.MustCompile(`^\$(?:([A-Za-z_][A-Za-z0-9_]*|[0-9]+)|\{([A-Za-z_][A-Za-z0-9_]*|[0-9]+)(?:\[[^\]]*\])?(?::-[^}]*)?\})$`)
)

// Parse lexes and structures one shell file. It returns an error rather than a
// best-effort structure whenever an assumption it rests on does not hold.
func Parse(path, src string) (*Source, error) {
	s := &Source{Path: path, lines: strings.Split(src, "\n")}
	code := codeMask(s.lines)
	if err := s.findFunctions(code); err != nil {
		return nil, err
	}
	if err := s.buildStatements(code); err != nil {
		return nil, err
	}
	return s, nil
}

// codeMask marks which physical lines are code. Heredoc BODIES are not: a
// `}` or an `fi` inside one would otherwise close a structure it has nothing
// to do with. Here-STRINGS (`<<<`) are ordinary code and stay marked.
func codeMask(lines []string) []bool {
	code := make([]bool, len(lines))
	term := ""
	for i, ln := range lines {
		if term != "" {
			code[i] = false
			if strings.TrimSpace(ln) == term {
				term = ""
			}
			continue
		}
		code[i] = true
		body := stripComment(ln)
		if strings.Contains(body, "<<<") {
			continue
		}
		if m := heredocRe.FindStringSubmatch(body); m != nil {
			term = m[2]
		}
	}
	return code
}

// stripComment drops a trailing `#` comment when the `#` starts a word. It is
// approximate on purpose: a `#` inside a quoted string is left alone by the
// word scanner, and the only consumer here is heredoc detection.
func stripComment(ln string) string {
	if i := strings.Index(ln, " #"); i >= 0 {
		return ln[:i]
	}
	if strings.HasPrefix(strings.TrimSpace(ln), "#") {
		return ""
	}
	return ln
}

// findFunctions records every top-level function body. The drivers all write
// `name() {` at column 0 and close with `}` at column 0 — including the shared
// scaffolding they were generated from — so the pairing is "the next `}` at
// column 0". A stray column-0 `}` with no opener before it (a `{ …; } > file`
// group at top level, which aider's epilogue uses) is simply never consulted.
//
// The failure this refuses: an opener with no closer would silently swallow
// the rest of the file into one region and move every later top-level
// statement inside a function, which is exactly the classification INV-1
// rests on.
func (s *Source) findFunctions(code []bool) error {
	for i, ln := range s.lines {
		if !code[i] {
			continue
		}
		m := funcOpenRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		if len(s.funcs) > 0 && s.funcs[len(s.funcs)-1].End >= i+1 {
			continue // a nested `name() {` inside a body we already closed over
		}
		if strings.HasSuffix(strings.TrimRight(ln, " \t"), "}") {
			s.funcs = append(s.funcs, Region{Name: m[1], Start: i + 1, End: i + 1})
			continue
		}
		end := -1
		for j := i + 1; j < len(s.lines); j++ {
			if code[j] && strings.HasPrefix(s.lines[j], "}") {
				end = j + 1
				break
			}
		}
		if end < 0 {
			return fmt.Errorf("%s:%d: function %s() is never closed by a `}` at column 0 — "+
				"this file's structure cannot be read, and a check that cannot run must say so",
				s.Path, i+1, m[1])
		}
		s.funcs = append(s.funcs, Region{Name: m[1], Start: i + 1, End: end})
	}
	return nil
}

// funcAt names the top-level function containing line n, or "" for top level.
func (s *Source) funcAt(n int) string {
	for _, f := range s.funcs {
		if n >= f.Start && n <= f.End {
			return f.Name
		}
	}
	return ""
}

// Body returns the source text of the named top-level function.
func (s *Source) Body(name string) (string, bool) {
	for _, f := range s.funcs {
		if f.Name == name {
			return strings.Join(s.lines[f.Start-1:f.End], "\n"), true
		}
	}
	return "", false
}

// buildStatements joins backslash continuations, splits each logical line into
// commands, and stamps every command with the `if` conditions enclosing it.
//
// The `if` stack must balance across the whole file. It refuses rather than
// truncating, because an unbalanced stack would attribute a gate to the wrong
// statement — and attributing "this teardown is gated" wrongly in EITHER
// direction is the failure mode INV-1 is about.
func (s *Source) buildStatements(code []bool) error {
	var conds []string
	depth := 0
	for i := 0; i < len(s.lines); i++ {
		if !code[i] {
			continue
		}
		start := i
		joined := s.lines[i]
		for i+1 < len(s.lines) {
			if strings.HasSuffix(strings.TrimRight(joined, " \t"), "\\") {
				joined = strings.TrimSuffix(strings.TrimRight(joined, " \t"), "\\") + " " + s.lines[i+1]
				i++
				continue
			}
			// A quote left open at end of line continues onto the next one.
			// mistral-vibe embeds a multi-line `awk '…'` program whose braces
			// are DATA; without this they were counted as shell blocks and the
			// whole file was reported as unreadable.
			if openQuote(joined) == 0 {
				break
			}
			joined += "\n" + s.lines[i+1]
			i++
		}
		for _, cmd := range splitCommands(shellWords(joined)) {
			words := cmd.words
			if len(words) == 0 {
				continue
			}
			switch words[0] {
			case "if", "case", "do", "{":
				depth++
			case "fi", "esac", "done", "}":
				depth--
				if depth < 0 {
					return fmt.Errorf("%s:%d: `%s` closes a compound statement that was never "+
						"opened — this file's block structure cannot be read, and a check that "+
						"cannot run must say so", s.Path, start+1, words[0])
				}
			}
			switch words[0] {
			case "if":
				conds = append(conds, strings.Join(words[1:], " "))
			case "elif":
				if len(conds) == 0 {
					return fmt.Errorf("%s:%d: `elif` with no open `if`", s.Path, start+1)
				}
				conds[len(conds)-1] = strings.Join(words[1:], " ")
			case "fi":
				if len(conds) == 0 {
					return fmt.Errorf("%s:%d: `fi` with no open `if` — the if/fi structure "+
						"cannot be read, and a check that cannot run must say so", s.Path, start+1)
				}
				conds = conds[:len(conds)-1]
			}
			st := Statement{
				Line: start + 1, Text: joined, Words: words,
				Prefix: cmd.prefix, Func: s.funcAt(start + 1), Depth: depth,
			}
			if len(conds) > 0 {
				st.Conds = append([]string(nil), conds...)
			}
			s.stmts = append(s.stmts, st)
		}
	}
	if len(conds) != 0 {
		return fmt.Errorf("%s: %d `if` block(s) are never closed by `fi` — the if/fi structure "+
			"cannot be read, and a check that cannot run must say so", s.Path, len(conds))
	}
	if depth != 0 {
		return fmt.Errorf("%s: %d compound statement(s) are never closed — the block structure "+
			"cannot be read, and a check that cannot run must say so", s.Path, depth)
	}
	return nil
}

// Statements exposes the parsed commands in file order.
func (s *Source) Statements() []Statement { return s.stmts }

// Command returns the statement's command name and arguments, skipping leading
// keywords and variable-assignment prefixes. ok is false for a statement that
// runs no command (a bare assignment, a `[[ … ]]` test, a keyword).
func (st Statement) Command() (name string, args []string, ok bool) {
	w := st.Words
	for len(w) > 0 {
		switch {
		case isKeyword(w[0]), assignRe.MatchString(w[0]):
			w = w[1:]
		default:
			return w[0], w[1:], true
		}
	}
	return "", nil, false
}

// Assignments returns every `VAR=…` in the statement — both a bare assignment
// and the several a `local a=… b=…` declares.
func (st Statement) Assignments() [][3]string {
	var out [][3]string
	for _, w := range st.Words {
		m := assignRe.FindStringSubmatch(w)
		if m == nil {
			continue
		}
		out = append(out, [3]string{m[1], m[2], m[3]})
	}
	return out
}

func isKeyword(w string) bool {
	switch w {
	case "then", "else", "do", "!", "time", "local", "declare", "export", "readonly", "typeset", "{":
		return true
	}
	return false
}

// command is one shell command plus the code it is conditional on within its
// own logical line.
type command struct {
	words  []string
	prefix string
}

// splitCommands cuts a word list at the operators that separate commands, so
// the `tmux kill-session …` half of `[[ … ]] && tmux kill-session …` is graded
// as its own command with its own name — while still carrying the `[[ … ]]`
// that decides whether it runs. `&&` and `||` accumulate into the prefix; the
// separators that start an unconditional command (`;` `|` `&`) clear it.
func splitCommands(words []string) []command {
	var out []command
	var cur []string
	prefix := ""
	for _, w := range words {
		switch w {
		case "&&", "||":
			out = append(out, command{words: cur, prefix: prefix})
			prefix = strings.TrimSpace(prefix + " " + strings.Join(cur, " "))
			cur = nil
		case ";", ";;", "|", "&", "(", ")":
			out = append(out, command{words: cur, prefix: prefix})
			prefix = ""
			cur = nil
		default:
			cur = append(cur, w)
		}
	}
	return append(out, command{words: cur, prefix: prefix})
}

// shellWords splits one logical line into shell words, keeping quotes and
// expansions intact and emitting command separators as their own words.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs):
			cur.WriteRune(c)
			i++
			cur.WriteRune(rs[i])
		case c == '#' && cur.Len() == 0:
			flush()
			return words // a comment starts only at a word boundary
		case c == ' ' || c == '\t':
			flush()
		case c == '\'':
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			if j >= len(rs) {
				j = len(rs) - 1
			}
			cur.WriteString(string(rs[i : j+1]))
			i = j
		case c == '"':
			j := scanQuoted(rs, i)
			cur.WriteString(string(rs[i : j+1]))
			i = j
		case c == '$' && i+1 < len(rs) && (rs[i+1] == '(' || rs[i+1] == '{'):
			j := scanExpansion(rs, i)
			cur.WriteString(string(rs[i : j+1]))
			i = j
		case c == ';' || c == '|' || c == '&':
			flush()
			j := i
			for j+1 < len(rs) && rs[j+1] == c {
				j++
			}
			words = append(words, string(rs[i:j+1]))
			i = j
		case c == '(' || c == ')':
			flush()
			words = append(words, string(c))
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return words
}

// scanQuoted returns the index of the `"` closing the one at i, stepping over
// escapes and over `$( … )` / `${ … }` (which may contain quotes of their own).
// An unterminated quote yields the last index and closed=false.
func scanQuoted(rs []rune, i int) int {
	j, _ := scanQuotedOK(rs, i)
	return j
}

func scanQuotedOK(rs []rune, i int) (int, bool) {
	for j := i + 1; j < len(rs); j++ {
		switch {
		case rs[j] == '\\':
			j++
		case rs[j] == '$' && j+1 < len(rs) && (rs[j+1] == '(' || rs[j+1] == '{'):
			j = scanExpansion(rs, j)
		case rs[j] == '"':
			return j, true
		}
	}
	return len(rs) - 1, false
}

// openQuote reports the quote character left unterminated at the end of s, or
// 0 when every quote on the line is closed. Text after an unquoted `#` is a
// comment and never opens one — an apostrophe in prose is not a shell quote.
func openQuote(s string) rune {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch {
		case rs[i] == '\\':
			i++
		case rs[i] == '#' && (i == 0 || rs[i-1] == ' ' || rs[i-1] == '\t'):
			return 0
		case rs[i] == '\'':
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			if j >= len(rs) {
				return '\''
			}
			i = j
		case rs[i] == '"':
			j, ok := scanQuotedOK(rs, i)
			if !ok {
				return '"'
			}
			i = j
		}
	}
	return 0
}

// scanExpansion returns the index of the bracket closing the expansion at i.
func scanExpansion(rs []rune, i int) int {
	opener, closer := '(', ')'
	if rs[i+1] == '{' {
		opener, closer = '{', '}'
	}
	depth := 0
	for j := i + 1; j < len(rs); j++ {
		switch {
		case rs[j] == '\\':
			j++
		case rs[j] == '\'' || rs[j] == '"':
			q := rs[j]
			for j++; j < len(rs) && rs[j] != q; j++ {
				if rs[j] == '\\' {
					j++
				}
			}
		case rs[j] == opener:
			depth++
		case rs[j] == closer:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(rs) - 1
}

// unquote strips one layer of surrounding double or single quotes.
func unquote(w string) string {
	if len(w) >= 2 && (w[0] == '"' || w[0] == '\'') && w[len(w)-1] == w[0] {
		return w[1 : len(w)-1]
	}
	return w
}

// soleVar names the variable a word expands to when the word is exactly one
// expansion, and reports whether it is one. `$1` comes back as the positional
// index 1 with positional true.
func soleVar(word string) (name string, positional int, ok bool) {
	m := soleVarRe.FindStringSubmatch(unquote(word))
	if m == nil {
		return "", 0, false
	}
	v := m[1]
	if v == "" {
		v = m[2]
	}
	if n, err := strconv.Atoi(v); err == nil {
		return "", n, true
	}
	return v, 0, true
}
