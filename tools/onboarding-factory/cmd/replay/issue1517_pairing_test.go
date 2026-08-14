package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	internalreplay "irrlicht/tools/onboarding-factory/internal/replay"
)

// Issue #1517: every aider recording was invisible to every Go replay gate.
//
// tools/replay-fixtures.sh has always walked `-name transcript.jsonl -o -name
// transcript.md`, while the Go walk paired a sidecar with a sibling
// transcript.jsonl and nothing else — so the sweep graded 31 aider recordings
// that #1342's known-lists, #1480's ratchets and #1503's census never saw. The
// two walks reported different counts for the same headline, which is how the
// divergence became visible at all, but the counts were the symptom — and the
// live ones are censusOfTheCommittedCatalog's, machine-generated, rather than
// restated here. The gates' stated design goal is that they are catalog-wide "so a
// newly recorded fixture is covered by existing coverage rather than by
// somebody remembering" (#1342), and for one adapter that was simply false.
//
// This file holds the pairing rule itself and the lock on it. The rule is one
// function so that widening or narrowing it is a single reviewable edit rather
// than a filename repeated at each walk.

// transcriptNames is the rule these walks pair by, and it is the PRODUCTION
// declaration rather than a copy of it: internal/replay.TranscriptNames is what
// SynthesizeEventsFromTranscript reads, so the gates cannot come to disagree
// with the code they are grading. That disagreement is exactly #1517 — the
// gates' copy said transcript.jsonl alone.
//
// The ORDER is part of the rule rather than an accident of iteration:
// internal/validate/expected.go treats a recording carrying both names as
// ambiguous, so the walk must not let directory order decide. No committed
// recording has both today — measured over the whole catalog: 396 sidecars,
// 365 beside a transcript.jsonl, 31 beside a transcript.md, none with both and
// none with neither — and TestPairedTranscriptPrefersJSONL pins the tie-break
// so it stays decided if one ever does.
var transcriptNames = internalreplay.TranscriptNames

// pairedTranscript returns the transcript sitting beside a sidecar in dir, and
// whether any exists at all.
//
// One function rather than a filename per call site. Before #1517 the rule was
// spelled three times over — a const in issue1503_census_test.go used by two
// walks, and a hardcoded "transcript.jsonl" in hook_fixture_test.go — so a
// change to the const moved two of the three sites and left the third silently
// narrower.
func pairedTranscript(dir string) (string, bool) {
	for _, name := range transcriptNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// TestPairedTranscriptPrefersJSONL pins the tie-break, the "no transcript at
// all" answer, and — the arm that matters — that a markdown transcript pairs
// when it is the only one present.
//
// Hermetic, so it keeps failing on a narrowed rule even if the catalog stops
// carrying a markdown recording. That is the division of labour with the
// catalog lock below: this one pins the RULE, that one proves the rule reaches
// real committed recordings.
func TestPairedTranscriptPrefersJSONL(t *testing.T) {
	both := t.TempDir()
	writeFile(t, filepath.Join(both, "transcript.jsonl"), "{}\n")
	writeFile(t, filepath.Join(both, "transcript.md"), "# hello\n")
	if got, ok := pairedTranscript(both); !ok || filepath.Base(got) != "transcript.jsonl" {
		t.Errorf("a recording carrying BOTH names paired with (%q, %v), want transcript.jsonl — "+
			"internal/validate/expected.go calls that shape ambiguous, so the tie-break must be "+
			"the declared order and not the filesystem's", filepath.Base(got), ok)
	}

	mdOnly := t.TempDir()
	writeFile(t, filepath.Join(mdOnly, "transcript.md"), "# hello\n")
	if got, ok := pairedTranscript(mdOnly); !ok || filepath.Base(got) != "transcript.md" {
		t.Errorf("a markdown-only recording paired with (%q, %v), want transcript.md — this is "+
			"#1517 itself: every aider recording is this shape, and a rule that answers false "+
			"here hides all 31 from every Go gate while tools/replay-fixtures.sh grades them",
			filepath.Base(got), ok)
	}

	if got, ok := pairedTranscript(t.TempDir()); ok {
		t.Errorf("an empty directory paired with %q — a walk that pairs a sidecar with a "+
			"transcript that does not exist reports recordings it never read", got)
	}
}

// TestTheCatalogWalkGradesMarkdownTranscripts is the catalog-wide half of the
// lock: the shared walk must actually reach committed recordings whose
// transcript is a transcript.md, not merely be capable of it.
//
// This is the assertion that goes red if the pairing is narrowed back to
// transcript.jsonl alone, which is what the widening owes as its mutation
// evidence. The census gate catches the same narrowing a second way — its
// population would SHRINK from 311 to 309 and trip the fail-loudly guard ahead
// of the paste-the-literal advice — but that one reports a moved NUMBER, while
// this one names the property.
func TestTheCatalogWalkGradesMarkdownTranscripts(t *testing.T) {
	var markdown []string
	checked := forEachSidecarRecording(t, func(name string, _ *extendedCheck) {
		if filepath.Base(name) == "transcript.md" {
			markdown = append(markdown, name)
		}
	})
	if len(markdown) == 0 {
		t.Fatalf("the walk graded %d recordings and not one of them has a transcript.md, so "+
			"every aider recording in the catalog is invisible to this gate, to #1480's and to "+
			"#1503's census — the exact state #1517 closed. Either pairedTranscript has been "+
			"narrowed back to transcript.jsonl, or the catalog's markdown recordings stopped "+
			"producing an extended check; the second is a replay regression and not a reason to "+
			"delete this test", checked)
	}
	t.Logf("the walk grades %d markdown-transcript recording(s) of %d:\n  %v",
		len(markdown), checked, markdown)
}

// findNameRE picks the -name arguments out of the sweep's find invocation.
var findNameRE = regexp.MustCompile(`-name\s+'([^']+)'`)

// TestSweepAndGatesWalkTheSameTranscriptNames pins the one copy of the rule
// that cannot be shared by reference: tools/replay-fixtures.sh selects
// recordings with its own `find … -name` list, in shell.
//
// Without this the agreement is a sentence. #1517 was two hand-kept lists
// drifting apart — the sweep grading 31 aider recordings that no Go gate could
// see — and unifying the Go side into one variable does not stop the shell's
// list from drifting tomorrow. Note which direction is dangerous: a name the
// SWEEP gains and the gates do not is invisible to UnpairedSidecars, because
// that figure counts sidecars no Go name can pair and a recording the gates
// never look at does not move it.
//
// It reads the script rather than restating its regexp — the sibling contract
// TestDriftSummary_FormatIsTheShellContract hardcodes the sweep's pattern and
// so pins the format while still allowing the two files to disagree about it.
func TestSweepAndGatesWalkTheSameTranscriptNames(t *testing.T) {
	sweep := mustAbs(t, filepath.Join("..", "..", "..", "replay-fixtures.sh"))
	src, err := os.ReadFile(sweep)
	if err != nil {
		t.Fatalf("read the sweep: %v", err)
	}

	var names []string
	var line string
	for _, l := range strings.Split(string(src), "\n") {
		if !strings.Contains(l, "find \"$FIXTURES_ROOT\"") {
			continue
		}
		line = l
		for _, m := range findNameRE.FindAllStringSubmatch(l, -1) {
			names = append(names, m[1])
		}
		break
	}
	// Both guards are the vacuity check this contract needs most: a rename in
	// the script makes every assertion below pass over an empty list, which is
	// the reassuring spelling of "the two sides are no longer compared".
	if line == "" {
		t.Fatal("no `find \"$FIXTURES_ROOT\"` line in tools/replay-fixtures.sh — this contract " +
			"is reading nothing. Find where the sweep selects recordings and re-anchor it, rather " +
			"than deleting the test")
	}
	if len(names) == 0 {
		t.Fatalf("the sweep's find line matched no -name argument, so this compares two empty "+
			"sets:\n  %s", line)
	}

	got, want := append([]string(nil), names...), append([]string(nil), transcriptNames...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools/replay-fixtures.sh walks %v but the Go gates pair %v.\n"+
			"The two must select the same recordings or one of them grades a population the "+
			"other cannot see — #1517, where the sweep read every aider recording and no Go "+
			"gate did. Update whichever side is wrong in the same commit.\n  sweep line: %s",
			got, want, line)
	}
	t.Logf("the sweep and the gates both walk %v", want)
}
