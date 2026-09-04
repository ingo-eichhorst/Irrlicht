package main

import (
	"fmt"
	"go/format"
	"io/fs"
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

// catalogCensus is the census of the committed catalog's populations, plus the
// denominator they are quoted against ("N of <Recordings>").
//
// The denominator is a field rather than a constant because it is quoted just
// as often and rots the same way: it moves whenever a recording is promoted or
// retired, and a stale one silently rescales every ratio computed from it.
type catalogCensus struct {
	// Recordings is how many sidecar-driven recordings the walk reached — the
	// denominator every population here is quoted against.
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
	// UnpairedSidecars counts recorded sidecars the walk cannot PAIR at all:
	// no name in transcriptNames sits beside them. It is 0, and the walk is
	// therefore not known to be blind to any recording on disk.
	//
	// A zero figure is kept in the census rather than deleted because it is
	// the one number that would report the blindness coming back. Until #1517
	// this field counted 31 — every aider recording, paired by
	// tools/replay-fixtures.sh and by no Go gate, so #1342's known-lists and
	// #1480's ratchets described the catalog that walk could see rather than
	// the catalog. A recording committed with its transcript in some third
	// format would be invisible to every gate here and would land in exactly
	// this figure. A zero that is measured every run is evidence; a zero that
	// is assumed is the shape #1503 was filed about.
	UnpairedSidecars int
	// PairedButUngraded counts recordings the walk DID pair and still could not
	// grade: resolveInputPaths declined the sidecar, or the replay produced no
	// extended check at all. Two populations dominate, both legitimate — the
	// process-owned-store adapters, which record no fswatcher fires and degrade
	// to transcript-only, and the 29 aider recordings whose sidecar names no
	// real session in a transcript_new event and is therefore not drivable
	// (the benign half of the pair TestReplayWithSidecar_CorruptSidecarIsFatal
	// separates).
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
// #1799 moved two figures, both upward by three and both for one reason: the
// first adapters to produce a session error now replay `error` transitions
// against sidecars recorded before that state existed. Divergent rose from 145
// and DivergentByCountsAndKinds from 144 — the new values are the literal
// below, deliberately not restated here (see
// TestNoCommentRestatesALiveCensusFigure, which fails on a hand-typed copy).
//
// The three that joined are all claudecode: 2-14_turn-aborted-by-error
// (ordered_mismatches 0 -> 3) and both 2-9_token-quota-exhausted recordings
// (0 -> 1 each). The two copilot recordings whose goldens this change also
// touched were ALREADY divergent and stayed at their existing counts (2 and 4)
// — their mismatch changed content, not existence — which is why the figure
// moves by three and not five. Measured per file with
//
//	git show HEAD:<golden> | jq '(.extended_check.ordered_mismatches // []) | length'
//
// against the same expression over the working copy, not inferred from the
// number of goldens the change rewrote.
//
// This is the frozen-sidecar cost of the fourth state and not a replay-fidelity
// regression: the daemon that produced those sidecars had no `error` state to
// record, so the replay reproducing one is the intended new behaviour rather
// than a drift from the daemon. It resolves on re-record. #1800 will move these
// figures again on its own adapters.
//
// #1800 moved Divergent and DivergentByCountsAndKinds up by exactly TWO
// recordings each, and both divergences SHOULD exist. (The values are
// deliberately not restated here — the machine-generated literal below is the
// one copy, and TestNoCommentRestatesALiveCensusFigure enforces that.)
//
// Both are turn-aborted-by-error cells whose sidecar was recorded against a
// daemon that could not see the failure, and both are frozen:
//
//   - pi's 2-14. pi reports a failed turn as `stopReason:"error"`, which the
//     parser did not read: the event was emitted as a mid-turn "assistant", so
//     the session stuck in `working` for the rest of its life. The parser now
//     emits `turn_done` plus a SessionError, so the reconstruction settles
//     where the frozen recording did not.
//   - gemini-cli's 2-14. Its failure settles to `error` now rather than to
//     `ready`.
//
// Neither can come back into agreement without a re-record, which is exactly
// what `known_failing` means.
//
// Attribution measured rather than inferred, once per recording: disabling
// only pi's new `case "error"` arm accounts for the first, and disabling only
// gemini's terminal-info error for the second. Nothing else in the change
// moves either figure.
//
// #1836 moved exactly ONE figure — Recordings, up by one — and the figure it
// was expected to move, Divergent, did not move at all. The re-record of
// claudecode's 2-25_agent-process-crash-midturn on a daemon carrying
// lifecycle.KindProcessDiedMidTurn added a recording whose golden reproduces
// the daemon exactly (no ordered_mismatches), so it joins the denominator
// without joining Divergent. The superseded 2026-08-25 recording is retained
// beside it under #268's deletion guard and is still divergent, so nothing
// LEAVES Divergent either. Divergent counts divergent RECORDINGS, not
// divergent cells: a re-record that keeps its predecessor can only ever hold
// that figure still. It would fall only when the older recording is retired
// out of the catalog into regressions/, which is a separate, deliberate act.
// #1860 moved Divergent and DivergentByCountsAndKinds up by exactly ONE
// recording each, and it is the recording #1836 had just brought INTO
// agreement: claudecode's 2-25_agent-process-crash-midturn, 2026-08-26. (The
// values are deliberately not restated here — the machine-generated literal
// below is the one copy, and TestNoCommentRestatesALiveCensusFigure enforces
// that.)
//
// #1860 reverts #1800: a process exit deletes the row again, whatever state it
// was in, so the replay no longer reconstructs the `working→error` that
// recording's frozen sidecar holds. The 2026-08-25 recording beside it was
// already divergent and stays divergent, so the figure moves by one and not by
// two. This is the frozen-sidecar cost of REMOVING a behaviour, the mirror of
// the #1798/#1800 entries above, and it resolves the same way — a re-record on
// a post-#1860 daemon. The cell is marked known_failing in the meantime.
var censusOfTheCommittedCatalog = catalogCensus{
	Recordings:                320,
	Zero:                      1,
	Fabricated:                1,
	Divergent:                 152,
	DivergentByCountsAndKinds: 151,
	UnpairedSidecars:          0,
	PairedButUngraded:         87,
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

// alignedFieldLine renders the one line literal() must contain for a field:
// its name and ITS OWN value, bound together in one string.
//
// Bound rather than checked apart, because that is the property the pasted
// block rests on and the one an independent pair of Contains calls cannot see.
// #1517 replaced a hand-typed verbatim list here with two separate
// substring checks — "the name appears" and "the value appears somewhere" — and
// a literal() that paired every field with its NEIGHBOUR's value satisfied
// both, stayed gofmt-canonical, kept the right line count, and shipped a census
// of garbage past the whole package (measured, on review).
//
// The width is re-derived by the same rule literal() uses. Do NOT "simplify"
// that by having literal() call this function: the moment the renderer and the
// expectation share one implementation, every assertion below is comparing a
// string to itself and passes for any pairing whatsoever. The duplication IS
// the test. It is not tautological as written, because the round-trip through
// go/format in TestCensusLiteralIsValidPasteableSource is an independent
// authority on the width, and this function pins the pairing that round-trip
// cannot see.
func (c catalogCensus) alignedFieldLine(f censusField) string {
	width := 0
	for _, other := range c.fields() {
		if n := len(other.name) + 1; n > width {
			width = n
		}
	}
	return fmt.Sprintf("\t%-*s %d,\n", width, f.name+":", f.value)
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

// divergesByCountsAndKinds is the NEAR-MISS spelling: "the transition counts
// differ, or the kind sets do". It is the one that reads as a restatement of
// Diverges and is not one — it misses an equal-length replay whose transitions
// are the same set in the wrong ORDER, which is how a whole comparison table
// came out one low.
//
// It is named, and named HERE rather than in extended_check.go, on purpose:
// the point of #1503 is that a counting rule with no name gets re-spelled
// slightly differently at each site, so the wrong spelling earns a name too —
// but it is test-only, and giving it a method beside Diverges in production
// would invite a caller.
func divergesByCountsAndKinds(ec *extendedCheck) bool {
	return ec.RecordedCount != ec.ReplayedCount || kindSetsDiffer(ec)
}

// divergesByOrderOrKinds is main.go's FORMER exit-code spelling, kept only so
// the census can measure that collapsing it onto Diverges changed nothing.
func divergesByOrderOrKinds(ec *extendedCheck) bool {
	return ec.Diverges() || kindSetsDiffer(ec)
}

// kindSetsDiffer is the disjunct both near-misses share.
func kindSetsDiffer(ec *extendedCheck) bool {
	return len(ec.MissingKinds) > 0 || len(ec.ExtraKinds) > 0
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
		if divergesByCountsAndKinds(ec) {
			measured.DivergentByCountsAndKinds++
		}
		wide := divergesByOrderOrKinds(ec)
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

	// The count check is strictly redundant — every disagreeing recording is
	// already in `disagreed`, so an empty slice implies equal counts. It stays
	// as a cross-check on the COLLECTION rather than on the predicates: a bug
	// in the append above would otherwise make this arm silently unfalsifiable,
	// which is the failure mode this whole file is about. Noted rather than
	// deleted, because the same diff removes a genuinely redundant disjunction
	// from main.go and the two calls deserve to be distinguishable.
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
// how many of them the walk cannot pair, because no name in transcriptNames
// sits beside them.
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
		if _, ok := pairedTranscript(filepath.Dir(path)); !ok {
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
	// moved renders the verdict for one perturbed field, taking the MEASURED
	// half from the live census rather than restating it.
	//
	// The rows below used to spell their expectations out in full — "Divergent:
	// committed 139, measured 140" — which put a hand-typed copy of every
	// census figure in the corpus that exists to remove hand-typed copies. It
	// held only while the census stood still: #1517 widened the walk, the
	// census legitimately moved, and seven of these rows failed for a reason
	// none of them is about. What each row pins is the SHAPE of the verdict and
	// the perturbation that provokes it; the figure is the census's business.
	moved := func(field string, committed int) string {
		for _, f := range current.fields() {
			if f.name == field {
				return fmt.Sprintf("%s: committed %d, measured %d", field, committed, f.value)
			}
		}
		t.Fatalf("no census field named %q — the corpus names a figure that no longer exists", field)
		return ""
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
			want:      []string{moved("Divergent", current.Divergent-1)},
		},
		{
			// The other direction. A count that only ever fails when it is too
			// LOW blesses a regression that raises it.
			name:      "divergent one high — a fidelity regression left uncounted",
			committed: with(func(c *catalogCensus) { c.Divergent++ }),
			want:      []string{moved("Divergent", current.Divergent+1)},
		},
		{
			// The near-miss converging on Diverges means the catalog lost its
			// only order-only witness — a coverage loss that looks like
			// agreement.
			name:      "the near-miss spelling stopped being a near miss",
			committed: with(func(c *catalogCensus) { c.DivergentByCountsAndKinds = current.Divergent }),
			want:      []string{moved("DivergentByCountsAndKinds", current.Divergent)},
		},
		{
			// The shape #1342 and #1478 both moved: a zero-transition recording
			// rescued, with the doc comments still claiming the old figure.
			name:      "zero stale at its pre-#1478 value",
			committed: with(func(c *catalogCensus) { c.Zero = 4 }),
			want:      []string{moved("Zero", 4)},
		},
		{
			name:      "fabricated stale — the count that must never rise unnoticed",
			committed: with(func(c *catalogCensus) { c.Fabricated = 3 }),
			want:      []string{moved("Fabricated", 3)},
		},
		{
			// This population is what the Go walk cannot pair at all. It is 0,
			// and a committed non-zero value would be claiming the gates are
			// blind to recordings they now read — the state #1517 closed.
			name:      "the unpaired-sidecar population moved",
			committed: with(func(c *catalogCensus) { c.UnpairedSidecars = 29 }),
			want:      []string{moved("UnpairedSidecars", 29)},
		},
		{
			// Moves when the walk's skip conditions change — a store adapter
			// gaining an fswatcher trace, or a pairing rule quietly widening.
			name:      "the ungraded population moved",
			committed: with(func(c *catalogCensus) { c.PairedButUngraded = 54 }),
			want:      []string{moved("PairedButUngraded", 54)},
		},
		{
			// The denominator rots on its own schedule: every promotion or
			// retirement moves it, and nothing else in the census need change.
			name:      "the denominator alone went stale",
			committed: with(func(c *catalogCensus) { c.Recordings = 300 }),
			want:      []string{moved("Recordings", 300)},
		},
		{
			// Everything at once, in declaration order — a census pasted from a
			// different branch names every figure rather than the first one.
			name: "every figure moved, and every one is named",
			// Every field is perturbed by a distinct non-zero delta rather than
			// set to fixed values: a constant that happens to equal the live
			// figure silently drops its line from the expected verdict, which
			// is how this row would stop testing "every one is named" without
			// failing. UnpairedSidecars reached exactly that state at 0.
			committed: with(func(c *catalogCensus) {
				c.Recordings -= 9
				c.Zero += 3
				c.Fabricated += 2
				c.Divergent += 5
				c.DivergentByCountsAndKinds += 5
				c.UnpairedSidecars += 7
				c.PairedButUngraded -= 4
			}),
			want: []string{
				moved("Recordings", current.Recordings-9),
				moved("Zero", current.Zero+3),
				moved("Fabricated", current.Fabricated+2),
				moved("Divergent", current.Divergent+5),
				moved("DivergentByCountsAndKinds", current.DivergentByCountsAndKinds+5),
				moved("UnpairedSidecars", current.UnpairedSidecars+7),
				moved("PairedButUngraded", current.PairedButUngraded-4),
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
				if want := current.alignedFieldLine(f); !strings.Contains(lit, want) {
					t.Errorf("literal() does not carry %s beside its measured value; "+
						"want the line %q:\n%s", f.name, want, lit)
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
	// The alignment is checked by running the block through gofmt itself and
	// requiring it back unchanged, which IS the property the idiom rests on: a
	// literal that is correct but misaligned is reformatted on the next gofmt
	// run and stops being byte-identical to what this test prints.
	//
	// It used to be pinned as seven verbatim lines carrying the committed
	// figures. That spelling asserted the alignment and the CENSUS at once, so
	// it failed whenever a figure legitimately moved — #1517 widened the walk
	// and it broke, having caught nothing about alignment. Round-tripping is
	// also strictly stronger: the verbatim list could only confirm the widths
	// someone typed, while gofmt is the authority those widths were guessing
	// at. It is not tautological, because literal() derives the column itself
	// rather than deferring to go/format.
	if formatted, err := format.Source([]byte(lit)); err != nil {
		t.Errorf("literal() is not valid Go source (%v):\n%s", err, lit)
	} else if string(formatted) != lit {
		t.Errorf("literal() is not gofmt-canonical, so the pasted block would be reformatted "+
			"and stop matching what this test prints.\n got:\n%s\nwant:\n%s", lit, formatted)
	}
	// Each field still has to appear beside ITS OWN value. Checking the name
	// and the value as two independent substrings is satisfied by a literal
	// that pairs every field with the wrong figure, which is worse than an
	// omission: the pasted block would be gofmt-clean and wrong.
	for _, f := range censusOfTheCommittedCatalog.fields() {
		if want := censusOfTheCommittedCatalog.alignedFieldLine(f); !strings.Contains(lit, want) {
			t.Errorf("literal() is missing the gofmt-aligned line %q for %s:\n%s",
				want, f.name, lit)
		}
	}
}
