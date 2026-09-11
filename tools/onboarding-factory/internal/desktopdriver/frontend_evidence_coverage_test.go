package desktopdriver

// The freshness guard next door ties a front-end verdict to the build it names.
// It can only tie the verdicts it recognises, and it recognises them by the
// evidence note they cite — so the set of notes IS the guard's reach.
//
// That set shipped holding one filename while two notes existed. frontend-gaps.md
// was listed; slash-commands.md was not, though it opens with "both from Claude
// Desktop 1.46388.4 with bundled Claude Code 2.1.260" and four verdicts rest on
// it. All four passed validation naming no build at all, and the freshness guard
// walked straight past them. Nothing was wrong with the guard's logic; the set it
// was handed did not describe the tree.
//
// This test refuses to let the set be a hand-typed list again. It derives the
// same answer from the notes themselves — a note that names a concrete Claude
// Desktop build is a front-end measurement — and fails when the two disagree in
// either direction. Adding the next measurement note now fails here until it is
// declared, instead of quietly widening the blind spot.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/desktopresults"
)

const claudecodeEvidenceDir = "../../../../replaydata/agents/claudecode/desktop-evidence"

// desktopBuildMention matches a note stating the build it was measured on. The
// committed notes all write it the same way, in their opening paragraph.
var desktopBuildMention = regexp.MustCompile(`Claude Desktop [0-9]+\.[0-9]+\.[0-9]+`)

// notesDeclaringADesktopBuild returns the prose notes that name a concrete
// Claude Desktop build, sorted.
//
// A directory it cannot read is an error, never an empty result: "no note names
// a build" and "no note could be opened" must not reach the caller alike.
func notesDeclaringADesktopBuild(evidenceDir string) ([]string, error) {
	entries, err := os.ReadDir(evidenceDir)
	if err != nil {
		return nil, fmt.Errorf("read the desktop-evidence directory: %w", err)
	}
	declaring := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(evidenceDir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if desktopBuildMention.Match(body) {
			declaring = append(declaring, name)
		}
	}
	sort.Strings(declaring)
	return declaring, nil
}

// auditFrontendEvidenceCoverage compares the declared set against the notes that
// actually name a build, and returns one line per disagreement.
//
// It reports both directions. A note that names a build and is not declared is
// the defect that happened; a declared name with no such note is a typo that
// would silently exempt every verdict citing the real file.
func auditFrontendEvidenceCoverage(declared, declaring []string) []string {
	findings := append(
		namesMissingFrom(declaring, declared, "%s names a Claude Desktop build but is not in "+
			"desktopresults.FrontendEvidenceFiles(): every verdict citing it is exempt from the "+
			"freshness guard"),
		namesMissingFrom(declared, declaring, "desktopresults.FrontendEvidenceFiles() names %s, and "+
			"no note by that name declares a Claude Desktop build")...,
	)
	sort.Strings(findings)
	return findings
}

// namesMissingFrom formats one finding per name absent from present.
func namesMissingFrom(names, present []string, format string) []string {
	known := make(map[string]bool, len(present))
	for _, name := range present {
		known[name] = true
	}
	findings := make([]string, 0, len(names))
	for _, name := range names {
		if !known[name] {
			findings = append(findings, fmt.Sprintf(format, name))
		}
	}
	return findings
}

func TestFrontendEvidenceSetCoversEveryNoteNamingABuild(t *testing.T) {
	declaring, err := notesDeclaringADesktopBuild(claudecodeEvidenceDir)
	if err != nil {
		t.Fatalf("collect the notes that name a Desktop build: %v", err)
	}
	// Finding none means the scan broke. These notes are committed data, and
	// the freshness guard is meaningless without at least one.
	if len(declaring) == 0 {
		t.Fatalf("no note under %s names a Claude Desktop build — this check cannot run", claudecodeEvidenceDir)
	}
	for _, finding := range auditFrontendEvidenceCoverage(desktopresults.FrontendEvidenceFiles(), declaring) {
		t.Errorf("the front-end evidence set does not describe the tree: %s", finding)
	}
}

// TestFrontendEvidenceCoverageCatchesAnUndeclaredNote replays the exact defect.
// The audit is an ADDED check with no red state of its own, so what proves it
// works is shrinking the declared set back to the single filename it shipped
// with and watching the second note surface by name.
func TestFrontendEvidenceCoverageCatchesAnUndeclaredNote(t *testing.T) {
	declaring, err := notesDeclaringADesktopBuild(claudecodeEvidenceDir)
	if err != nil {
		t.Fatalf("collect the notes that name a Desktop build: %v", err)
	}
	if got := auditFrontendEvidenceCoverage(desktopresults.FrontendEvidenceFiles(), declaring); len(got) != 0 {
		t.Fatalf("the committed set must be clean before the mutation: %v", got)
	}

	// The set as it shipped: frontend-gaps.md alone.
	findings := auditFrontendEvidenceCoverage([]string{desktopresults.FrontendGapsFile}, declaring)
	if len(findings) == 0 {
		t.Fatal("dropping a note from the declared set must be caught")
	}
	if !strings.Contains(strings.Join(findings, "\n"), desktopresults.SlashCommandsFile) {
		t.Fatalf("the finding must name the dropped note; got %v", findings)
	}
}

// TestFrontendEvidenceCoverageCatchesAPhantomName is the other direction: a name
// in the set that no note answers to exempts nothing and protects nothing.
func TestFrontendEvidenceCoverageCatchesAPhantomName(t *testing.T) {
	findings := auditFrontendEvidenceCoverage([]string{"frontend-gasp.md"}, []string{desktopresults.FrontendGapsFile})
	if len(findings) != 2 {
		t.Fatalf("a misspelt name must flag both the phantom and the uncovered note; got %v", findings)
	}
}

// TestFrontendEvidenceCoverageRefusesAnUnreadableTree keeps "cannot look"
// separate from "found nothing".
func TestFrontendEvidenceCoverageRefusesAnUnreadableTree(t *testing.T) {
	if _, err := notesDeclaringADesktopBuild(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("an evidence directory that cannot be read must be an error, not an empty result")
	}
	if notes, err := notesDeclaringADesktopBuild(t.TempDir()); err != nil || len(notes) != 0 {
		t.Fatalf("an empty tree must read cleanly and name no note; got %v, %v", notes, err)
	}
}
