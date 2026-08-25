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
	// Blocks are the KINDS of those enclosing compound statements, outermost
	// first: "if", "case", "do" (a loop body), "{" (a group or a function
	// body). len(Blocks) is Depth — they are maintained as one stack so the
	// count and the kinds cannot drift apart.
	//
	// Depth alone could not tell INV-1's two top-level shapes apart. The
	// end-of-run sweep every driver writes is `for (( i = 1; i <= N_SLOTS;
	// i++ )); do … done`, and copilot's step dispatch is `while … case … esac
	// … done`; both put a `tmux kill-session` at Func "" and a non-zero depth.
	// The kinds, in order, are what separates them. See isStepDispatchArm.
	Blocks []string
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
	var n nesting
	for i := 0; i < len(s.lines); i++ {
		if !code[i] {
			continue
		}
		start := i
		joined, words, end, err := s.joinLogicalLine(start)
		if err != nil {
			return err
		}
		i = end
		for _, cmd := range splitCommands(words) {
			if len(cmd.words) == 0 {
				continue
			}
			if err := n.track(s.Path, start+1, cmd.words); err != nil {
				return err
			}
			st := Statement{
				Line: start + 1, Text: joined, Words: cmd.words,
				Prefix: cmd.prefix, Func: s.funcAt(start + 1),
			}
			n.stamp(&st)
			s.stmts = append(s.stmts, st)
		}
	}
	return n.close(s.Path)
}

// joinLogicalLine gathers the physical lines that form the logical line starting
// at index i — those held open by a trailing backslash, and those swallowed by a
// quote left open at end of line — and returns the joined text, its words, and
// the index of the last physical line it consumed.
//
// The open-quote join is not optional. mistral-vibe embeds a multi-line
// `awk '…'` program whose braces are DATA; without it they were counted as shell
// blocks and the whole file was reported as unreadable.
func (s *Source) joinLogicalLine(i int) (joined string, words []string, end int, err error) {
	start := i
	joined = s.lines[i]
	words, open := shellWordsOpen(joined)
	for i+1 < len(s.lines) {
		trimmed := strings.TrimRight(joined, " \t")
		switch {
		case strings.HasSuffix(trimmed, "\\"):
			joined = strings.TrimSuffix(trimmed, "\\") + " " + s.lines[i+1]
		case open != 0:
			joined += "\n" + s.lines[i+1]
		default:
			return joined, words, i, nil
		}
		i++
		words, open = shellWordsOpen(joined)
	}
	// The joiner ran out of file still looking for the closing quote. Every
	// remaining line has been swallowed into one quoted word — the sink of the
	// same class of silent loss the shared scanner closes in shellWordsOpen, and
	// the one shape a shared scanner cannot see, because both halves agree the
	// quote is open. Refuse: an unreadable tail and a clean tail must not be the
	// same answer.
	if open != 0 {
		return "", nil, 0, fmt.Errorf("%s:%d: a %c quote opened on this line is never closed "+
			"before the end of the file, so every statement after it is swallowed into one "+
			"quoted word — this file cannot be read, and a check that cannot run must say so",
			s.Path, start+1, open)
	}
	return joined, words, i, nil
}

// nesting is the compound-block and `if`-condition stack buildStatements carries
// down a file. The two live in ONE type because they have to agree: a `fi` that
// popped a block but not a condition would attribute a gate to the wrong
// statement, and attributing "this teardown is gated" wrongly in either
// direction is the failure mode INV-1 is about.
type nesting struct {
	blocks []string // kinds of the enclosing compound statements, outermost first
	conds  []string // the `if`/`elif` conditions enclosing, outermost first
}

// track updates the stack for one command's leading word, refusing whenever the
// file's structure does not balance rather than truncating to a best effort.
func (n *nesting) track(path string, line int, words []string) error {
	switch words[0] {
	case "if":
		n.blocks = append(n.blocks, words[0])
		n.conds = append(n.conds, strings.Join(words[1:], " "))
	case "case", "do", "{":
		n.blocks = append(n.blocks, words[0])
	case "fi":
		if err := n.popBlock(path, line, words[0]); err != nil {
			return err
		}
		if len(n.conds) == 0 {
			return fmt.Errorf("%s:%d: `fi` with no open `if` — the if/fi structure "+
				"cannot be read, and a check that cannot run must say so", path, line)
		}
		n.conds = n.conds[:len(n.conds)-1]
	case "esac", "done", "}":
		return n.popBlock(path, line, words[0])
	case "elif":
		if len(n.conds) == 0 {
			return fmt.Errorf("%s:%d: `elif` with no open `if`", path, line)
		}
		n.conds[len(n.conds)-1] = strings.Join(words[1:], " ")
	}
	return nil
}

func (n *nesting) popBlock(path string, line int, word string) error {
	if len(n.blocks) == 0 {
		return fmt.Errorf("%s:%d: `%s` closes a compound statement that was never "+
			"opened — this file's block structure cannot be read, and a check that "+
			"cannot run must say so", path, line, word)
	}
	n.blocks = n.blocks[:len(n.blocks)-1]
	return nil
}

// stamp records where in the nesting a statement was found. Depth and Blocks are
// read off the same slice, so the count and the kinds cannot drift apart.
func (n *nesting) stamp(st *Statement) {
	st.Depth = len(n.blocks)
	if len(n.blocks) > 0 {
		st.Blocks = append([]string(nil), n.blocks...)
	}
	if len(n.conds) > 0 {
		st.Conds = append([]string(nil), n.conds...)
	}
}

// close refuses a file whose stacks never emptied.
func (n *nesting) close(path string) error {
	if len(n.conds) != 0 {
		return fmt.Errorf("%s: %d `if` block(s) are never closed by `fi` — the if/fi structure "+
			"cannot be read, and a check that cannot run must say so", path, len(n.conds))
	}
	if len(n.blocks) != 0 {
		return fmt.Errorf("%s: %d compound statement(s) are never closed — the block structure "+
			"cannot be read, and a check that cannot run must say so", path, len(n.blocks))
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

// shellWordsOpen splits one logical line into shell words, keeping quotes and
// expansions intact and emitting command separators as their own words, and
// reports the quote character left unterminated at the end of s — 0 when the
// text ends outside any quote, INCLUDING when it ends inside a comment, which
// cannot open one.
//
// WHY ONE FUNCTION ANSWERS BOTH QUESTIONS. It used to be two, and they
// disagreed about where a comment starts. This scanner's rule is "a `#` that
// opens a word"; a separate openQuote's rule was "a `#` at column 0 or after a
// space/tab". A comment glued to code sat in the gap:
//
//	bar=1;# the driver's flag
//
// openQuote read the apostrophe in "driver's" as an unterminated quote, so
// buildStatements joined the following lines on to close it, and this scanner
// then truncated the joined text at the `#` — dropping every statement that had
// just been joined in. Silently: the dropped lines took their own `if`/`fi` and
// `do`/`done` with them, so the balance assertions stayed happy. A dropped
// `trap … EXIT` or `REACHED_EPILOGUE=1` is a false POSITIVE and merely noisy,
// but a dropped top-level gated `tmux kill-session` is a false CLEAN on INV-1
// with `sites` still non-zero, which is the one failure the vacuity guard
// cannot catch.
//
// Aligning the two rules would have fixed the shape and left the hazard: two
// scanners doing the same job drift. Deleting one is what makes the class of
// defect unreachable, and this file's contract is that every place it can be
// wrong is a place it refuses instead.
func shellWordsOpen(s string) (words []string, open rune) {
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
			cur.WriteString(string(rs[i : i+2]))
			i++
		case c == '#' && cur.Len() == 0:
			// A comment starts only at a word boundary — and it runs to the end
			// of the text, so nothing after it can open a quote.
			flush()
			return words, 0
		case c == ' ' || c == '\t':
			flush()
		case c == '\'' || c == '"':
			j, closed := scanQuoted(rs, i)
			if !closed {
				open = c
			}
			cur.WriteString(string(rs[i : j+1]))
			i = j
		case opensExpansion(rs, i):
			j := scanExpansion(rs, i)
			cur.WriteString(string(rs[i : j+1]))
			i = j
		case c == ';' || c == '|' || c == '&':
			flush()
			j := runEnd(rs, i)
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
	return words, open
}

// scanQuoted returns the index of the quote closing the one at i, and whether it
// found one. An unterminated quote yields the last index and closed=false.
//
// The two quote kinds need different scans — a single-quoted string has no
// escapes and no expansions — but they report the SAME shape, so shellWordsOpen
// handles them in one arm and the "which quote is still open" answer cannot come
// to depend on which quote it was. That is the same unification, for the same
// reason, that shellWordsOpen's own doc comment sets out.
func scanQuoted(rs []rune, i int) (int, bool) {
	if rs[i] == '\'' {
		return scanSingleQuoted(rs, i)
	}
	return scanDoubleQuoted(rs, i)
}

// scanSingleQuoted returns the index of the `'` closing the one at i. Nothing
// inside a single-quoted string is special, so the scan is a plain search.
func scanSingleQuoted(rs []rune, i int) (int, bool) {
	for j := i + 1; j < len(rs); j++ {
		if rs[j] == '\'' {
			return j, true
		}
	}
	return len(rs) - 1, false
}

// scanDoubleQuoted returns the index of the `"` closing the one at i, stepping
// over escapes and over `$( … )` / `${ … }` (which may contain quotes of their
// own).
func scanDoubleQuoted(rs []rune, i int) (int, bool) {
	for j := i + 1; j < len(rs); j++ {
		switch {
		case rs[j] == '\\':
			j++
		case opensExpansion(rs, j):
			j = scanExpansion(rs, j)
		case rs[j] == '"':
			return j, true
		}
	}
	return len(rs) - 1, false
}

// opensExpansion reports whether a `$( … )` or `${ … }` expansion begins at i.
//
// One spelling, consulted by both the word scanner and the double-quote scanner.
// They have to agree about where an expansion starts: a `"` or a `#` inside one
// is not a quote or a comment, and shellWordsOpen's doc comment is the record of
// what it cost the last time two scanners in this file disagreed about a
// boundary.
func opensExpansion(rs []rune, i int) bool {
	return rs[i] == '$' && i+1 < len(rs) && (rs[i+1] == '(' || rs[i+1] == '{')
}

// runEnd returns the last index of the run of the operator character at i, so
// `&&` and `;;` come out as one word rather than two.
func runEnd(rs []rune, i int) int {
	j := i
	for j+1 < len(rs) && rs[j+1] == rs[i] {
		j++
	}
	return j
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
