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
// "n.a." is the spelling #1367 retired in favour of "n/a" (retired-spelling-ok).
// Lines that must
// name it carry the marker `retired-spelling-ok`, which
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
// says nothing about a renderer that still prints it. This walks every Go and
// JS source file the onboarding factory owns and fails on any surviving
// literal.
//
// Lines that must legitimately name the retired spelling (the schema that
// records it as retired, the migration fixtures, this file) opt out with the
// marker `retired-spelling-ok`.
func TestNoSourceEmitsRetiredSpelling(t *testing.T) {
	// Assembled at runtime so this line is not itself a hit.
	retired := "n" + ".a."
	const optOut = "retired-spelling-ok"

	factoryRoot := filepath.Join("..", "..") // internal/matrix → tools/onboarding-factory
	var hits []string

	err := filepath.WalkDir(factoryRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".js":
		default:
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, retired) && !strings.Contains(line, optOut) {
				hits = append(hits, filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", factoryRoot, err)
	}
	if len(hits) > 0 {
		t.Fatalf("%d source line(s) still carry the retired %q spelling (#1367 canonicalised it to %q); a line that must name it should carry the %q marker:\n  %s",
			len(hits), retired, "n/a", optOut, strings.Join(hits, "\n  "))
	}
}
