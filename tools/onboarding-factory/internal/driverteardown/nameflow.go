package driverteardown

import (
	"fmt"
	"sort"
	"strings"
)

type mintSite struct {
	line    int
	literal string
}

// funcRef identifies ONE function definition: the file it is written in, plus
// its name.
//
// Keying positional facts on the bare NAME was a defect. LoadDriver globs the
// whole replaydata/_lib/drive directory and hands every adapter every file in
// it, sourced or not, and two different `alloc_slot`s exist (`git grep -n
// 'alloc_slot()' -- replaydata`): the shared _lib/drive/slots.sh takes the tmux
// session name at argument 1, while claudecode's private one, defined in its own
// driver, takes it at argument 2. On one bare key
// both positions get marked, and a literal at the wrong position is then graded
// as a session name — a false INV-3 against a correct driver.
//
// It does not fire today, and that was measured rather than assumed: the flow
// resolves claudecode to {2} and codex to {1} with no contamination, but only
// because claudecode never touches SESSION or SES_SESSION, so slots.sh's `sess`
// never becomes name-carrying in claudecode's pass. One driver that used the
// shared slot model AND kept a `SESSION` alias would light it.
// inv3_shadowed_alloc_slot_ok.sh is that driver.
type funcRef struct {
	file string
	name string
}

// nameFlow is the fixpoint of "what carries a tmux session name".
type nameFlow struct {
	vars  map[string]bool          // variable names
	pos   map[funcRef]map[int]bool // one DEFINITION -> its name-carrying argument positions
	def   map[string]string        // function name -> the file whose definition a call resolves to
	seeds []mintSite               // names written inline at the launch itself
	all   []*Source
}

func newNameFlow(src *Source, libs []*Source) (*nameFlow, error) {
	f := &nameFlow{vars: map[string]bool{}, pos: map[funcRef]map[int]bool{}, def: map[string]string{}}
	f.all = append([]*Source{src}, libs...)
	// Which definition a call resolves to: the driver's own if it has one, else
	// the first library that defines the name. Same precedence as lookupFunc,
	// and the same reason — a driver that defines a helper the shared libs also
	// define is shadowing it, because its definition is sourced last.
	for _, s := range f.all {
		for _, fn := range s.funcs {
			if _, seen := f.def[fn.Name]; !seen {
				f.def[fn.Name] = s.Path
			}
		}
	}
	if err := f.seed(src); err != nil {
		return nil, err
	}
	for changed := true; changed; {
		changed = false
		for _, s := range f.all {
			if f.propagate(s) {
				changed = true
			}
		}
	}
	return f, nil
}

// seed reads the `-s` argument of every `tmux new-session`. A launch with no
// `-s` is an error: tmux would name the session itself, which is precisely the
// state INV-3 exists to forbid, and reading it as "nothing to grade" would let
// it pass.
func (f *nameFlow) seed(src *Source) error {
	launches := 0
	for _, st := range src.Statements() {
		args, launched := tmuxArgs(st, "new-session")
		if !launched {
			continue
		}
		launches++
		arg, found := flagValue(args, "-s")
		if !found {
			return fmt.Errorf("%s:%d: `tmux new-session` with no `-s <name>` — tmux would name "+
				"the session itself, so it could not carry the driver's PID and no post-run "+
				"assertion could attribute it", src.Path, st.Line)
		}
		if v, _, isVar := soleVar(arg); isVar && v != "" {
			f.vars[v] = true
			continue
		}
		if lit := unquote(arg); strings.TrimSpace(lit) != "" {
			f.seeds = append(f.seeds, mintSite{line: st.Line, literal: lit})
		}
	}
	if launches == 0 {
		return fmt.Errorf("%s: `tmux new-session` appears in the text but not as a parsed "+
			"command — the lexer and the text disagree, so nothing here was actually graded",
			src.Path)
	}
	return nil
}

func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// propagate runs one pass of the closure over one file, reporting whether it
// learned anything new. The closure grows along two edges, and they are graded
// separately below because they are different relations: an ASSIGNMENT relates
// two variables (or a variable and one of its function's parameters), while a
// CALL relates an argument to the parameter position it lands on.
func (f *nameFlow) propagate(src *Source) bool {
	changed := false
	for _, st := range src.Statements() {
		if f.propagateAssignments(src.Path, st) {
			changed = true
		}
		if f.propagateCallArgs(st) {
			changed = true
		}
	}
	return changed
}

// propagateAssignments walks one statement's `LHS=RHS` pairs. `local sess="$1"`
// inside a function marks that function's PARAMETER position; everything else
// relates two variables, and the relation runs both ways — a name-carrying
// variable assigned from another makes that other name-carrying too, which is
// what walks the closure backwards from the launch to the text that mints it.
func (f *nameFlow) propagateAssignments(path string, st Statement) bool {
	changed := false
	for _, a := range st.Assignments() {
		lhs, rhs := a[0], a[2]
		v, positional, isVar := soleVar(rhs)
		switch {
		case !isVar:
			continue
		case positional == 0 && v == "":
			// $0 names the current script. It is a positional expansion, but it
			// is not a function argument and it is not a named shell variable.
			continue
		case positional > 0:
			// Marked against THIS file's definition. Marking a definition no
			// call resolves to is harmless — nothing reads that key — and
			// keeping it unconditional keeps the fixpoint a single pass.
			if !f.vars[lhs] || st.Func == "" {
				continue
			}
			if f.markPos(funcRef{file: path, name: st.Func}, positional) {
				changed = true
			}
		default:
			if f.vars[lhs] && f.markVar(v) {
				changed = true
			}
			if f.vars[v] && f.markVar(lhs) {
				changed = true
			}
		}
	}
	return changed
}

// propagateCallArgs marks the variable passed at a name-carrying argument
// position of the ONE definition this call resolves to.
func (f *nameFlow) propagateCallArgs(st Statement) bool {
	name, args, ok := st.Command()
	if !ok {
		return false
	}
	changed := false
	for n := range f.posOf(name) {
		if n > len(args) {
			continue
		}
		if v, _, isVar := soleVar(args[n-1]); isVar && v != "" && f.markVar(v) {
			changed = true
		}
	}
	return changed
}

// markVar records that a variable carries a tmux session name, reporting whether
// that was new — which is what drives the fixpoint to a halt.
func (f *nameFlow) markVar(v string) bool {
	if f.vars[v] {
		return false
	}
	f.vars[v] = true
	return true
}

// markPos records that argument n of one function DEFINITION carries a session
// name, reporting whether that was new.
func (f *nameFlow) markPos(fn funcRef, n int) bool {
	if f.pos[fn][n] {
		return false
	}
	if f.pos[fn] == nil {
		f.pos[fn] = map[int]bool{}
	}
	f.pos[fn][n] = true
	return true
}

// posOf gives the name-carrying argument positions of the ONE definition a call
// to fn resolves to, so a same-named function in a library the driver never
// sources cannot contribute positions to it.
func (f *nameFlow) posOf(fn string) map[int]bool {
	file, ok := f.def[fn]
	if !ok {
		return nil
	}
	return f.pos[funcRef{file: file, name: fn}]
}

// mintSites lists every composed literal in ONE file that becomes a tmux
// session name — assigned to a name-carrying variable, or passed at a
// name-carrying argument position.
func (f *nameFlow) mintSites(src *Source) []mintSite {
	out := append([]mintSite(nil), f.seeds...)
	add := func(line int, word string) {
		if lit, ok := composedLiteral(word); ok {
			out = append(out, mintSite{line: line, literal: lit})
		}
	}
	for _, st := range src.Statements() {
		for _, a := range st.Assignments() {
			if lhs, rhs := a[0], a[2]; f.vars[lhs] {
				add(st.Line, rhs)
			}
		}
		name, args, ok := st.Command()
		if !ok {
			continue
		}
		for n := range f.posOf(name) {
			if n > len(args) {
				continue
			}
			add(st.Line, args[n-1])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

// composedLiteral reports the text a word contributes when it MINTS a name
// rather than merely carrying one: a bare variable expansion is the closure's
// business and not a mint site, and empty text names nothing.
//
// Deliberately not shared with nameFlow.seed, whose test differs: at a launch,
// `tmux new-session -s "$1"` is a POSITIONAL expansion that soleVar reports as a
// variable with no name, and seed grades it as a literal — a session name it
// cannot trace is one INV-3 must still report on, not one it quietly drops.
func composedLiteral(word string) (string, bool) {
	if _, _, isVar := soleVar(word); isVar {
		return "", false
	}
	lit := unquote(word)
	if strings.TrimSpace(lit) == "" {
		return "", false
	}
	return lit, true
}
