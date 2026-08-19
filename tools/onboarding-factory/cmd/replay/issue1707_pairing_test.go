package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/lifecycle"
)

// Issue #1707: compareOrdered pairs a replayed transition with a recorded one
// on prev_state/new_state and nothing else, and #1480 then hands every
// kind-matched pair a timing delta — so the issue asked whether `reason`
// belongs in the match predicate.
//
// It does not, and the measurement is what settled it rather than the argument.
// Over the committed catalog: 49 of 836 kind-matched pairs differ on reason, of
// which 45 are one shape — a session's FIRST ready→working, reached through the
// classifier's transcript_activity default on the daemon side and through the
// force bounce on the replay side, with the same recording then agreeing on
// "force" for every later ready→working. Those 45 sit CLOSER in time than the
// pairs that agree (77.6% within 1ms against 19.7%), which is the opposite of
// what two different transitions look like. Three more are reason strings
// edited after their sidecar was frozen. Putting `reason` in the predicate
// would demote all 48 and delete four genuine drift measurements — three of
// them pinned by name in knownFirstTransitionDrift — to reach one pair.
//
// What this file adds is not that count. A raw reason-difference counter would
// only ever grow, for two reasons nobody will fix, and would report the one
// real pair a second time. It adds the narrow predicate the count was hiding —
// CrossMechanism, exactly one side synthesized — plus the assertion the
// decision to close actually rests on: every such pair in the catalog is
// already visible to an existing mechanism. The day one is not, this fails and
// #1707 is a live issue again.

// ---------------------------------------------------------------------------
// 1. The predicate, against a committed corpus.
// ---------------------------------------------------------------------------

// pairingCase is one testdata/pairing/*.json row. Same two input slices
// testdata/timing/ supplies; the two want_* fields are this corpus's own.
type pairingCase struct {
	Description string            `json:"description"`
	Recorded    []lifecycle.Event `json:"recorded"`
	Replayed    []transition      `json:"replayed"`
	WantCross   []int             `json:"want_cross_mechanism"`
	WantUnrep   bool              `json:"want_unreported"`
}

// TestCrossMechanismCorpus drives every committed shape through the production
// pairing and pins both verdicts — which pairs the predicate must report AND
// which it must leave alone. Only the second kind of row separates a predicate
// that discriminates from one that fires on any reason difference, which is
// precisely the rule #1707 rejected.
func TestCrossMechanismCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "pairing")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}

	var ran, sawCross, sawSilent, sawUnreported, sawReported int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			var tc pairingCase
			if err := json.Unmarshal(raw, &tc); err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			if tc.Description == "" {
				t.Fatalf("%s has no description — a corpus row states what it pins", name)
			}
			if len(tc.Recorded) == 0 || len(tc.Replayed) == 0 {
				t.Fatalf("%s supplies %d recorded / %d replayed — an empty side pins nothing",
					name, len(tc.Recorded), len(tc.Replayed))
			}

			ec := &extendedCheck{
				RecordedCount: len(tc.Recorded),
				ReplayedCount: len(tc.Replayed),
			}
			ec.TimeDeltas, ec.OrderedMismatches = compareOrdered(tc.Recorded, tc.Replayed)
			ec.OrderedMatches = len(ec.TimeDeltas)

			// A row that produced no kind-matched pair asserts nothing about a
			// predicate that only reads kind-matched pairs, while still
			// passing and still counting toward the balance guards below.
			if len(ec.TimeDeltas) == 0 {
				t.Fatalf("%s produced no kind-matched pair — the predicate under test is never "+
					"consulted, so this row passes without asserting anything", name)
			}

			var got []int
			for _, d := range ec.TimeDeltas {
				if d.CrossMechanism() {
					got = append(got, d.Index)
				}
			}
			want := tc.WantCross
			if want == nil {
				want = []int{}
			}
			if got == nil {
				got = []int{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s:\n  cross-mechanism pairs = %v, want %v", tc.Description, got, want)
			}

			// The second verdict, and the one the catalog gate turns on: a
			// cross-mechanism pair is UNREPORTED when the recording carries no
			// ordered divergence, because then nothing else in this package
			// says the two sequences failed to line up.
			gotUnrep := len(got) > 0 && !ec.Diverges()
			if gotUnrep != tc.WantUnrep {
				t.Errorf("%s:\n  unreported = %t, want %t (cross pairs %v, diverges %t)",
					tc.Description, gotUnrep, tc.WantUnrep, got, ec.Diverges())
			}

			if len(want) > 0 {
				sawCross++
			} else {
				sawSilent++
			}
			if tc.WantUnrep {
				sawUnreported++
			} else {
				sawReported++
			}
			ran++
		})
	}

	if ran == 0 {
		t.Fatal("no corpus case ran — testdata/pairing/ is empty or unreadable, and an empty corpus reads exactly like a passing one")
	}
	if sawCross == 0 {
		t.Error("no corpus case expects a cross-mechanism pair — nothing here proves the predicate can fire")
	}
	if sawSilent == 0 {
		t.Error("no corpus case expects silence — nothing here separates this predicate from one that " +
			"reports every reason difference, which is the rule #1707 measured and rejected")
	}
	if sawUnreported == 0 || sawReported == 0 {
		t.Errorf("corpus expects unreported on %d cases and reported on %d — both are needed, or a gate "+
			"flagging every cross-mechanism pair looks identical to one flagging correctly",
			sawUnreported, sawReported)
	}
	t.Logf("corpus: %d cases (%d expect a cross-mechanism pair, %d expect silence; %d unreported, %d already reported)",
		ran, sawCross, sawSilent, sawUnreported, sawReported)
}

// syntheticReasonConstant matches the exported reason constants in the
// classifier's own source. Anchored at the start of a const declaration so
// prose mentioning a name in a doc comment is not counted.
var syntheticReasonConstant = regexp.MustCompile(`(?m)^const\s+(Synthetic\w*Reason)\s*=`)

// TestSyntheticReasonsNamesEveryConstant is the completeness guard syntheticReasons
// cannot give itself. Naming a constant is compile-checked, but a SIXTH
// synthesizer added to state_classifier.go would leave the set short and
// CrossMechanism silently under-reporting — "found nothing" and "could not
// look" producing the same output, which AGENTS.md forbids. So the classifier's
// own source is the list, the same move TestAllHookEvents_CoversEveryConstant
// makes for hook events.
func TestSyntheticReasonsNamesEveryConstant(t *testing.T) {
	path := mustAbs(t, filepath.Join("..", "..", "..", "..", "core", "application", "services", "state_classifier.go"))
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — this guard cannot run, which is not the same as passing", path, err)
	}

	matches := syntheticReasonConstant.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("%s declares no Synthetic*Reason constant — either the classifier moved or this "+
			"scan stopped matching, and both read as a pass if allowed to", path)
	}

	// Resolve each declared constant's VALUE by name against the set, rather
	// than re-parsing the string literal: the set is keyed on the value, and
	// pairing the two by name is what would silently drift.
	declared := map[string]bool{}
	for _, m := range matches {
		declared[m[1]] = true
	}
	byName := map[string]string{
		"SyntheticWaitingReason":          services.SyntheticWaitingReason,
		"SyntheticTurnSettleReason":       services.SyntheticTurnSettleReason,
		"SyntheticQueuedTurnStartReason":  services.SyntheticQueuedTurnStartReason,
		"SyntheticCatchUpTurnStartReason": services.SyntheticCatchUpTurnStartReason,
		"SyntheticCatchUpTurnDoneReason":  services.SyntheticCatchUpTurnDoneReason,
	}

	var missing []string
	for name := range declared {
		value, known := byName[name]
		switch {
		case !known:
			missing = append(missing, fmt.Sprintf("%s (new synthesizer, unknown to this package)", name))
		case !syntheticReasons[value]:
			missing = append(missing, fmt.Sprintf("%s (known, but its value is not in syntheticReasons)", name))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("state_classifier.go declares %d synthesizer reason constant(s) syntheticReasons does not "+
			"cover:\n  %s\nCrossMechanism under-reports by exactly these, silently.",
			len(missing), strings.Join(missing, "\n  "))
	}
	// And the other direction: an entry naming a constant the classifier no
	// longer declares is dead weight that reads as coverage.
	for name := range byName {
		if !declared[name] {
			t.Errorf("this package still names %s, which state_classifier.go no longer declares", name)
		}
	}
	if len(syntheticReasons) != len(byName) {
		t.Errorf("syntheticReasons holds %d values but this guard checks %d names — one of the two "+
			"grew without the other, so the guard no longer covers the set",
			len(syntheticReasons), len(byName))
	}
	t.Logf("checked %d synthesizer reason constant(s) against syntheticReasons", len(declared))
}

// ---------------------------------------------------------------------------
// 2. The catalog assertion — the one this issue closes on.
// ---------------------------------------------------------------------------

// knownCrossMechanismPairs enumerates every kind-matched pair in the committed
// catalog where exactly one side is a synthesized transition, together with
// what ALREADY reports it.
//
// Machine-generated: TestCrossMechanismPairsAreAlreadyReported prints the exact
// map literal to paste when the set legitimately changes. It is not maintained
// by hand and must not be.
//
// The single entry is #1707's one genuine finding, and reading it is what
// closed that issue: the pair is one index behind a state_differs, so the two
// sequences are shifted and everything after the shift is suspect by
// construction. That recording is already the most-reported in the catalog —
// it is divergenceWitness, the named witness for Diverges in
// issue1503_census_test.go, and a named entry in knownFirstTransitionDrift
// carrying this exact pair's delta ("-18.621s at pair 1").
var knownCrossMechanismPairs = map[string]string{
	"copilot/scenarios/1-4_session-resume/recordings/2026-08-05-19-02-01_irrlichd-0.5.9+4b58365/transcript.jsonl": "pair 1 (working→ready) -18.621s: daemon \"turn already complete at first discovery → synthetic catch-up\" vs replay \"agent finished turn → ready\"",
}

// TestCrossMechanismPairsAreAlreadyReported is the assertion #1707 was closed
// on, and it is deliberately not a gate over the 49 reason-differing pairs.
//
// It fails in three ways, the same three shapes #1480's ratchet uses:
//
//   - a NEW cross-mechanism pair appears — a pairing or classifier change
//     started comparing a synthesized transition against a classified one;
//   - a pinned pair stops appearing, so the entry has rotted into an excuse;
//   - any cross-mechanism pair sits in a recording that does NOT otherwise
//     diverge, which is the condition the whole dismissal rests on. #1707 was
//     closed because the one real pair is already reported by an existing
//     mechanism; a pair that nothing else reports is that argument failing, and
//     is the signal to reopen it rather than to widen this list.
func TestCrossMechanismPairsAreAlreadyReported(t *testing.T) {
	type crossPair struct {
		name  string
		delta timeDelta
		clean bool // the recording carries no ordered divergence
	}
	var found []crossPair
	var kindMatched int

	checked := forEachSidecarRecording(t, func(name string, ec *extendedCheck) {
		kindMatched += len(ec.TimeDeltas)
		for _, d := range ec.TimeDeltas {
			if !d.CrossMechanism() {
				continue
			}
			found = append(found, crossPair{name: name, delta: d, clean: !ec.Diverges()})
		}
	})

	// The measurement's own fail-loudly guard: a walk that reached every
	// recording and produced no kind-matched pair compared nothing, and
	// reporting "no cross-mechanism pairs" from it would be a finding about the
	// harness wearing the shape of a clean catalog.
	if kindMatched == 0 {
		t.Fatalf("walked %d sidecar-driven recordings and produced ZERO kind-matched pairs — "+
			"the predicate was never consulted", checked)
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].name != found[j].name {
			return found[i].name < found[j].name
		}
		return found[i].delta.Index < found[j].delta.Index
	})

	var literal strings.Builder
	for _, p := range found {
		fmt.Fprintf(&literal, "\t%q: %q,\n", p.name, describeCrossMechanism(p.delta))
	}
	t.Logf("%d cross-mechanism pair(s) over %d kind-matched pairs in %d recordings:\n%s",
		len(found), kindMatched, checked, literal.String())

	seen := map[string]bool{}
	for _, p := range found {
		seen[p.name] = true
		if _, known := knownCrossMechanismPairs[p.name]; !known {
			t.Errorf("%s newly pairs a synthesized transition against a classified one (%s) — a "+
				"pairing or classifier change started comparing two different transitions. Paste "+
				"the printed literal into knownCrossMechanismPairs only with a reason per entry.",
				p.name, describeCrossMechanism(p.delta))
		}
		if p.clean {
			t.Errorf("%s carries a cross-mechanism pair (%s) in a recording that does NOT otherwise "+
				"diverge — nothing else in this package reports it. That is the condition #1707 was "+
				"closed against; reopen it rather than widening knownCrossMechanismPairs.",
				p.name, describeCrossMechanism(p.delta))
		}
	}
	for name := range knownCrossMechanismPairs {
		if !seen[name] {
			t.Errorf("knownCrossMechanismPairs entry %q no longer pairs a synthesized transition "+
				"against a classified one — delete it. Note that the catalog then exercises this "+
				"predicate nowhere, so check the entry was fixed rather than merely stopped being "+
				"reached (a recording that stops producing kind-matched pairs looks the same here).", name)
		}
	}
}

// describeCrossMechanism is the one spelling of a cross-mechanism figure,
// shared by the pasteable literal and the two errors, so a reader comparing
// them is looking at the same rendering.
func describeCrossMechanism(d timeDelta) string {
	return fmt.Sprintf("pair %d (%s) %+.3fs: daemon %q vs replay %q",
		d.Index, d.Kind, d.Delta.Seconds(), d.RecordedReason, d.ReplayedReason)
}
