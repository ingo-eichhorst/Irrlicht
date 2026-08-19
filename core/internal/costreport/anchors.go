package costreport

import (
	"os"
	"strings"
	"testing"
)

// RegenerateMarker is the uniform prefix every measured figure in a doc comment
// carries. It is one token so that the reverse direction is a grep:
// `git grep "regenerate:" core/` answers "which doc comments claim to be
// machine-producible", and AssertFiguresNameTheirGenerator answers "and are
// they".
const RegenerateMarker = "regenerate:"

// Anchor names one doc comment that quotes a measured figure.
//
// The table of them IS the decision #1572 left open about what those comments
// should say. The alternative was to reduce each figure to a pointer ("run the
// generator"). Rejected, because these figures are not decoration —
// gitHistoryTimeout is chosen against the SHAPE of a curve, and shelloutTimeout
// is a compromise between two named failures. A reader deciding whether 2s is
// still right needs the magnitude in front of them; what was missing was never
// the number, it was an owner for it. So the figure stays and gains three
// things: the conditions it was taken under, the issue that took it, and the
// command that takes it again.
type Anchor struct {
	// File is the source file holding the declaration, relative to the calling
	// package's directory (a Go test runs in its own package directory).
	File string
	// Symbol is the declaration whose doc comment carries the figure. Matched as
	// a declaration LINE rather than resolved with go/types, deliberately: half
	// of processlifecycle is behind //go:build darwin, so a type-aware load on a
	// linux runner would find nothing and pass — the gate-whose-absence-reads-
	// as-a-pass shape this repo has paid for repeatedly.
	Symbol string
	// Why names what the figure justifies, so an entry that outlives its figure
	// is removable by reading the table rather than by reading the file.
	Why string
}

// AssertFiguresNameTheirGenerator fails the build when a figure loses its
// pointer, when an anchor stops naming a real declaration, or when the marker is
// renamed on one side only. Each entry is checked in BOTH directions — that is
// the existence-check polarity nilTolerant and TW_EXEMPT_KEYS use, and it is
// what stops a table that has stopped naming real declarations from reading
// exactly like a table with nothing to say.
//
// What it deliberately does NOT do is DISCOVER figures. A lint keyed on
// figure-shaped prose was tried against both trees and rejected on the
// measurement: the word "median" appears four times in the git adapter's doc
// comments and only three of them are figures — the fourth is "a biased median
// presented as Available:true". A heuristic arriving with an exemption list is
// worse than a table, because the exemption list is what nobody re-reads.
// Discovering them is #1518's subject, in the tree that has more of them.
func AssertFiguresNameTheirGenerator(t *testing.T, anchors []Anchor) {
	t.Helper()
	if len(anchors) == 0 {
		t.Fatal("no anchors declared — this tripwire cannot pass by having nothing to check")
	}
	for _, a := range anchors {
		t.Run(a.File+"/"+a.Symbol, func(t *testing.T) {
			src, err := os.ReadFile(a.File)
			if err != nil {
				t.Fatalf("read %s: %v", a.File, err)
			}
			doc, found := DocCommentFor(string(src), a.Symbol)
			if !found {
				t.Fatalf("%s declares no %q — the anchor table has rotted, which is not a reason to skip: "+
					"either the figure moved (point the anchor at it) or it is gone (delete the entry, and %s)",
					a.File, a.Symbol, a.Why)
			}
			if !strings.Contains(doc, RegenerateMarker) {
				t.Errorf("the doc comment on %s in %s quotes a measured figure and does not carry %q.\n"+
					"That figure justifies: %s\n"+
					"Add the command that regenerates it, or move the anchor.",
					a.Symbol, a.File, RegenerateMarker, a.Why)
			}
		})
	}
}

// DocCommentFor returns the contiguous `//` block immediately preceding the line
// that declares symbol. Strings rather than go/ast, for the build-tag reason on
// Anchor.Symbol — so it is graded by a committed corpus rather than by being
// short.
func DocCommentFor(src, symbol string) (string, bool) {
	lines := strings.Split(src, "\n")
	decl := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), symbol) {
			decl = i
			break
		}
	}
	if decl < 0 {
		return "", false
	}
	start := decl
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "//") {
		start--
	}
	if start == decl {
		return "", true // declared, but carries no doc comment at all
	}
	return strings.Join(lines[start:decl], "\n"), true
}
