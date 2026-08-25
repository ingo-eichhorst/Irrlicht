// Package driverteardown grades the onboarding-factory's interactive
// recording drivers against the four teardown invariants #1825 was opened
// for. It is a STATIC check over driver source text: it needs no tmux, no
// agent CLI and no recording, so unlike the rig itself it runs in CI (`tmux`
// appears nowhere under .github/ — the recording harness has never run there).
//
// The failure it exists to catch is a leak that reports success. A driver that
// marks a slot dead on an optimistic flag and then gates its teardown on that
// same flag leaves the agent process and its tmux session running while
// writing driver.exit-reason=ok. #1825 measured nine such runs for claudecode
// — claude answers a single Ctrl-D with "Press Ctrl-D again to exit" and lets
// the confirmation expire — and the audit in that issue found the same shape
// in codex, pi and kiro-cli. Nothing anywhere said why.
//
//	INV-1  End-of-run teardown is UNGATED. A `tmux kill-session` that belongs
//	       to teardown must gate on session-name PRESENCE
//	       (`-n "${SES_SESSION[$i]:-}"`), never on a liveness flag the driver
//	       set optimistically and nothing re-derives from `tmux has-session`.
//	INV-2  Every tmux-launching driver installs a `trap … EXIT` whose handler
//	       tears sessions down, so a `set -e` abort mid-run still cleans up.
//	       The reference shape is
//	       tools/onboarding-factory/scripts/templates/drive-interactive.sh.tmpl.
//	INV-3  Every tmux session name a driver mints carries the driver process's
//	       own `$$` as a `-`-delimited field. run-cell.sh has no record of the
//	       names a driver chose — no staging-contract file carries them — so
//	       the PID embedded in the name is the ONLY join from a run to the
//	       sessions it owns, and the post-run leak assertion rests on it.
//	INV-4  An EXIT-trap handler that writes the staging contract's verdict file
//	       cannot write it with the verdict variable still at the value it held
//	       when the trap was armed. Adding a trap to satisfy INV-2 turns "no
//	       verdict file, read as unknown" into "the initialiser, read as ok",
//	       so the fix for a leak that reports success can manufacture a
//	       different driver that reports success. See checkVerdictCannotBeStale.
//
// Adapters that never launch tmux are exempt, and the exemption is DERIVED
// (no `tmux new-session` in the file) rather than listed, so an adapter added
// tomorrow is graded by existing rather than by whoever adds it remembering
// this package.
//
// # What these invariants deliberately do NOT check
//
// Each invariant's own doc comment states its residual gaps. One gap belongs to
// no single invariant and is recorded here so it lives in the repo rather than
// in whoever last looked:
//
// A `trap - EXIT` that DISARMS the teardown trap is invisible to INV-2. INV-2
// asks whether an arming trap with a tearing-down handler exists; a trap that
// is installed and later removed satisfies it. opencode's driver does exactly
// this, and it is benign there — checked, not assumed: the disarm sits
// immediately after an unconditional `tmux kill-session`, so no session is
// alive when the trap goes away. A future driver that disarmed BEFORE its
// teardown would pass INV-2 while being as exposed as the four adapters #1825
// started with.
//
// No rule for it is shipped, and that is a judgment rather than an oversight.
// Three candidate rules were considered and each fails in the way that makes a
// rule worse than a documented gap — it looks like it grades this and does not:
//
//   - "flag a disarm not preceded by an unconditional teardown in the same
//     region" passes a multi-slot driver that unconditionally kills ONE slot
//     (`tmux kill-session -t "$SESSION"` is the active slot, not all N) and
//     then disarms with the other slots still live. That is the leak, wearing
//     the shape the rule accepts.
//   - "flag a disarm followed by any `tmux new-session`" reads SOURCE order as
//     EXECUTION order. A disarm inside a function called early and a launch
//     inside a function defined earlier but called later is a leak the rule
//     reports clean.
//   - "forbid `trap - EXIT` outright in a tmux-launching driver" is robust and
//     trivially expressible, and would fail opencode, which is correct today.
//     A finding against a driver that is right is the kind that gets a rule
//     ignored rather than fixed — the same reasoning that shaped aliveGate.
//
// Grading it soundly needs reachability and slot-aliasing analysis, which a
// text checker does not have. Anyone adding a disarm to a driver is on their
// own; this paragraph is the warning.
package driverteardown

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AgentsDir is the catalog directory holding one subdirectory per adapter.
const AgentsDir = "replaydata/agents"

// DriverName is the file every onboarded adapter carries.
const DriverName = "driver-interactive.sh"

// libDir holds the shell libraries the drivers source. They participate in the
// analysis (alloc_slot lives there, and so does the slot bookkeeping a session
// name flows through) but are never themselves reported: they are shared, so a
// finding there would be raised once per adapter against a file no adapter owns.
const libDir = "replaydata/_lib/drive"

// aliveGate names the anti-pattern INV-1 forbids at a teardown site.
//
// TRADEOFF, stated plainly: this is a NAMED anti-pattern, not a general
// "optimistic flag" detector. It matches the substring `alive`, case
// insensitively, anywhere in the gate. A driver that invented a differently
// named liveness flag — `SES_RUNNING`, say — would gate its teardown on it and
// pass. The reason to accept that is that the flag is not per-driver: SES_ALIVE
// is declared and maintained by the SHARED replaydata/_lib/drive/slots.sh that
// every slot-model driver sources, and claudecode's private slot scheme uses
// the same name. Matching the name that exists keeps the rule readable and
// keeps its false-POSITIVE rate at zero, which matters more here: a finding
// against a driver that is correct is the kind that gets a rule ignored rather
// than fixed. The general form of the rule — "teardown must gate on session
// presence" — is stated in the finding text so the next reader knows what the
// substring is standing in for.
var aliveGate = regexp.MustCompile(`(?i)alive`)

// pidField is the `$$` a minted session name must carry as its own
// `-`-delimited field.
const pidField = "$$"

// exitReasonFile is the staging contract's verdict file — the one run-cell.sh
// reads at :443 with `|| echo "unknown"`, which is why an EXIT trap that writes
// it unconditionally is a behaviour CHANGE and not just a tidy-up.
//
// It is a filename, not a variable name, and that is what makes matching it
// legitimate where matching `SES_ALIVE` was a named-anti-pattern compromise: it
// is a contract between two files, so a rename has to touch run-cell.sh too.
// TestVerdictFilenameIsTheOneRunCellReads asserts that it still does.
const exitReasonFile = "driver.exit-reason"

// condVarRe extracts the variables a shell condition expands.
var condVarRe = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// File is one shell source under analysis.
type File struct {
	Path string
	Src  string
}

// Finding is one violated invariant, anchored to a line of the driver.
type Finding struct {
	Invariant string // "INV-1", "INV-2", "INV-3" or "INV-4"
	Path      string
	Line      int // 0 when the finding is about the file as a whole
	Excerpt   string
	Detail    string
}

func (f Finding) String() string {
	loc := f.Path
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	s := fmt.Sprintf("%s %s: %s", f.Invariant, loc, f.Detail)
	if f.Excerpt != "" {
		s += "\n    " + strings.TrimSpace(f.Excerpt)
	}
	return s
}

// LaunchCount counts `tmux new-session` in a driver's CODE. It is the vacuity
// measurement, kept separate from every verdict so "this driver launches no
// tmux session" and "every launch this driver makes is fine" can never be
// confused for one another.
//
// Comments are stripped first, and only comments: a driver's prose routinely
// names the thing it is talking about, and reading "see the tmux new-session
// above" as a launch would take a genuinely headless driver out of its derived
// exemption and hard-fail it for having no teardown to grade. Everything else
// stays counted — over-counting means the driver gets GRADED, which is the
// safe direction, while under-counting would exempt one silently.
func LaunchCount(src string) int {
	n := 0
	for _, ln := range strings.Split(src, "\n") {
		if strings.Contains(stripComment(ln), "tmux new-session") {
			n++
		}
	}
	return n
}

// CheckDriver grades one driver against INV-1 through INV-4.
//
// libs are the shell libraries the driver sources. They are read for the
// dataflow the invariants need — a session name minted at an `alloc_slot`
// call site reaches `tmux new-session -s` only through slots.sh — and never
// reported against.
//
// It returns an error, not an empty finding list, whenever it could not
// actually look: an empty driver, a structure it cannot parse, a launch with
// no `-s`, a tmux-launching driver in which it found nothing to grade.
func CheckDriver(driver File, libs []File) ([]Finding, error) {
	if strings.TrimSpace(driver.Src) == "" {
		return nil, fmt.Errorf("%s is empty — a driver that cannot be read graded nothing, "+
			"and a check that cannot run must say so", driver.Path)
	}
	src, err := Parse(driver.Path, driver.Src)
	if err != nil {
		return nil, err
	}
	if LaunchCount(driver.Src) == 0 {
		// Derived exemption: no `tmux new-session` means no session to leak
		// and no name to carry a PID. The non-empty check above is what keeps
		// this from also covering "the file was never read".
		return nil, nil
	}
	parsedLibs, err := parseAll(libs)
	if err != nil {
		return nil, err
	}

	traps, err := exitTraps(src, parsedLibs)
	if err != nil {
		return nil, err
	}
	findings := checkTrapExists(driver, traps)

	inv1, err := checkTeardownUngated(src, traps)
	if err != nil {
		return nil, err
	}
	findings = append(findings, inv1...)

	inv3, err := checkSessionNamesCarryPID(src, parsedLibs)
	if err != nil {
		return nil, err
	}
	findings = append(findings, inv3...)

	inv4, err := checkVerdictCannotBeStale(src, traps)
	if err != nil {
		return nil, err
	}
	return append(findings, inv4...), nil
}

func parseAll(files []File) ([]*Source, error) {
	out := make([]*Source, 0, len(files))
	for _, f := range files {
		p, err := Parse(f.Path, f.Src)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// exitTrap is one armed `trap … EXIT`.
type exitTrap struct {
	line      int
	fn        string      // handler function name, "" for an inline handler
	body      string      // the handler's source text
	stmts     []Statement // the handler's commands, in order
	tearsDown bool
}

// exitTraps finds every armed EXIT trap and resolves each handler's body,
// following a named handler into the driver and then into the sourced libs. A
// handler it cannot resolve is an error: "this driver has no teardown trap"
// and "the trap's handler could not be found" must not be the same answer.
func exitTraps(src *Source, libs []*Source) ([]exitTrap, error) {
	var out []exitTrap
	for _, st := range src.Statements() {
		name, args, ok := st.Command()
		if !ok || name != "trap" || len(args) < 2 {
			continue
		}
		if !argsNameEXIT(args[1:]) {
			continue
		}
		handler := unquote(args[0])
		if handler == "-" || handler == "" {
			continue // `trap - EXIT` disarms; it arms nothing to grade
		}
		t := exitTrap{line: st.Line}
		if isIdentifier(handler) {
			owner, body, found := lookupFunc(handler, src, libs)
			if !found {
				return nil, fmt.Errorf("%s:%d: `trap %s EXIT` names a handler that is defined "+
					"neither in this driver nor in any library it sources — the trap cannot be "+
					"graded, and a check that cannot run must say so", src.Path, st.Line, handler)
			}
			t.fn, t.body = handler, body
			for _, s := range owner.Statements() {
				if s.Func == handler {
					t.stmts = append(t.stmts, s)
				}
			}
		} else {
			t.body = handler
			// An inline handler is one quoted word, so its commands are not
			// statements of the enclosing file. Parse the word's text as its
			// own source rather than reading the handler as an opaque string.
			inline, err := Parse(fmt.Sprintf("%s:%d (inline EXIT trap)", src.Path, st.Line), handler)
			if err != nil {
				return nil, err
			}
			t.stmts = inline.Statements()
		}
		t.tearsDown = strings.Contains(t.body, "tmux kill-session")
		out = append(out, t)
	}
	return out, nil
}

func argsNameEXIT(args []string) bool {
	for _, a := range args {
		switch strings.ToUpper(unquote(a)) {
		case "EXIT", "0":
			return true
		}
	}
	return false
}

func isIdentifier(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}
	return s != ""
}

// lookupFunc finds a function definition, preferring the driver over the
// libraries it sources, and returns the file it was found in so the caller can
// read its statements rather than only its text.
func lookupFunc(name string, src *Source, libs []*Source) (*Source, string, bool) {
	if b, ok := src.Body(name); ok {
		return src, b, true
	}
	for _, l := range libs {
		if b, ok := l.Body(name); ok {
			return l, b, true
		}
	}
	return nil, "", false
}

// checkTrapExists is INV-2.
func checkTrapExists(driver File, traps []exitTrap) []Finding {
	for _, t := range traps {
		if t.tearsDown {
			return nil
		}
	}
	detail := "this driver launches the agent under tmux but installs no `trap … EXIT` that " +
		"tears the session down. Any exit path that does not reach the end of the script — a " +
		"`set -e` abort mid-launch, a failed jq, an `exit` from a step — then leaves the pane " +
		"and the agent process alive. See scripts/templates/drive-interactive.sh.tmpl's `cleanup`."
	line := 0
	if len(traps) > 0 {
		line = traps[0].line
		detail = "this driver's `trap … EXIT` handler contains no `tmux kill-session`, so an " +
			"abort mid-run leaves the pane and the agent process alive. See " +
			"scripts/templates/drive-interactive.sh.tmpl's `cleanup`."
	}
	return []Finding{{Invariant: "INV-2", Path: driver.Path, Line: line, Detail: detail}}
}

// checkTeardownUngated is INV-1.
//
// HOW A TEARDOWN SITE IS TOLD APART FROM A STEP-LEVEL GUARD, and the tradeoff.
//
// The distinction is STRUCTURAL — where the `tmux kill-session` sits — not
// textual, because both spellings of the gate are byte-identical:
//
//	teardown (must be ungated)   : the EXIT-trap handler, and the end-of-run
//	                               sweep, which every driver writes at TOP
//	                               LEVEL after the step loop has finished.
//	step-level (may be gated)    : anything inside an ordinary function.
//
// kiro-cli is the case that forces this. Its `step_exit_clean` and
// `step_sigkill` open with `if [[ "${SES_ALIVE[$ACTIVE]:-0}" != "1" ]]; then
// return 0; fi`, and those guards are CORRECT: reset_session gives a new slot
// the SAME tmux pane as an old one, so an already-retired slot number can alias
// a pane a different, still-live slot now owns, and a recipe re-targeting the
// old number must not tear the live session down. A textual rule keyed on
// "SES_ALIVE near a kill-session" would fail those, and a finding against a
// driver that is right is the kind that gets a rule ignored rather than fixed.
//
// The cost of the structural rule is the mirror image: a driver that moved its
// end-of-run sweep INTO a helper function — `final_teardown() { … }` called
// once at the bottom — would have that sweep classified as step-level and its
// gate would go unflagged. That is a real hole and it is worth naming rather
// than papering over. Two things make it acceptable. The sweep is generated
// from a shared template that puts it at top level, so a driver moving it is a
// deliberate act by someone editing teardown; and INV-2 still binds on that
// driver, so the EXIT trap — whose handler IS a named function, and which this
// rule follows by name — must still tear down ungated. The alternative,
// keying on a marker comment the drivers would carry, was rejected because it
// makes the rule depend on the very line an author editing teardown is most
// likely to move or drop.
func checkTeardownUngated(src *Source, traps []exitTrap) ([]Finding, error) {
	handlerFuncs := map[string]bool{}
	sites := 0
	var findings []Finding

	for _, t := range traps {
		if t.fn != "" {
			handlerFuncs[t.fn] = true
			continue
		}
		if !t.tearsDown {
			continue
		}
		// An inline handler is one quoted word, so its kill-session is not a
		// statement of its own; grade the handler text directly.
		sites++
		if aliveGate.MatchString(t.body) {
			findings = append(findings, Finding{
				Invariant: "INV-1", Path: src.Path, Line: t.line, Excerpt: t.body,
				Detail: inv1Detail("this inline EXIT-trap handler"),
			})
		}
	}

	for _, st := range src.Statements() {
		name, args, ok := st.Command()
		if !ok || name != "tmux" || len(args) == 0 || args[0] != "kill-session" {
			continue
		}
		where := ""
		switch {
		case st.Func == "":
			where = "this end-of-run teardown"
		case handlerFuncs[st.Func]:
			where = fmt.Sprintf("this teardown inside the EXIT-trap handler %s()", st.Func)
		default:
			continue // step-level: legitimately allowed to gate on liveness
		}
		sites++
		if !gatedOnLiveness(st) {
			continue
		}
		findings = append(findings, Finding{
			Invariant: "INV-1", Path: src.Path, Line: st.Line, Excerpt: st.Text,
			Detail: inv1Detail(where),
		})
	}

	if sites == 0 {
		return nil, fmt.Errorf("%s launches the agent under tmux but has no `tmux kill-session` "+
			"in its EXIT trap or at top level — INV-1 graded nothing here, and a check that "+
			"cannot run must say so", src.Path)
	}
	return findings, nil
}

func inv1Detail(where string) string {
	return where + " is gated on a liveness flag. That flag is a driver INTENT, not an " +
		"observation — nothing anywhere re-derives it from `tmux has-session` — so a step that " +
		"asked the TUI to exit and was not obeyed clears it, teardown skips the kill, and the " +
		"pane plus the agent process survive the run while driver.exit-reason still says ok. " +
		"Gate on session-name PRESENCE instead: `[[ -n \"${SES_SESSION[$i]:-}\" ]]`, the shape " +
		"scripts/templates/drive-interactive.sh.tmpl already ships."
}

func gatedOnLiveness(st Statement) bool {
	if aliveGate.MatchString(st.Prefix) {
		return true // `[[ "${SES_ALIVE[$i]}" == 1 ]] && tmux kill-session …`
	}
	for _, c := range st.Conds {
		if aliveGate.MatchString(c) {
			return true
		}
	}
	return false
}

// checkVerdictCannotBeStale is INV-4: an EXIT-trap handler that writes the
// staging contract's verdict file must not be able to write it with the verdict
// variable still at the value it had when the trap was armed.
//
// WHY THIS EXISTS, and why it arrived a day after INV-1/INV-2. Before #1825 an
// aborting driver wrote NO driver.exit-reason at all, and run-cell.sh:443 reads
// that absence as `unknown`. Adding an EXIT trap to stop a leak — INV-2's whole
// demand — turns the absence into whatever the verdict variable happens to hold,
// which on an abort before any step ran is its initialiser: `ok`. So the fix for
// "a driver that leaks while reporting success" can manufacture "a driver that
// reports success for a run that never formed a verdict". The agent fixing aider
// caught themselves about to ship exactly that, and INV-2 alone would have
// passed the one-line trap they nearly wrote.
//
// HOW IT IS STATED SO IT IS NOT TRIVIALLY SATISFIABLE. No variable name is
// matched — not the verdict variable's, and above all not the sentinel's, which
// has no shared-library declaration to anchor it the way SES_ALIVE has. The rule
// is built from one structural primitive instead:
//
//	an UNCONDITIONAL TOP-LEVEL assignment, placed after the trap is armed AND
//	after the driver's last `tmux new-session`.
//
// That is what "the epilogue set it" looks like with the names removed: not
// inside a function, not inside an `if`, and downstream of the work whose
// completion it is claiming. A driver satisfies INV-4 by either shape:
//
//	(a) GUARD — inside the handler, before the write, a conditional assigns the
//	    verdict variable, its condition consults some OTHER variable that has
//	    such an assignment, and it does not assign the initial value back. This
//	    is aider's REACHED_EPILOGUE, and it is graded without knowing that name.
//	(b) FAIL-CLOSED — the verdict variable itself has such an assignment, to a
//	    value different from its initialiser. A driver whose verdict starts at a
//	    fault and is promoted to success at the end needs no guard at all, and a
//	    rule that failed it would be prescribing one implementation rather than
//	    the property. inv4_fail_closed_ok.sh is that row.
//
// WHAT IT STILL CANNOT SEE, stated rather than implied:
//   - that the sentinel is set at the RIGHT place. "After the last launch" is a
//     proxy for "immediately before the final exit"; a driver that set it three
//     lines later than it should would pass.
//   - that the value written on the abort path DENOTES failure. The verdict
//     vocabulary belongs to run-cell.sh, not to the drivers, so a guard that
//     assigned some other success-shaped token would pass. Only "not the
//     initialiser" is checked.
//   - which statements can actually abort. `set -e` reachability is not modelled.
//
// The precondition is DERIVED, not assumed: the rule binds only on a handler
// that redirects into a path ending in the staging contract's verdict filename
// AND writes a variable's value there. A handler that writes a literal has no
// initial-value hazard and is exempt by derivation, which is
// inv4_literal_verdict_ok.sh. The filename that anchors all of it is asserted
// against run-cell.sh by TestVerdictFilenameIsTheOneRunCellReads, so a rename
// fails loudly instead of silently switching this whole invariant off.
func checkVerdictCannotBeStale(src *Source, traps []exitTrap) ([]Finding, error) {
	lastLaunch := lastLaunchLine(src)
	var findings []Finding
	for _, t := range traps {
		write, idx, v, ok := verdictWrite(t.stmts)
		if !ok {
			continue // derived exemption: this handler writes no verdict variable
		}
		initial, ok := initialVerdict(src, v, t.line)
		if !ok {
			return nil, fmt.Errorf("%s:%d: the EXIT-trap handler writes $%s to %s, but %s is "+
				"never assigned unconditionally at top level before the trap is armed — INV-4 "+
				"has no initial value to compare against and graded nothing, and a check that "+
				"cannot run must say so", src.Path, write.Line, v, exitReasonFile, v)
		}
		if f := gradeVerdictGuard(src, t, idx, v, initial, lastLaunch); f != nil {
			findings = append(findings, *f)
		}
	}
	return findings, nil
}

// gradeVerdictGuard returns the INV-4 finding for one handler, or nil when the
// handler satisfies either shape. The reason it reports is the most specific
// one that applies, so the message names what to change rather than restating
// the rule.
func gradeVerdictGuard(src *Source, t exitTrap, writeIdx int, v, initial string, lastLaunch int) *Finding {
	// (b) fail-closed: the success path itself moves the verdict off its
	// initialiser, so an abort and a completed run write different bytes.
	for _, a := range epilogueAssignments(src, v, t.line, lastLaunch) {
		if a != initial {
			return nil
		}
	}

	reason := ""
	for _, st := range t.stmts[:writeIdx] {
		rhs, assigns := assignedValue(st, v)
		if !assigns {
			continue
		}
		others := condVarsExcept(strings.TrimSpace(st.Prefix+" "+strings.Join(st.Conds, " ")), v)
		if len(others) == 0 {
			reason = fmt.Sprintf("the only re-assignment of $%s before the write is guarded by "+
				"nothing but $%s itself, which is equally true on a run that completed", v, v)
			continue
		}
		if _, _, isVar := soleVar(rhs); !isVar && unquote(rhs) == initial {
			reason = fmt.Sprintf("the guard on the write assigns $%s its own initial value %q, "+
				"so it changes nothing", v, initial)
			continue
		}
		set := false
		for _, o := range others {
			if len(epilogueAssignments(src, o, t.line, lastLaunch)) > 0 {
				set = true
			}
		}
		if set {
			return nil // (a) guard, satisfied
		}
		reason = fmt.Sprintf("the guard on the write consults %s, and nothing assigns any of "+
			"them unconditionally at top level after the trap is armed and after the last "+
			"`tmux new-session` — so no amount of progress through the run can change what "+
			"the handler writes", strings.Join(dollarize(others), ", "))
	}
	if reason == "" {
		reason = fmt.Sprintf("the handler writes $%s unconditionally, and nothing between the "+
			"trap arming and the write can change it", v)
	}
	return &Finding{
		Invariant: "INV-4", Path: src.Path, Line: t.line, Excerpt: t.body,
		Detail: fmt.Sprintf("this EXIT-trap handler can write %s with $%s still at its initial "+
			"value %q: %s. Before an EXIT trap existed, an abort wrote NO %s and run-cell.sh:443 "+
			"read the absence as `unknown`; a handler that writes the initialiser turns that into "+
			"a verdict the run never formed — on a ticket whose subject is a driver claiming "+
			"success while leaking. Either guard the write on a sentinel the epilogue sets "+
			"unconditionally at top level (aider's `REACHED_EPILOGUE`), or start $%s at a fault "+
			"and promote it on the success path.",
			exitReasonFile, v, initial, reason, exitReasonFile, v),
	}
}

// verdictWrite finds the handler command that redirects into the verdict file
// and names the variable whose value it writes, with its index in the handler.
// A handler that writes a literal reports ok=false: there is no initial value
// for it to be stale at, which is the derived half of INV-4's precondition.
func verdictWrite(stmts []Statement) (st Statement, idx int, v string, ok bool) {
	for i, s := range stmts {
		if !writesVerdictFile(s) {
			continue
		}
		_, args, isCmd := s.Command()
		if !isCmd {
			continue
		}
		for _, a := range args {
			if name, pos, isVar := soleVar(a); isVar && pos == 0 && name != "" {
				return s, i, name, true
			}
		}
		return s, i, "", false
	}
	return Statement{}, 0, "", false
}

// writesVerdictFile reports whether a command redirects into the staging
// contract's verdict file, in either spacing (`> "$S/f"` and `>"$S/f"`).
func writesVerdictFile(st Statement) bool {
	for i, w := range st.Words {
		if w == ">" || w == ">>" {
			if i+1 < len(st.Words) && strings.HasSuffix(unquote(st.Words[i+1]), exitReasonFile) {
				return true
			}
			continue
		}
		if strings.HasPrefix(w, ">") &&
			strings.HasSuffix(unquote(strings.TrimLeft(w, ">")), exitReasonFile) {
			return true
		}
	}
	return false
}

// initialVerdict is the value the verdict variable holds when the trap arms:
// the last unconditional top-level assignment to it before the trap statement.
func initialVerdict(src *Source, v string, trapLine int) (string, bool) {
	val, found := "", false
	for _, st := range src.Statements() {
		if st.Line >= trapLine || !isUnconditionalTopLevel(st) {
			continue
		}
		if rhs, ok := assignedValue(st, v); ok {
			val, found = unquote(rhs), true
		}
	}
	return val, found
}

// epilogueAssignments lists the values assigned to a variable by an
// unconditional top-level statement placed after the trap arms AND after the
// driver's last tmux launch — the name-free structural stand-in for "the
// epilogue set it, once the run's work was behind it".
func epilogueAssignments(src *Source, v string, trapLine, lastLaunch int) []string {
	var out []string
	for _, st := range src.Statements() {
		if st.Line <= trapLine || st.Line <= lastLaunch || !isUnconditionalTopLevel(st) {
			continue
		}
		if rhs, ok := assignedValue(st, v); ok {
			out = append(out, unquote(rhs))
		}
	}
	return out
}

// isUnconditionalTopLevel reports whether a statement runs on every path
// through the script body: outside any function, outside any `if`, and not
// chained behind a `&&` / `||` test.
func isUnconditionalTopLevel(st Statement) bool {
	return st.Func == "" && st.Depth == 0 && strings.TrimSpace(st.Prefix) == ""
}

func assignedValue(st Statement, v string) (string, bool) {
	for _, a := range st.Assignments() {
		if a[0] == v {
			return a[2], true
		}
	}
	return "", false
}

func lastLaunchLine(src *Source) int {
	last := 0
	for _, st := range src.Statements() {
		if name, args, ok := st.Command(); ok && name == "tmux" && len(args) > 0 &&
			args[0] == "new-session" {
			last = st.Line
		}
	}
	return last
}

// condVarsExcept names the variables a condition expands, minus one.
func condVarsExcept(cond, skip string) []string {
	seen := map[string]bool{skip: true}
	var out []string
	for _, m := range condVarRe.FindAllStringSubmatch(cond, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

func dollarize(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "$"+n)
	}
	return out
}

// checkSessionNamesCarryPID is INV-3.
//
// It resolves which text becomes a tmux session name rather than guessing from
// the shape of a string, because the negative case has to work: a name that
// carries no `$$` at all must still be recognised AS a name. So the analysis
// runs backwards from the sink.
//
//	seed        : the `-s` argument of every `tmux new-session`.
//	closure     : a variable assigned from, or to, a name-carrying variable is
//	              itself name-carrying — which walks through the slot arrays and
//	              the view variables, and through `local sess="$1"` into the
//	              POSITIONAL parameters of alloc_slot, wherever alloc_slot is
//	              defined (slots.sh for the shared slot model, the driver
//	              itself for claudecode's private one, where the name is the
//	              SECOND argument rather than the first).
//	mint sites  : the composed literals that reach a name-carrying variable or
//	              a name-carrying argument position.
//
// Only the driver's own mint sites are reported. The libraries take part in the
// closure and own no names of their own.
func checkSessionNamesCarryPID(src *Source, libs []*Source) ([]Finding, error) {
	flow, err := newNameFlow(src, libs)
	if err != nil {
		return nil, err
	}
	mints := flow.mintSites(src)
	if len(mints) == 0 {
		return nil, fmt.Errorf("%s launches the agent under tmux but no tmux session NAME could "+
			"be traced back to the text that mints it — INV-3 graded nothing here, and a check "+
			"that cannot run must say so", src.Path)
	}
	var findings []Finding
	for _, m := range mints {
		if carriesPIDField(m.literal) {
			continue
		}
		findings = append(findings, Finding{
			Invariant: "INV-3", Path: src.Path, Line: m.line, Excerpt: m.literal,
			Detail: "this tmux session name does not carry `$$` as a `-`-delimited field. " +
				"run-cell.sh keeps no record of the names a driver chose — no staging-contract " +
				"file carries them, and the driver is backgrounded only so its PID can be " +
				"captured — so the driver PID embedded in the name is the ONLY join from a run " +
				"to the sessions it owns, and the post-run leak assertion has nothing to match " +
				"on without it. A `$$` glued to another token does not count: `-` is the " +
				"delimiter the assertion splits on, and a glued PID lets a DIFFERENT pid whose " +
				"digits merely share a prefix match instead.",
		})
	}
	return findings, nil
}

// carriesPIDField reports whether a session-name literal carries `$$` as its
// own `-`-delimited field.
//
// The split is over the literal SOURCE text, not over an expanded name, so a
// `-` inside an expansion (`${FOO:-x}`) splits too. That direction is safe:
// it can only ever break a field APART, never manufacture a bare `$$` field
// where the author did not write one, so it cannot turn a bad name into a
// passing one.
func carriesPIDField(literal string) bool {
	for _, f := range strings.Split(literal, "-") {
		if f == pidField {
			return true
		}
	}
	return false
}

type mintSite struct {
	line    int
	literal string
}

// nameFlow is the fixpoint of "what carries a tmux session name".
type nameFlow struct {
	vars  map[string]bool         // variable names
	pos   map[string]map[int]bool // function name -> name-carrying argument positions
	seeds []mintSite              // names written inline at the launch itself
	all   []*Source
}

func newNameFlow(src *Source, libs []*Source) (*nameFlow, error) {
	f := &nameFlow{vars: map[string]bool{}, pos: map[string]map[int]bool{}}
	f.all = append([]*Source{src}, libs...)
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
		name, args, ok := st.Command()
		if !ok || name != "tmux" || len(args) == 0 || args[0] != "new-session" {
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
// learned anything new.
func (f *nameFlow) propagate(src *Source) bool {
	changed := false
	for _, st := range src.Statements() {
		for _, a := range st.Assignments() {
			lhs, rhs := a[0], a[2]
			v, positional, isVar := soleVar(rhs)
			if !isVar {
				continue
			}
			switch {
			case positional > 0:
				if f.vars[lhs] && st.Func != "" && !f.posHas(st.Func, positional) {
					f.markPos(st.Func, positional)
					changed = true
				}
			default:
				if f.vars[lhs] && !f.vars[v] {
					f.vars[v] = true
					changed = true
				}
				if f.vars[v] && !f.vars[lhs] {
					f.vars[lhs] = true
					changed = true
				}
			}
		}
		name, args, ok := st.Command()
		if !ok || len(f.pos[name]) == 0 {
			continue
		}
		for n := range f.pos[name] {
			if n > len(args) {
				continue
			}
			if v, _, isVar := soleVar(args[n-1]); isVar && v != "" && !f.vars[v] {
				f.vars[v] = true
				changed = true
			}
		}
	}
	return changed
}

func (f *nameFlow) posHas(fn string, n int) bool { return f.pos[fn][n] }

func (f *nameFlow) markPos(fn string, n int) {
	if f.pos[fn] == nil {
		f.pos[fn] = map[int]bool{}
	}
	f.pos[fn][n] = true
}

// mintSites lists every composed literal in ONE file that becomes a tmux
// session name — assigned to a name-carrying variable, or passed at a
// name-carrying argument position.
func (f *nameFlow) mintSites(src *Source) []mintSite {
	out := append([]mintSite(nil), f.seeds...)
	for _, st := range src.Statements() {
		for _, a := range st.Assignments() {
			lhs, rhs := a[0], a[2]
			if !f.vars[lhs] {
				continue
			}
			if _, _, isVar := soleVar(rhs); isVar {
				continue
			}
			if lit := unquote(rhs); strings.TrimSpace(lit) != "" {
				out = append(out, mintSite{line: st.Line, literal: lit})
			}
		}
		name, args, ok := st.Command()
		if !ok {
			continue
		}
		for n := range f.pos[name] {
			if n > len(args) {
				continue
			}
			arg := args[n-1]
			if _, _, isVar := soleVar(arg); isVar {
				continue
			}
			if lit := unquote(arg); strings.TrimSpace(lit) != "" {
				out = append(out, mintSite{line: st.Line, literal: lit})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

// Adapters lists every adapter in the catalog that ships a driver, read from
// disk so an adapter onboarded tomorrow is graded without being listed here.
func Adapters(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, AgentsDir))
	if err != nil {
		return nil, fmt.Errorf("reading the adapter catalog: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, AgentsDir, e.Name(), DriverName)); err != nil {
			continue
		}
		out = append(out, e.Name())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no adapter under %s ships a %s — a broken scan and an empty "+
			"catalog must not be the same answer", filepath.Join(root, AgentsDir), DriverName)
	}
	sort.Strings(out)
	return out, nil
}

// LoadDriver reads one adapter's driver plus every shell library it can source
// — the shared drive libs and the adapter's own sibling scripts.
func LoadDriver(root, adapter string) (File, []File, error) {
	path := filepath.Join(root, AgentsDir, adapter, DriverName)
	src, err := os.ReadFile(path) // #nosec G304 -- built from the repo root and a scanned adapter slug
	if err != nil {
		return File{}, nil, fmt.Errorf("reading %s's driver: %w", adapter, err)
	}
	driver := File{Path: path, Src: string(src)}

	var libs []File
	for _, dir := range []string{filepath.Join(root, libDir), filepath.Join(root, AgentsDir, adapter)} {
		found, err := readShellDir(dir, path)
		if err != nil {
			return File{}, nil, err
		}
		libs = append(libs, found...)
	}
	return driver, libs, nil
}

// readShellDir reads every non-test .sh file in dir except skip.
func readShellDir(dir, skip string) ([]File, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.sh"))
	if err != nil {
		return nil, err
	}
	var out []File
	for _, m := range matches {
		if m == skip || strings.HasSuffix(m, "_test.sh") {
			continue
		}
		b, err := os.ReadFile(m) // #nosec G304 -- a path returned by Glob over a repo directory
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", m, err)
		}
		out = append(out, File{Path: m, Src: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
