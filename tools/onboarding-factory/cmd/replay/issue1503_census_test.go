package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1503: the replay tree's headline figures — how many committed
// recordings reproduce NOTHING, how many FABRICATE, and how many DIVERGE — are
// quoted in doc comments, in tools/replay-fixtures.sh, and in every replay PR
// body, and until this file none of them was produced by anything. They were
// typed by hand and went stale twice in two PRs: #1478 had to correct copies in
// five places, and its own table was one low on every row because "divergent"
// had no single definition to count with.
//
// #1480 had already solved this shape once, for a different figure:
// knownFirstTransitionDrift is machine-generated, the test prints the exact map
// literal to paste, and it stayed correct across a change that reshaped the
// distribution it describes. This file extends that idiom to the counts.
//
// The general rule it instances is the quieter sibling of AGENTS.md's "a
// verification mechanism must fail loudly when it cannot run": a number that
// DOCUMENTS behaviour but is not PRODUCED by it drifts silently, and is then
// quoted with full confidence.

// transcriptName is the transcript filename forEachSidecarRecording pairs a
// sidecar with. It is a constant beside eventsSidecarName because the census
// and the walk it audits must apply the SAME pairing rule — the whole meaning
// of UnpairedSidecars is the difference between this rule and the wider one
// tools/replay-fixtures.sh uses, and two bare string literals is how that
// difference stops being deliberate.
const transcriptName = "transcript.jsonl"

// catalogCensus is the census of the committed catalog's populations, plus the
// denominator they are quoted against ("N of 309").
//
// The denominator is a field rather than a constant because it is quoted just
// as often and rots the same way: it moves whenever a recording is promoted or
// retired, and a stale one silently rescales every ratio computed from it.
type catalogCensus struct {
	// Recordings is how many sidecar-driven recordings the walk reached — the
	// denominator in "N of 309".
	Recordings int
	// Zero counts extendedCheck.ReproducesNothing: the daemon logged
	// transitions, the replay reproduced none, so the golden asserts nothing.
	Zero int
	// Fabricated counts extendedCheck.Fabricates: the daemon logged nothing and
	// the replay invented transitions, so the golden asserts something false.
	Fabricated int
	// Divergent counts extendedCheck.Diverges: the replay disagreed about which
	// transitions happened or in what order. This is the figure #1503 was filed
	// about; see Diverges for the two near-miss spellings and which one cost
	// #1478 a one-low table.
	Divergent int
	// DivergentByCountsAndKinds counts the plausible NEAR-MISS spelling — "the
	// transition counts differ, or the kind sets do" — over the same
	// population.
	//
	// It is a census figure because the gap between it and Divergent IS #1503:
	// that spelling reads as a restatement of Diverges and is not one, and a
	// whole comparison table was computed with it one low. Carried here, the
	// claim "they differ by exactly one recording" is re-derived on every run
	// rather than being a sentence that was true once. The recording making up
	// the difference is pinned by name as divergenceWitness.
	DivergentByCountsAndKinds int
	// UnpairedSidecars counts recorded sidecars the walk cannot PAIR: it
	// requires a sibling transcriptName, and these recordings carry a
	// transcript.md instead — today, every aider recording.
	//
	// It is in the census because leaving it out is how the same headline came
	// to have two values: tools/replay-fixtures.sh walks `-name
	// transcript.jsonl -o -name transcript.md`, so it replays these too and
	// reports a higher divergence figure. Measured, not asserted — the sweep
	// reported 142 against this census's 140 while this field was being added,
	// and diffing the two sets named the two extra recordings exactly (both
	// aider/4-2_multiple-agents-same-workspace).
	//
	// Also a standing coverage note: #1342's known-lists and #1480's ratchets
	// have never been evaluated against any of these recordings, so
	// "catalog-wide" is true of the catalog this walk can see rather than of
	// the catalog.
	//
	// Why widening the pairing is deferred is a SCOPE call and is stated as
	// one, because the natural blast-radius reason is measurably false:
	// pairing transcript.md when no transcriptName exists adds exactly 2
	// gradeable recordings, both merely divergent, and moves
	// knownZeroTransition, knownFabricated and knownFirstTransitionDrift by
	// nothing at all. It would make this census agree with the sweep exactly,
	// at the cost of re-replaying the aider recordings that then produce no
	// extended check.
	UnpairedSidecars int
	// PairedButUngraded counts recordings the walk DID pair and still could not
	// grade: resolveInputPaths declined the sidecar, or the replay produced no
	// extended check at all — chiefly the process-owned-store adapters, which
	// record no fswatcher fires and legitimately degrade to transcript-only.
	//
	// Derived as (all recorded sidecars) - UnpairedSidecars - Recordings, so
	// the three account for every sidecar on disk. That identity holds by
	// construction and is therefore not a guard; the VALUE is, because it moves
	// the moment the walk's skip conditions change and the committed literal
	// then stops matching. It exists because UnpairedSidecars alone was
	// described as everything the gates are blind to, and it is only part of
	// it — this is the rest.
	PairedButUngraded int
}

// censusOfTheCommittedCatalog is the census as measured against the catalog in
// this commit.
//
// It is MACHINE-GENERATED and must not be edited by hand:
// TestCatalogCensusMatchesTheCommittedFigures prints this exact literal on
// every run, and fails when it stops describing the catalog. Regenerate with
//
//	go test ./cmd/replay/ -run TestCatalogCensusMatchesTheCommittedFigures -v -count=1
//
// and paste the printed block over this one, with a reason per moved figure in
// the PR body — the same contract knownFirstTransitionDrift carries.
//
// A figure moving is not by itself a defect: Zero falling is #1342's and
// #1478's whole purpose, and Divergent falling is a replay-fidelity win. The
// point is that it cannot move UNNOTICED, and that no doc comment anywhere
// carries a second, hand-typed copy of it.
var censusOfTheCommittedCatalog = catalogCensus{
	Recordings:                309,
	Zero:                      1,
	Fabricated:                1,
	Divergent:                 140,
	DivergentByCountsAndKinds: 139,
	UnpairedSidecars:          31,
	PairedButUngraded:         56,
}

// literal renders the census as the Go source to paste over the declaration
// above. Rendering it from the measured value — rather than reporting the
// numbers in prose and leaving a human to transcribe them — is the entire
// mechanism: a transcription step is where #1478's error entered.
func (c catalogCensus) literal() string {
	var b strings.Builder
	b.WriteString("var censusOfTheCommittedCatalog = catalogCensus{\n")
	// gofmt aligns a struct literal's values one space past the longest
	// "Name:", so the width is derived rather than hardcoded. A fixed width
	// would silently stop being gofmt-clean the moment a field is added, and
	// the printed block would then no longer be what the file must contain —
	// which is the one property the whole idiom rests on.
	width := 0
	for _, f := range c.fields() {
		if n := len(f.name) + 1; n > width {
			width = n
		}
	}
	for _, f := range c.fields() {
		fmt.Fprintf(&b, "\t%-*s %d,\n", width, f.name+":", f.value)
	}
	b.WriteString("}\n")
	return b.String()
}

// censusField pairs a figure with the name it is declared and reported under,
// so the literal, the diff and the failure message cannot drift apart in naming
// or in order.
type censusField struct {
	name  string
	value int
}

// fields is the one enumeration of the census. Every renderer below ranges over
// it, for the reason driftPercentiles is ranged over by both its computation
// and its rendering: a figure added to one side only either never prints or
// prints a zero from nowhere.
func (c catalogCensus) fields() []censusField {
	return []censusField{
		{"Recordings", c.Recordings},
		{"Zero", c.Zero},
		{"Fabricated", c.Fabricated},
		{"Divergent", c.Divergent},
		{"DivergentByCountsAndKinds", c.DivergentByCountsAndKinds},
		{"UnpairedSidecars", c.UnpairedSidecars},
		{"PairedButUngraded", c.PairedButUngraded},
	}
}

// diff names every figure on which two censuses disagree, in declaration order,
// or returns nil when they agree exactly.
//
// It names the figures rather than reporting a bare "stale" boolean because
// WHICH figure moved is the whole diagnosis: Zero falling by one is a
// zero-transition recording rescued, Divergent rising by one is a
// replay-fidelity regression, and Recordings moving at all means the catalog or
// the walk changed underneath both.
func (c catalogCensus) diff(other catalogCensus) []string {
	var moved []string
	mine, theirs := c.fields(), other.fields()
	for i := range mine {
		if mine[i].value == theirs[i].value {
			continue
		}
		moved = append(moved, fmt.Sprintf("%s: committed %d, measured %d",
			mine[i].name, mine[i].value, theirs[i].value))
	}
	return moved
}

// TestCatalogCensusMatchesTheCommittedFigures is #1503's deliverable: it walks
// the catalog, counts each population with the named predicates, prints the
// pasteable literal whether or not anything moved, and fails when the committed
// literal has gone stale.
//
// It runs its own walk rather than folding into
// TestSidecarReplayAgreesWithTheDaemonsOwnLog, matching the trade
// forEachSidecarRecording's doc comment already records for #1480's gate: the
// re-replay costs a couple of seconds and buys full independence between gates.
// What is NOT duplicated is the classification — every count comes from the
// predicates on extendedCheck, so the two gates cannot disagree about what they
// are counting even though they count separately.
func TestCatalogCensusMatchesTheCommittedFigures(t *testing.T) {
	var measured catalogCensus
	// main.go's former exit-code spelling, kept here as a MEASUREMENT rather
	// than a claim. Diverges' doc comment argues the extra disjuncts are
	// structurally unreachable; this counts them over the whole catalog so the
	// refactor that collapsed them is evidenced instead of reasoned.
	var wideSpelling int
	// Both directions. Collecting only one of them reports "disagree on 0
	// recording(s)" whenever the other spelling is a strict SUBSET, which is
	// the commonest way for two predicates to differ and reads as a message bug
	// rather than as the finding it is.
	var disagreed []string

	measured.Recordings = forEachSidecarRecording(t, func(name string, ec *extendedCheck) {
		if ec.ReproducesNothing() {
			measured.Zero++
		}
		if ec.Fabricates() {
			measured.Fabricated++
		}
		if ec.Diverges() {
			measured.Divergent++
		}
		if ec.RecordedCount != ec.ReplayedCount || len(ec.MissingKinds) > 0 || len(ec.ExtraKinds) > 0 {
			measured.DivergentByCountsAndKinds++
		}
		wide := ec.Diverges() || len(ec.MissingKinds) > 0 || len(ec.ExtraKinds) > 0
		if wide {
			wideSpelling++
		}
		if wide != ec.Diverges() {
			disagreed = append(disagreed, fmt.Sprintf("%s (Diverges=%t, ordered-or-kinds=%t)",
				name, ec.Diverges(), wide))
		}
	})

	sidecars, unpaired := countSidecars(t)
	measured.UnpairedSidecars = unpaired
	measured.PairedButUngraded = sidecars - unpaired - measured.Recordings

	t.Logf("#1503 catalog census, measured over %d sidecar-driven recordings "+
		"(of %d recorded sidecars on disk):\n\n%s",
		measured.Recordings, sidecars, measured.literal())

	if wideSpelling != measured.Divergent || len(disagreed) > 0 {
		t.Errorf("the two divergence spellings disagree (Diverges %d, ordered-or-kinds %d) on "+
			"%d recording(s):\n  %s\nmain.go's exit code was collapsed onto Diverges on the "+
			"grounds that they cannot differ, so this is that grounds failing, not a cosmetic "+
			"difference",
			measured.Divergent, wideSpelling, len(disagreed), firstFew(disagreed, 5))
	}

	// Computed BEFORE the guards below, so their messages can name what moved.
	// A guard that fatals on a shrinking population while suppressing the
	// per-figure diff invites the developer to confirm the shrink is genuine
	// and bulk-paste the literal — blessing any other regression that landed in
	// the same window, which is exactly the transcription step this file exists
	// to remove.
	moved := censusOfTheCommittedCatalog.diff(measured)

	// Fail-loudly guards, ahead of the equality check, because the equality
	// check's advice is "paste the measured literal" — which is the wrong move
	// when the measurement itself broke. Both are ways for the census to come
	// back smaller while every count in it is honest about what it saw.
	if measured.Recordings < censusOfTheCommittedCatalog.Recordings {
		t.Fatalf("the walk reached %d sidecar-driven recordings but the committed census was "+
			"taken over %d — the population SHRANK. Do not paste the new literal until you "+
			"know a recording was genuinely retired: a walk that stopped pairing recordings "+
			"with their sidecars reports exactly this, with every other figure quietly scaled "+
			"down to match. Everything that moved: %s",
			measured.Recordings, censusOfTheCommittedCatalog.Recordings, strings.Join(moved, "; "))
	}
	if measured.Divergent == 0 && censusOfTheCommittedCatalog.Divergent > 0 {
		t.Fatalf("no recording diverges at all, where the committed census counts %d — the "+
			"comparison has stopped comparing. This is the shape #1326 and #1342 were both "+
			"about, and it reads as an improvement from the inside. Everything that moved: %s",
			censusOfTheCommittedCatalog.Divergent, strings.Join(moved, "; "))
	}

	if len(moved) == 0 {
		return
	}
	t.Errorf("the committed catalog census is STALE — %s.\n\n"+
		"These figures are quoted in doc comments and in every replay PR body, so a stale "+
		"one is restated downstream with full confidence; that is the failure #1503 exists "+
		"to close. Paste this over censusOfTheCommittedCatalog, with a reason per moved "+
		"figure in the PR body:\n\n%s",
		strings.Join(moved, "; "), measured.literal())
}

// countSidecars returns how many recorded sidecars exist under the catalog and
// how many of them the walk cannot pair, because no sibling transcriptName sits
// beside them.
//
// It stats rather than replays, so it costs milliseconds and, more to the
// point, it cannot be fooled by the very pairing it is measuring: asking the
// walk how much the walk missed would always answer zero.
func countSidecars(t *testing.T) (total, unpaired int) {
	t.Helper()
	root := replaydataRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != eventsSidecarName {
			return nil
		}
		total++
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), transcriptName)); statErr != nil {
			unpaired++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk replaydata/agents: %v", err)
	}
	// The vacuity guard this file's own subject matter demands: a walk that
	// found no sidecar at all reports zero unpaired ones, which reads as
	// "nothing is being missed" — the most reassuring possible spelling of a
	// broken measurement.
	if total == 0 {
		t.Fatal("no events.jsonl sidecar found under replaydata/agents — the accounting is vacuous")
	}
	return total, unpaired
}

// firstFew renders at most n items with a count of the remainder. A predicate
// disagreement can involve a third of the catalog, and a failure that prints a
// hundred paths buries the one fact the reader needs; five names are enough to
// see the shape and the count carries the scale.
func firstFew(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, "\n  ")
	}
	return fmt.Sprintf("%s\n  ... and %d more", strings.Join(items[:n], "\n  "), len(items)-n)
}

// divergenceWitness is the ONE recording in the committed catalog whose replay
// diverges while its transition COUNTS and its kind SETS both agree with the
// daemon's: four recorded, four replayed, the same two kinds on both sides, and
// pair 0 swapped (daemon working→ready, replay ready→working).
//
// It is the recording that separates Diverges from the most plausible near-miss
// spelling of it, so it is pinned by name — the trap #1503 is about is a
// counting rule that looks like a restatement and silently omits one recording.
const divergenceWitness = "copilot/scenarios/1-4_session-resume/recordings/2026-08-05-19-02-01_irrlichd-0.5.9+4b58365/transcript.jsonl"

// TestDivergesCatchesTheOrderSwapThatCountsAndKindsMiss is a LOCK, not a defect
// test: it passes on main by construction, because Diverges is already the
// established definition. What it pins is that the near-miss remains a near
// MISS — that the catalog still contains a witness distinguishing the two, so
// the distinction cannot quietly become academic and then be "simplified" away
// by a future reader who cannot see why the spelling matters.
//
// The SIZE of the difference is not asserted here; it is
// censusOfTheCommittedCatalog's Divergent minus DivergentByCountsAndKinds, both
// machine-generated. This test names the recording that difference is made of.
func TestDivergesCatchesTheOrderSwapThatCountsAndKindsMiss(t *testing.T) {
	ec := extendedCheckOf(t, divergenceWitness)

	// The vacuity guards. Each of these failing means the witness has been
	// replaced by a re-recording and no longer witnesses anything, so the
	// assertion below would still pass while proving nothing.
	if ec.RecordedCount != ec.ReplayedCount {
		t.Fatalf("%s: daemon recorded %d transitions and replay reproduced %d — the witness is "+
			"a witness BECAUSE the counts agree; it has been re-recorded and this test no "+
			"longer means what it says", divergenceWitness, ec.RecordedCount, ec.ReplayedCount)
	}
	if len(ec.MissingKinds) > 0 || len(ec.ExtraKinds) > 0 {
		t.Fatalf("%s: kind sets no longer agree (missing %v, extra %v) — the witness is a "+
			"witness BECAUSE they do", divergenceWitness, ec.MissingKinds, ec.ExtraKinds)
	}

	if !ec.Diverges() {
		t.Fatalf("%s no longer diverges — the catalog has lost its only recording where an "+
			"ORDER-only divergence is visible, so nothing distinguishes Diverges from the "+
			"counts-and-kinds spelling any more. Find a replacement witness rather than "+
			"deleting this test", divergenceWitness)
	}
	t.Logf("%s: %d recorded / %d replayed, kinds agree, %d ordered mismatch(es) — %+v",
		divergenceWitness, ec.RecordedCount, ec.ReplayedCount, len(ec.OrderedMismatches),
		ec.OrderedMismatches)
}

// TestCensusDiffNamesEveryStaleShape is the mutation evidence #1503's
// acceptance criterion asks for, committed as a corpus rather than described in
// the PR body: a generator that nobody has watched fail has bought nothing.
//
// Each row is a deliberately-stale committed literal paired with the verdict
// the comparison MUST return for it. The want:nil row is the vacuity guard —
// without it, a diff that reported every census as stale would satisfy every
// other row here and be indistinguishable from one that works.
//
// This is the unit half. The catalog half — the real gate going red against a
// hand-staled censusOfTheCommittedCatalog — is pasted verbatim in the PR body,
// because it cannot be committed without leaving the suite permanently red.
func TestCensusDiffNamesEveryStaleShape(t *testing.T) {
	// The rows below perturb the committed census, and read it from the
	// declaration rather than restating it — a fixture labelled "as of this
	// commit" is the same hand-typed copy this file exists to remove, one
	// layer down.
	current := censusOfTheCommittedCatalog
	with := func(f func(*catalogCensus)) catalogCensus {
		c := current
		f(&c)
		return c
	}

	for _, tc := range []struct {
		name      string
		committed catalogCensus
		want      []string
	}{
		{
			name:      "a census that still describes the catalog is not stale",
			committed: current,
			want:      nil,
		},
		{
			// #1478's exact error, reproduced as data: a divergence count one
			// low, internally consistent, and invisible without re-measuring.
			name:      "divergent one low — #1478's error",
			committed: with(func(c *catalogCensus) { c.Divergent-- }),
			want:      []string{"Divergent: committed 139, measured 140"},
		},
		{
			// The other direction. A count that only ever fails when it is too
			// LOW blesses a regression that raises it.
			name:      "divergent one high — a fidelity regression left uncounted",
			committed: with(func(c *catalogCensus) { c.Divergent++ }),
			want:      []string{"Divergent: committed 141, measured 140"},
		},
		{
			// The near-miss converging on Diverges means the catalog lost its
			// only order-only witness — a coverage loss that looks like
			// agreement.
			name:      "the near-miss spelling stopped being a near miss",
			committed: with(func(c *catalogCensus) { c.DivergentByCountsAndKinds = 140 }),
			want:      []string{"DivergentByCountsAndKinds: committed 140, measured 139"},
		},
		{
			// The shape #1342 and #1478 both moved: a zero-transition recording
			// rescued, with the doc comments still claiming the old figure.
			name:      "zero stale at its pre-#1478 value",
			committed: with(func(c *catalogCensus) { c.Zero = 4 }),
			want:      []string{"Zero: committed 4, measured 1"},
		},
		{
			name:      "fabricated stale — the count that must never rise unnoticed",
			committed: with(func(c *catalogCensus) { c.Fabricated = 3 }),
			want:      []string{"Fabricated: committed 3, measured 1"},
		},
		{
			// The gap between this census and tools/replay-fixtures.sh's own
			// divergence figure is made of exactly this population, so a stale
			// value here is a stale explanation of a live discrepancy.
			name:      "the unpaired-sidecar population moved",
			committed: with(func(c *catalogCensus) { c.UnpairedSidecars = 29 }),
			want:      []string{"UnpairedSidecars: committed 29, measured 31"},
		},
		{
			// Moves when the walk's skip conditions change — a store adapter
			// gaining an fswatcher trace, or a pairing rule quietly widening.
			name:      "the ungraded population moved",
			committed: with(func(c *catalogCensus) { c.PairedButUngraded = 54 }),
			want:      []string{"PairedButUngraded: committed 54, measured 56"},
		},
		{
			// The denominator rots on its own schedule: every promotion or
			// retirement moves it, and nothing else in the census need change.
			name:      "the denominator alone went stale",
			committed: with(func(c *catalogCensus) { c.Recordings = 300 }),
			want:      []string{"Recordings: committed 300, measured 309"},
		},
		{
			// Everything at once, in declaration order — a census pasted from a
			// different branch names every figure rather than the first one.
			name: "every figure moved, and every one is named",
			committed: with(func(c *catalogCensus) {
				*c = catalogCensus{
					Recordings: 300, Zero: 4, Fabricated: 3, Divergent: 145,
					DivergentByCountsAndKinds: 144, UnpairedSidecars: 0, PairedButUngraded: 0,
				}
			}),
			want: []string{
				"Recordings: committed 300, measured 309",
				"Zero: committed 4, measured 1",
				"Fabricated: committed 3, measured 1",
				"Divergent: committed 145, measured 140",
				"DivergentByCountsAndKinds: committed 144, measured 139",
				"UnpairedSidecars: committed 0, measured 31",
				"PairedButUngraded: committed 0, measured 56",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.committed.diff(current)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("diff() = %v, want %v", got, tc.want)
			}
			// The literal is what a developer pastes in response, so a row that
			// reports staleness must also render the CORRECTED figures. A
			// generator whose message is right and whose literal is stale is
			// the same defect one layer down.
			if len(tc.want) == 0 {
				return
			}
			lit := current.literal()
			for _, f := range current.fields() {
				if !strings.Contains(lit, fmt.Sprintf("%s:", f.name)) {
					t.Errorf("literal() omits the %s field entirely:\n%s", f.name, lit)
				}
				if !strings.Contains(lit, fmt.Sprintf(" %d,\n", f.value)) {
					t.Errorf("literal() does not carry the measured %s value %d:\n%s",
						f.name, f.value, lit)
				}
			}
		})
	}
}

// TestCensusLiteralIsValidPasteableSource pins the property that makes the whole
// idiom work: the printed block is the declaration's own source text, so pasting
// it is a mechanical substitution rather than a transcription. A literal that
// merely REPORTS the numbers puts a human retyping step back in the exact place
// #1478's error entered.
func TestCensusLiteralIsValidPasteableSource(t *testing.T) {
	lit := censusOfTheCommittedCatalog.literal()
	const wantPrefix = "var censusOfTheCommittedCatalog = catalogCensus{\n"
	if !strings.HasPrefix(lit, wantPrefix) {
		t.Errorf("literal() does not open with the declaration it replaces:\n%s", lit)
	}
	if !strings.HasSuffix(lit, "}\n") {
		t.Errorf("literal() is not a closed composite literal:\n%s", lit)
	}
	// One line per field plus the two braces. Without this a field added to
	// fields() and forgotten in the expectations below would still satisfy
	// every Contains check, and the pinned alignment would silently describe a
	// literal the file no longer contains.
	if got, want := strings.Count(lit, "\n"), len(censusOfTheCommittedCatalog.fields())+2; got != want {
		t.Errorf("literal() has %d lines, want %d (one per field plus both braces):\n%s",
			got, want, lit)
	}
	// The alignment is pinned verbatim because gofmt is what the pasted block
	// must survive: a literal that is correct but misaligned gets reformatted
	// on the next gofmt run and stops being byte-identical to what the test
	// prints, which is the property the idiom rests on.
	for _, want := range []string{
		"\tRecordings:                309,\n",
		"\tZero:                      1,\n",
		"\tFabricated:                1,\n",
		"\tDivergent:                 140,\n",
		"\tDivergentByCountsAndKinds: 139,\n",
		"\tUnpairedSidecars:          31,\n",
		"\tPairedButUngraded:         56,\n",
	} {
		if !strings.Contains(lit, want) {
			t.Errorf("literal() is missing the gofmt-aligned line %q:\n%s", want, lit)
		}
	}
}
