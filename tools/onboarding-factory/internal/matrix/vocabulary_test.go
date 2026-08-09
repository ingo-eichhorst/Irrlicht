package matrix

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The vocabulary tests in this file assert against HARD-CODED literals rather
// than against the schema constants they pin. That is deliberate: a test
// written as `got != StateNotApplicable` passes no matter what
// StateNotApplicable is later changed to, so it cannot catch the one failure
// mode #1367 is about — the canonical token drifting. Here, the literal is the
// spec.
//
// The dotted spelling #1367 retired in favour of "n/a" is named on lines
// carrying the marker `retired-spelling-ok`, which
// TestNoSourceEmitsRetiredSpelling honours.

// canonicalDisplayStates is the display-state vocabulary as #1367 fixes it,
// spelled out independently of the production constants for the reason above.
var canonicalDisplayStates = map[string]bool{
	"observed": true, "pending-record": true,
	"blocked-daemon": true, "blocked-driver": true,
	"unobservable": true, "n/a": true, "unknown": true,
}

// TestDeriveDisplayStateNeverEmitsRetiredSpelling walks the full cross-product
// of the three axes plus both booleans and asserts every derived state is a
// member of the canonical vocabulary — in particular that it is never the
// retired dotted spelling. This is the "no code path still produces it" half
// of the #1367 guarantee; TestNoSourceEmitsRetiredSpelling is the "no source
// literal survives" half.
func TestDeriveDisplayStateNeverEmitsRetiredSpelling(t *testing.T) {
	retired := "n.a." // retired-spelling-ok
	supports := []string{"yes", "partial", "no", "unknown", "", "n/a", retired}
	daemons := []string{"full", "bug", "incapable", "unknown", "", "n/a", retired}
	drivers := []string{"ready", "gap:interrupt", "unknown", "", "n/a", retired}

	for _, s := range supports {
		for _, d := range daemons {
			for _, dr := range drivers {
				for _, rec := range []bool{true, false} {
					for _, appl := range []bool{true, false} {
						got := DeriveDisplayState(s, d, dr, rec, appl)
						if got == retired {
							t.Fatalf("DeriveDisplayState(%q,%q,%q,rec=%v,applic=%v) = %q; #1367 retired the dotted spelling in favour of %q",
								s, d, dr, rec, appl, got, "n/a")
						}
						if !canonicalDisplayStates[got] {
							t.Fatalf("DeriveDisplayState(%q,%q,%q,rec=%v,applic=%v) = %q; not a member of the display-state vocabulary",
								s, d, dr, rec, appl, got)
						}
					}
				}
			}
		}
	}
}

// TestDeriveDisplayStateNotApplicableCases pins the three routes that reach the
// not-applicable state, so the rename cannot quietly turn one of them into a
// different bucket (e.g. "unknown", which would under-report remaining work).
func TestDeriveDisplayStateNotApplicableCases(t *testing.T) {
	cases := []struct {
		name                    string
		supports, daemon, drive string
		rec, applic             bool
	}{
		{"agent does not support the feature", "no", "full", "ready", true, true},
		{"daemon axis is not applicable", "yes", "n/a", "ready", true, true},
		{"recipe defers the cell", "yes", "full", "ready", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveDisplayState(c.supports, c.daemon, c.drive, c.rec, c.applic); got != "n/a" {
				t.Errorf("DeriveDisplayState(%q,%q,%q,rec=%v,applic=%v) = %q; want %q",
					c.supports, c.daemon, c.drive, c.rec, c.applic, got, "n/a")
			}
		})
	}
}

// TestNoSourceEmitsRetiredSpelling is the census this ticket actually turns on.
// #1367's product is a vocabulary, so the failure mode is a MISSED call site,
// not broken logic — and a schema that rejects the retired spelling on disk
// says nothing about a renderer that still prints it. This walks the whole
// repository and fails on any surviving literal.
//
// It is rooted at the REPO, not at tools/onboarding-factory, because the
// spelling reaches beyond the factory's own sources: #1367 itself had to fix
// .claude/skills/ir:onboarding-factory/SKILL.md, which a factory-scoped,
// Go-and-JS-only walk would have missed entirely. Shell gates, the viewer's
// index.html and the docs are all places the vocabulary can re-enter.
//
// replaydata/ is skipped wholesale: captured recordings are frozen fixtures,
// and stored assessments carry free-text body/notes prose that legitimately
// quotes the superseded verdict. The FIELD values there are gated by
// `of validate` instead, which is the right tool for data.
//
// Lines that must legitimately name the retired spelling (the schema that
// records it as retired, the migration fixtures, this file) opt out with the
// marker `retired-spelling-ok`.
// retiredScanExts are the source kinds the census reads. Extensions rather
// than a name list so a new file type is covered by default.
var retiredScanExts = map[string]bool{
	".go": true, ".js": true, ".md": true, ".sh": true, ".html": true,
	".swift": true, ".json": true, ".yml": true, ".yaml": true,
}

// retiredScanSkipDirs are directories the census never descends into.
// worktrees holds other agents' checkouts of this same repo; replaydata holds
// frozen recordings plus assessment prose that legitimately quotes the
// superseded verdict (its FIELD values are gated by `of validate` instead).
var retiredScanSkipDirs = map[string]bool{
	".git": true, ".build": true, "node_modules": true,
	"testdata": true, "replaydata": true, "worktrees": true,
}

// scanRetired reports the lines of one file that name the retired spelling
// without the opt-out marker (hits), and — in production Go only — the lines
// that carry the marker on executable code rather than in a comment
// (markerOnCode). The opt-out is a whole-line substring, so a live branch on
// the retired token with a trailing marker comment would otherwise be hidden
// by it: documenting the retired spelling is legitimate, executing on it is not.
func scanRetired(path, content, retired, optOut string, enforceMarkerPlacement bool) (hits, markerOnCode []string) {
	for i, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, retired) {
			continue
		}
		loc := filepath.ToSlash(path) + ":" + strconv.Itoa(i+1) + ": " + strings.TrimSpace(line)
		switch {
		case !strings.Contains(line, optOut):
			hits = append(hits, loc)
		case enforceMarkerPlacement && !strings.HasPrefix(strings.TrimSpace(line), "//"):
			markerOnCode = append(markerOnCode, loc)
		}
	}
	return hits, markerOnCode
}

func TestNoSourceEmitsRetiredSpelling(t *testing.T) {
	retired := "n.a." // retired-spelling-ok
	const optOut = "retired-spelling-ok"

	// internal/matrix → tools/onboarding-factory → tools → repo root.
	repoRoot := filepath.Join("..", "..", "..", "..")
	var hits, markerOnCode []string

	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && retiredScanSkipDirs[d.Name()]:
			return filepath.SkipDir
		case d.IsDir() || !retiredScanExts[filepath.Ext(path)]:
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// vocabulary.go is the schema itself: its retiredSpellings registry is
		// the one place production code must name the retired token as data.
		isSchema := strings.HasSuffix(filepath.ToSlash(path), "internal/matrix/vocabulary.go")
		prodGo := filepath.Ext(path) == ".go" && !strings.HasSuffix(path, "_test.go") && !isSchema

		fileHits, fileMarkers := scanRetired(path, string(b), retired, optOut, prodGo)
		hits = append(hits, fileHits...)
		markerOnCode = append(markerOnCode, fileMarkers...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
	if len(hits) > 0 {
		t.Fatalf("%d source line(s) still carry the retired %q spelling (#1367 canonicalised it to %q); a line that must name it should carry the %q marker:\n  %s",
			len(hits), retired, "n/a", optOut, strings.Join(hits, "\n  "))
	}
	if len(markerOnCode) > 0 {
		t.Fatalf("%d production Go line(s) use the %q marker on executable code, which would hide a live call site — the marker is only honoured on comment lines there:\n  %s",
			len(markerOnCode), optOut, strings.Join(markerOnCode, "\n  "))
	}
}

// TestDeriveDisplayStateRejectsOffVocabularyAxes pins the fail-safe added after
// the #1367 review. An axis value outside the vocabulary used to fall THROUGH
// the switch to the optimistic arms, so a malformed cell derived to "observed"
// and silently inflated the coverage numbers — the worst possible default.
//
// Note the cross-product test above cannot catch this on its own: it feeds a
// bad value in, gets "observed" out, and "observed" IS in the canonical set.
func TestDeriveDisplayStateRejectsOffVocabularyAxes(t *testing.T) {
	cases := []struct {
		name                    string
		supports, daemon, drive string
	}{
		{"retired daemon spelling", "yes", "n.a.", "ready"}, // retired-spelling-ok
		{"garbage daemon", "yes", "garbage", "ready"},
		{"garbage supports", "yep", "full", "ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// hasRecording=true is the case that used to read "observed".
			if got := DeriveDisplayState(c.supports, c.daemon, c.drive, true, true); got != "unknown" {
				t.Errorf("DeriveDisplayState(%q,%q,%q,rec=true,applic=true) = %q; an off-vocabulary axis must read %q, not be treated as onboarded",
					c.supports, c.daemon, c.drive, got, "unknown")
			}
		})
	}
}

// TestViewerRendersEveryDisplayState is the POSITIVE half of the
// cross-language guarantee. The census asserts no source names the retired
// token; this asserts the viewer's JavaScript handles every state the Go
// schema defines.
//
// Without it, adding a state to DisplayStates renders every such cell as the
// grey "unknown" fallback in the matrix — silent mis-bucketing, and exactly
// the drift class #1367 is about, just across the language boundary instead of
// between two Go files. The colours live only in JS, so there is nothing to
// generate from; a test is the available seam.
func TestViewerRendersEveryDisplayState(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "viewer", "web", "viewer.js"))
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	// Scope to _displayMeta: other switches (_axisBadge, _daemonBadge) match on
	// the assessment axes and share some token spellings, so a whole-file
	// search would report a false pass.
	body := string(src)
	start := strings.Index(body, "function _displayMeta(")
	if start < 0 {
		t.Fatal("viewer.js no longer defines _displayMeta — update this test with the renderer that replaced it")
	}
	end := strings.Index(body[start:], "\n}")
	if end < 0 {
		t.Fatal("could not find the end of _displayMeta")
	}
	block := body[start : start+end]

	for _, state := range DisplayStates {
		if state == StateUnknown {
			// unknown is the default arm rather than an explicit case.
			if !strings.Contains(block, "default:") {
				t.Errorf("_displayMeta has no default arm, so %q would not render", StateUnknown)
			}
			continue
		}
		if !strings.Contains(block, `case "`+state+`":`) {
			t.Errorf("_displayMeta has no `case %q:` — cells in that state will render as the grey unknown fallback", state)
		}
	}
}
