// Package costreport is #1572's reporter: the machinery that turns a measured
// figure in a doc comment from something REMEMBERED into something PRODUCIBLE.
//
// AGENTS.md's "Replay's measured figures" states the rule — a number which
// documents behaviour but is not produced by it drifts silently, and is then
// quoted with full confidence — and two trees in core/ had reached the stage
// where the drift was visible in ONE FILE: bundleIDMemo (processlifecycle/
// osutil_darwin.go) describes the same `plutil` exec at 2.2ms (#1524) and 9.7ms
// (#1544). PR #1569 stated the discrepancy and marked both as snapshots, which
// is the right thing to tell a reader and is not a mechanism.
//
// WHAT IS DELIBERATELY NOT BUILT, because it would be flaky by construction: an
// equality gate. Replay's census can compare a committed literal against a fresh
// measurement because its subject is a fixed catalog on disk. These figures are
// a property of a MACHINE under a LOAD, so a threshold would fail for reasons
// unrelated to the code and would then be widened until it protected nothing.
// #1572 says so explicitly: "It should NOT gate CI."
//
// So what IS enforced is the weaker property that closes the gap: a figure names
// the command that regenerates it (anchors.go), and the generator REFUSES rather
// than printing zeros when it cannot measure (this file).
//
// ONE package rather than a copy per caller. The two callers' first draft was
// two copies of this reporter, whose own header argued the duplication was
// deliberate; AGENTS.md's most on-point precedent says otherwise — "The
// shell-lib suite runner ... Before #1639 those were two copies of the same loop
// that disagreed about the only thing that matters." A change filed ABOUT
// figures drifting should not ship its mechanism in two copies that can drift.
// It is a non-test package for the reason core/internal/contracttesting is: test
// files in different packages cannot share test-only code any other way.
package costreport

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"irrlicht/core/domain/stats"
)

// Marker is the token every refused row carries. It is one token so that a
// reader scanning a pasted block can tell a complete one from an incomplete one
// without counting rows, and so that Render's footer can name the shortfall.
const Marker = "UNMEASURED"

// Row is one line of a generated block: either a measured figure, or a NAMED
// REASON the figure could not be produced. There is no third state, and no way
// to spell a measured row that carries no samples — which is the whole point of
// the type. #1572's central ask is that a generator which cannot find `plutil`
// must SAY SO rather than print "0.0 ms", and this repo's own history is why: a
// check whose inability to look is indistinguishable from a clean result is the
// most expensive way to fail (AGENTS.md, "A verification mechanism must fail
// loudly when it cannot run").
type Row struct {
	// name is the figure's label — for a probe the probeKind, so the report's
	// rows and probecount.go's diagnostics rows carry the same tokens.
	name string
	// samplesMS holds one per-call duration in milliseconds. Empty iff refusal
	// is set.
	samplesMS []float64
	// refusal is why this row has no figure. Empty iff samplesMS is not.
	refusal string
	// note carries the conditions of this particular row (what was probed),
	// which is the half of a measurement a reader needs to know whether it
	// still applies.
	note string
}

// Measured builds a row from samples, DEMOTING it to a refusal when the samples
// cannot support a figure. Both demotions have been real failure modes:
//
//   - Zero samples. The probe was planned, the loop ran, and nothing was
//     collected — a missing binary, a subject that vanished, an environment
//     variable nobody set. stats.Median answers (0, false) for an empty slice,
//     so a caller that ignored the second result would print 0.0ms, and 0.0ms is
//     the one figure that reads as "extremely fast" rather than "I could not
//     look".
//   - A non-positive sample. No child process costs zero, so a 0 or negative
//     duration means the MEASUREMENT broke. That is the guard replay's census
//     draws before its equality check, for the same reason: "paste the measured
//     literal" is the wrong advice when the measurement itself is what broke.
func Measured(name string, samplesMS []float64, note string) Row {
	if len(samplesMS) == 0 {
		return Refused(name, "collected 0 samples")
	}
	for i, ms := range samplesMS {
		if ms <= 0 {
			return Refused(name, fmt.Sprintf(
				"sample %d of %d measured %.4fms — no child process costs that, so the measurement broke rather than the probe being fast",
				i+1, len(samplesMS), ms))
		}
	}
	out := make([]float64, len(samplesMS))
	copy(out, samplesMS)
	return Row{name: name, samplesMS: out, note: note}
}

// Refused builds a row that carries a reason instead of a figure. reason is
// required: a refusal that does not say why is the silence this package exists
// to remove, so an empty one is replaced by a message naming the omission rather
// than rendered as a blank.
func Refused(name, reason string) Row {
	if strings.TrimSpace(reason) == "" {
		reason = "refused with no reason given — a refusal that cannot be read is not a refusal"
	}
	return Row{name: name, refusal: reason}
}

// WithRate attaches a derived per-unit figure — bytes of output over the
// population the command walked, which is what gitMaxOutput is argued from —
// and refuses THE RATE, by name, when it cannot be expressed.
//
// The rate is a separate refusal from the row because the two facts are
// separable: a `git log --grep` over a history with no matching commit walked
// every commit, so its DURATION is a real measurement, while its bytes-per-commit
// does not exist. Refusing the whole row would throw away the figure that was
// obtained; printing a zero is the defect this package is about.
//
// This is #1572's own defect, caught in its own generator and reproduced live
// before the fix: over this repo, `log --all --grep <no match>` printed
// "0 bytes out = 0/commit" and `rev-parse HEAD` printed "41 bytes out =
// 0/commit" — the second from INTEGER DIVISION, which a guard keyed on
// `bytes == 0` does not catch and which is the more dangerous of the two,
// because 41 is a real measurement rendered as nothing. rateOf is the one
// predicate that decides both.
//
// over names the population IN FULL ("commits from HEAD", "commits across all
// refs") rather than as a bare noun, because which population a byte count was
// divided by is exactly what #1553 got wrong: it divided one `%B` walk's bytes
// by the all-refs count for a command that walks HEAD, and every extrapolation
// from that figure was ~2.3x low.
func (r Row) WithRate(bytes, population int, over string) Row {
	if r.refusal != "" {
		return r // nothing to attach a rate to; the row already says why.
	}
	head := fmt.Sprintf("%s bytes / %s %s", byteCount(bytes), byteCount(population), over)
	rate, ok := rateOf(bytes, population)
	if !ok {
		r.note = joinNote(r.note, head+": NO RATE — "+rateRefusal(bytes, population))
		return r
	}
	r.note = joinNote(r.note, head+" = "+rate+" bytes each")
	return r
}

// rateOf renders bytes-per-population, or reports that it cannot.
//
// Rendered as a FLOAT rather than as an integer quotient, which is the whole
// fix: `41/3209` is 0 in Go and 0.0128 to a reader. The precision is chosen by
// magnitude so the common case stays readable — gitMaxOutput's own figure is
// "1885", not "1.88e+03" — while a ratio below 1 keeps enough significant digits
// to be a number at all. The zeroShaped guard then re-reads what WOULD BE
// PRINTED, so the property is "no figure this package emits reads as zero"
// rather than "this particular format verb happens not to produce one".
func rateOf(bytes, population int) (string, bool) {
	if population <= 0 || bytes <= 0 {
		return "", false
	}
	v := float64(bytes) / float64(population)
	var s string
	switch {
	case v >= 100:
		s = strconv.FormatFloat(v, 'f', 0, 64)
	case v >= 1:
		s = strconv.FormatFloat(v, 'f', 2, 64)
	default:
		s = strconv.FormatFloat(v, 'g', 3, 64)
	}
	if zeroShaped(s) {
		return "", false
	}
	return s, true
}

// zeroShaped reports whether a rendered figure reads as zero — the shape a
// reader carries off as "this costs nothing" when it means "this could not be
// computed". Unparseable counts as zero-shaped: a validator that cannot read its
// input checks MORE, never less (AGENTS.md).
func zeroShaped(s string) bool {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err != nil || v == 0
}

// rateRefusal names which of the two ways the rate failed, because "no output at
// all" and "a population nobody counted" point at different things to go and fix.
func rateRefusal(bytes, population int) string {
	if bytes <= 0 {
		return "the command produced no output, so the figure above is the cost of the WALK and there is no rate"
	}
	if population <= 0 {
		return "the population could not be counted, so there is nothing to divide by"
	}
	// Unreachable while rateOf refuses only on those two, and spelled out rather
	// than left as an empty clause: this is the line a future rendering change
	// would have to extend, and a refusal that says nothing is the silence this
	// package exists to remove.
	return "the figure it would print reads as zero, which is an inability to compute rather than a measurement"
}

// Render is one line of the block. A refused row renders its reason and NO
// figure — asserted by the corpus, because "renders the reason" and "renders the
// reason INSTEAD of a number" are different claims and only the second is the
// property.
//
// nameWidth pads the label column. It is the caller's because the two trees'
// labels differ in length by more than a factor of two, and a column sized for
// one of them is unreadable for the other.
func (r Row) Render(nameWidth int) string {
	if r.refusal != "" {
		return fmt.Sprintf("//\t%-*s %s — %s", nameWidth, r.name, Marker, r.refusal)
	}
	med, okMed := stats.Median(r.samplesMS)
	p99, okP99 := stats.Percentile(r.samplesMS, 0.99)
	if !okMed || !okP99 {
		// Unreachable while Measured is the only constructor that sets
		// samplesMS, and spelled out anyway rather than left to a zero value:
		// this is the exact line where a future refactor would reintroduce
		// "0.0ms" as the output of a computation that failed.
		return fmt.Sprintf("//\t%-*s %s — %d sample(s) yielded no percentile",
			nameWidth, r.name, Marker, len(r.samplesMS))
	}
	// min is carried beside the two percentiles because it is the figure that
	// travels between machines. #1524 and #1544 disagreed 4x about the same
	// `plutil` exec, and a median is a statement about the load as much as about
	// the exec — the floor is what a second machine can be compared against.
	lo := r.samplesMS[0]
	for _, ms := range r.samplesMS {
		if ms < lo {
			lo = ms
		}
	}
	line := fmt.Sprintf("//\t%-*s median %.1fms  p99 %.1fms  min %.1fms  n=%d",
		nameWidth, r.name, med, p99, lo, len(r.samplesMS))
	if r.note != "" {
		line += "  (" + r.note + ")"
	}
	return line
}

// Render renders the pasteable block, and REFUSES — non-nil error — when the run
// cannot be read as a measurement at all.
//
// The two refusals are deliberately not "some row failed": a machine without
// kitty legitimately cannot measure `kitten.window`, and failing the whole
// generator for that would make it unrunnable anywhere and therefore unrun. So a
// partial block is a success that SAYS it is partial (the footer), while a block
// with nothing measured in it is a failure — because that is the shape where the
// operator, not the machine, is what went wrong: a wrong build tag, a wrong
// directory, a stubbed-out plan.
func Render(title string, conditions []string, rows []Row) (string, error) {
	if len(rows) == 0 {
		return "", fmt.Errorf("cost report %q has no rows at all — the plan produced nothing, which is a broken generator rather than a fast machine", title)
	}
	width := 0
	var refused []string
	for _, r := range rows {
		if len(r.name) > width {
			width = len(r.name)
		}
		if r.refusal != "" {
			refused = append(refused, r.name)
		}
	}
	if len(refused) == len(rows) {
		return "", fmt.Errorf("cost report %q measured NOTHING: all %d row(s) refused (%s) — report this as an inability to look, never as a figure",
			title, len(rows), strings.Join(refused, ", "))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "// %s\n//\n", title)
	for _, c := range conditions {
		fmt.Fprintf(&b, "// %s\n", c)
	}
	if len(conditions) > 0 {
		b.WriteString("//\n")
	}
	for _, r := range rows {
		b.WriteString(r.Render(width) + "\n")
	}
	if len(refused) > 0 {
		sorted := append([]string(nil), refused...)
		sort.Strings(sorted)
		fmt.Fprintf(&b, "//\n// INCOMPLETE: %d of %d row(s) %s on this machine — %s\n",
			len(refused), len(rows), Marker, strings.Join(sorted, ", "))
	}
	return b.String(), nil
}

func joinNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + "; " + extra
}

// byteCount groups thousands, because the whole value of a generated block is
// that it can be PASTED into the doc comment it refreshes, and those quote their
// figures that way ("measured 2,611,982 bytes on this repo", gitMaxOutput).
func byteCount(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	joined := strings.Join(out, ",")
	if neg {
		return "-" + joined
	}
	return joined
}
