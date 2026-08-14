package main

import (
	"os"
	"path/filepath"
	"testing"
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

// transcriptNames are the transcript filenames the catalog walks pair a sidecar
// with, in preference order. They are ONE list because every walk over this
// catalog must apply the same rule: tools/replay-fixtures.sh walks exactly
// these two names, and a bare string literal at each Go site is how the two
// came to disagree about which recordings exist at all.
//
// The ORDER is part of the rule rather than an accident of iteration:
// internal/validate/expected.go treats a recording carrying both names as
// ambiguous, so the walk must not let directory order decide. No committed
// recording has both today — measured over the whole catalog: 396 sidecars,
// 365 beside a transcript.jsonl, 31 beside a transcript.md, none with both and
// none with neither — and TestPairedTranscriptPrefersJSONL pins the tie-break
// so it stays decided if one ever does.
var transcriptNames = []string{"transcript.jsonl", "transcript.md"}

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
