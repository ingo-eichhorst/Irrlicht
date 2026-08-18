package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Issue #1518: #1503 machine-generated the census so the DECLARATION could not
// go stale. Nothing constrains the PROSE around it, and #1511 — the PR that
// landed the generator — committed the very slip it was fixing four more times
// in its own doc comments, written by an author who had spent the whole PR
// thinking about nothing else. That is the argument for a mechanical check
// rather than more care: prose discipline does not hold even under maximum
// attention.
//
// The check is deliberately crude. It cannot know whether a number in a
// sentence MEANS the divergence count, and it does not need to: if a bare
// integer equal to a live census figure sits in a comment, either it is that
// figure (and must derive, or name censusOfTheCommittedCatalog instead of
// copying it) or it is a coincidence (and must be reworded, or exempted with a
// reason). Both outcomes are improvements.
//
// # Where this runs, and why it is a Go test
//
// Beside the census, in the package the census lives in. The alternative was a
// tools/preflight.sh gate, and it is the worse of the two for the reason the
// ticket exists: a shell gate cannot read censusOfTheCommittedCatalog, so it
// would have to re-derive or re-type the figures it checks against — a second
// hand-maintained copy of the numbers, which is the defect, reproduced inside
// its own fix. A Go test in package main sees the live values for free, parses
// comments with go/parser rather than grepping (so a digit inside a string
// literal or an identifier is not a comment), and rides gates that already
// exist: `go test ./tools/onboarding-factory/... -race -count=1`, test.yml's
// onboarding-factory job, and tools/preflight.sh's `tools` group.
//
// # What "bare integer" and "checked figure" mean
//
// See bareIntegerSpans for the disqualifiers (each one carries the real
// spelling from this package that motivated it) and censusFigureFloor for why
// three of the seven census fields are not checked at all. The floor is the
// whole reason this ticket is cheap: without it the check flags every 0 and 1
// in the package's prose, and a check with that false-positive rate gets
// suppressed wholesale, which is worse than no check.
//
// # Measured false-positive rate, on the catalog as of this commit
//
// TestCensusFigureFloorIsCalibrated prints both figures on every run rather
// than this comment restating them — which is the rule this file enforces,
// applied to itself.

// censusFigureFloor is the value below which a census figure is not checked.
//
// It is not read off a trough; unlike driftThreshold there isn't one, and
// saying so is the honest version. It is placed where the three
// STRUCTURALLY-small census fields drop out: Zero, Fabricated and
// UnpairedSidecars are defect counters driven toward zero by design, so they
// can never be distinctive fingerprints, and checking them costs dozens of
// false positives for a true-positive rate of nothing. Their current values
// collide with more comment sites in this package than every other census
// figure combined, by more than an order of magnitude.
//
// TestCensusFigureFloorIsCalibrated measures both walls and prints the
// collision-density table the choice rests on. The walls pin the floor to a
// band rather than to a point, and no value inside that band behaves
// differently against today's catalog — the same honesty driftThreshold's
// "the exact value cannot change the verdict" carries. What the walls DO
// establish is that the floor is neither decorative (lowering it to zero
// admits a large real population of noise) nor over-tight (every field it
// skips is measurably noisy, so it is not skipping a figure that would have
// been clean).
const censusFigureFloor = 50

// censusFigureExemption names one comment site whose bare integer happens to
// equal a live census figure and is NOT that figure.
//
// It carries a reason rather than a bare suppression, the shape
// applyWritesNoUserFile and nilTolerant use elsewhere in this repo, and its key
// is existence-checked in BOTH directions by
// TestNoCommentRestatesALiveCensusFigure: an entry that no longer names a real
// site fails, and so does one that names a site the check no longer flags. The
// second is the #1480 ratchet — a pinned entry left to rot is a failure, not a
// no-op — and it fires when a census figure moves off the value it collided
// with, which is exactly when the reason stops being true.
//
// Marker is a distinctive fragment of the comment LINE rather than a line
// number, because a line number churns on every edit above it and would make
// this map high-maintenance for reasons unrelated to what it claims.
type censusFigureExemption struct {
	// File is the base name of the file in this package.
	File string
	// Marker identifies the comment line, by a fragment unique within File.
	Marker string
	// Reason says why the integer is not the census figure it equals.
	Reason string
}

// coincidentalCensusFigures is every site in this package where the check fires
// on a number that is not a census figure.
//
// All three are rows of a committed measurement table whose surrounding prose
// already declares itself historical, which is why the reason is checkable
// rather than a matter of taste. A rule skipping table-shaped comment lines
// would remove all three and was rejected: it would also blind the check to a
// real figure quoted in a table, and three reasons a reader can verify are
// cheaper than one rule they cannot.
var coincidentalCensusFigures = []censusFigureExemption{
	{
		File:   "timing_drift.go",
		Marker: "1-5s        85",
		Reason: "a bucket count from #1480's |delta| histogram, measured over the " +
			"826 kind-matched pairs the catalog held then. Its own block says it " +
			"is 'quoted here as of that measurement, not as a current count'. It " +
			"coincides with PairedButUngraded and has nothing to do with it.",
	},
	{
		File:   "replay_sidecar.go",
		Marker: "+ 28ms cluster (rejected)",
		Reason: "the divergent column of a REJECTED cluster-window configuration " +
			"in #1478's calibration table. The block above it says the columns " +
			"'no longer equal the live gate's figures' and to read the table as a " +
			"comparison between rows. It coincides with DivergentByCountsAndKinds.",
	},
	{
		File:   "replay_sidecar.go",
		Marker: "+ 69ms cluster (rejected)",
		Reason: "the divergent column of another rejected row of the same #1478 " +
			"table, coinciding with Divergent. That two rejected rows collide " +
			"with two different census fields is coincidence twice over, which is " +
			"the strongest argument available that neither is a restatement.",
	},
}

// WHICH rows collide is itself evidence, and #1388 is the run that showed it.
// Adding two codex recordings moved Divergent and DivergentByCountsAndKinds each
// up by one, and every collision moved WITH them, one row along a table that has
// not changed: the 28ms row stopped colliding with anything and its entry was
// deleted per this check's own advice, the 69ms row changed which field it
// shadows, and the 2ms row started colliding and needed a new entry. A table that
// restated a live figure could not behave that way — it would have tracked the
// figure rather than being overtaken by it. The existence check on both sides is
// what surfaced all three moves in one run instead of leaving two of them to rot.
//
// #1695 is the same movement in the opposite direction and is worth recording
// for that reason: both fields fell by one (one recording stopped diverging when
// replay began honouring the Stop hook), and every collision walked back up the
// same table — the 2ms entry stopped suppressing anything and was deleted, the
// 28ms row started colliding and regained an entry, and the 69ms row swapped
// fields again. Two runs, opposite signs, the same table, collisions tracking
// the figures both times. A restatement cannot do that in either direction.
//
// No figure is quoted in this paragraph, deliberately: the first draft of it did
// quote them and this very check reported five sites, in its own exemption file.

// censusFigureSite is one bare integer found in one comment line.
type censusFigureSite struct {
	File   string
	Line   int
	Value  int
	Fields []string
	Text   string
}

func (s censusFigureSite) String() string {
	return fmt.Sprintf("%s:%d: %d (= censusOfTheCommittedCatalog.%s) in %q",
		s.File, s.Line, s.Value, strings.Join(s.Fields, "/"), s.Text)
}

// figureSet decides which integer values a scan reports, and under which
// census field names.
//
// It is an interface for one reason: the density measurement in
// TestCensusFigureFloorIsCalibrated needs "every value", and the alternative
// was a second traversal of the comments written specially for it. Two
// traversals are free to disagree about what a bare integer is, and the one
// that is not under test would then be the one reporting the false-positive
// rate — the failure #1480 removed by making compareOrdered return its matched
// pairs instead of a count beside them.
type figureSet interface {
	fields(value int) ([]string, bool)
}

// censusFigures is the live set: value -> the census fields holding it.
type censusFigures map[int][]string

func (f censusFigures) fields(v int) ([]string, bool) { n, ok := f[v]; return n, ok }

// everyInteger matches every value, for the density measurement only.
type everyInteger struct{}

func (everyInteger) fields(int) ([]string, bool) { return nil, true }

// checkedCensusFigures splits a census into the figures worth checking and the
// ones the floor skips.
//
// The skipped list is returned rather than dropped so the skip can be REPORTED.
// A guard that quietly stops covering three of its seven inputs is
// indistinguishable from one that covers all seven and finds nothing, which is
// AGENTS.md's "a verification mechanism must fail loudly when it cannot run"
// in its local form: here it cannot USEFULLY run, and says so.
func checkedCensusFigures(c catalogCensus, floor int) (wanted censusFigures, skipped []censusField) {
	wanted = censusFigures{}
	for _, f := range c.fields() {
		if f.value < floor {
			skipped = append(skipped, f)
			continue
		}
		wanted[f.value] = append(wanted[f.value], f.name)
	}
	return wanted, skipped
}

// bareIntegerSpans returns the [start,end) byte spans of every BARE integer in
// text: a maximal run of ASCII digits that is not part of a larger token.
//
// Every disqualifier below is here because a real comment in this package
// spells a number that way, and the spelling is named beside it. The rule is
// purely lexical on purpose — the ticket is explicit that a check needing to
// parse meaning out of English is not this ticket.
func bareIntegerSpans(text string) [][2]int {
	var out [][2]int
	for i := 0; i < len(text); {
		if !isASCIIDigit(text[i]) {
			i++
			continue
		}
		j := i
		for j < len(text) && isASCIIDigit(text[j]) {
			j++
		}
		if !disqualifiedBefore(text, i) && !disqualifiedAfter(text, j) {
			out = append(out, [2]int{i, j})
		}
		i = j
	}
	return out
}

// disqualifiedBefore reports whether the byte preceding start makes the digit
// run part of a larger token.
func disqualifiedBefore(text string, start int) bool {
	if start == 0 {
		return false
	}
	switch b := text[start-1]; {
	case isASCIIDigit(b) || isASCIILetter(b) || b == '_':
		return true // p50, v2, sub_5
	case b == '#':
		return true // #1503, an issue reference
	case b == '.':
		return true // 0.1-1s, v1.2.3
	case b == '/':
		return true // codex/1-1_session-start
	case b == '-':
		return true // lifetime-1, and the tail of a 1-5s range
	case b == '+':
		return true // +2.00s, a signed delta
	case b == '=':
		return true // UPDATE_REPLAY_GOLDENS=1, -count=1
	case b == ':':
		return true // 20:36:37.440, a clock time
	case b == '%':
		return true // %-3d, a format verb
	case b == '$':
		return true // $1, a shell positional
	default:
		return false
	}
}

// disqualifiedAfter reports whether the byte following end makes the digit run
// part of a larger token.
//
// '.' and '-' disqualify only when a digit follows them, because a figure at
// the end of a sentence ("...reached 4242.") and a figure before an em-dash are
// both bare, and excluding them outright would blind the check to the most
// ordinary spelling of all.
func disqualifiedAfter(text string, end int) bool {
	if end >= len(text) {
		return false
	}
	next := func(k int) byte {
		if end+k < len(text) {
			return text[end+k]
		}
		return 0
	}
	switch b := text[end]; {
	case isASCIIDigit(b) || isASCIILetter(b) || b == '_':
		return true // 2ms, 42px, 85_
	case b == '%':
		return true // 10.3%
	case b == ':':
		return true // 20:36:37
	case b == '/':
		return true // 30/06, a path or date
	case b == '.' && isASCIIDigit(next(1)):
		return true // 10.3, a decimal
	case b == '-' && isASCIIDigit(next(1)):
		return true // 1-5s, a range
	default:
		return false
	}
}

func isASCIIDigit(b byte) bool  { return b >= '0' && b <= '9' }
func isASCIILetter(b byte) bool { return (b|0x20) >= 'a' && (b|0x20) <= 'z' }

// scanCommentsForFigures reports every bare integer in src's COMMENTS whose
// value is one of wanted's keys.
//
// Comments only, via go/parser rather than a grep, because a digit in a string
// literal or an identifier is code and is not a claim about the catalog —
// testdata/censuslint/code-not-comment.go.txt pins that.
func scanCommentsForFigures(filename string, src []byte, wanted figureSet) ([]censusFigureSite, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var sites []censusFigureSite
	for _, group := range file.Comments {
		for _, c := range group.List {
			first := fset.Position(c.Slash).Line
			for _, span := range bareIntegerSpans(c.Text) {
				value, err := strconv.Atoi(c.Text[span[0]:span[1]])
				if err != nil {
					continue // an integer too wide for int is not a census figure
				}
				fields, ok := wanted.fields(value)
				if !ok {
					continue
				}
				sites = append(sites, censusFigureSite{
					File:   filepath.Base(filename),
					Line:   first + strings.Count(c.Text[:span[0]], "\n"),
					Value:  value,
					Fields: fields,
					Text:   commentLineAt(c.Text, span[0]),
				})
			}
		}
	}
	return sites, nil
}

// commentLineAt returns the single line of a (possibly multi-line) comment that
// contains the byte at off, trimmed.
func commentLineAt(text string, off int) string {
	start := strings.LastIndexByte(text[:off], '\n') + 1
	end := strings.IndexByte(text[off:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += off
	}
	return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(text[start:end]), "/*"))
}

// packageGoFiles reads this package's own .go files. It is non-recursive, so
// testdata/ is excluded by construction rather than by a name check.
func packageGoFiles(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		files[e.Name()] = src
	}
	// Fail loudly rather than passing on nothing: a walk that reached no
	// source reads exactly like a package with no stale figures in it.
	if len(files) < 10 {
		t.Fatalf("the walk reached only %d .go files in this package, which is fewer than "+
			"the package has ever had — the scan is broken, not the prose", len(files))
	}
	return files
}

// scanPackage returns every flagged site in this package, in file:line order.
func scanPackage(t *testing.T, wanted figureSet) []censusFigureSite {
	t.Helper()
	var sites []censusFigureSite
	for name, src := range packageGoFiles(t) {
		found, err := scanCommentsForFigures(name, src, wanted)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		sites = append(sites, found...)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})
	return sites
}

// TestNoCommentRestatesALiveCensusFigure is #1518: no comment in this package
// may carry a bare integer equal to a live census figure, unless
// coincidentalCensusFigures says why it is not that figure.
func TestNoCommentRestatesALiveCensusFigure(t *testing.T) {
	wanted, skipped := checkedCensusFigures(censusOfTheCommittedCatalog, censusFigureFloor)
	if len(wanted) == 0 {
		t.Fatalf("censusFigureFloor (%d) is above every census figure, so this check "+
			"covers nothing and would pass over any prose whatsoever", censusFigureFloor)
	}
	for _, f := range skipped {
		t.Logf("not checked: %s = %d is below censusFigureFloor (%d) — see the constant's "+
			"doc comment for why a structurally-small figure is not a fingerprint",
			f.name, f.value, censusFigureFloor)
	}

	sites := scanPackage(t, wanted)

	// Exemptions first, so the two existence checks can see which sites each
	// entry actually claims.
	claimed := make([][]censusFigureSite, len(coincidentalCensusFigures))
	var unexplained []censusFigureSite
	for _, s := range sites {
		matched := false
		for i, ex := range coincidentalCensusFigures {
			if s.File == ex.File && strings.Contains(s.Text, ex.Marker) {
				claimed[i] = append(claimed[i], s)
				matched = true
			}
		}
		if !matched {
			unexplained = append(unexplained, s)
		}
	}

	files := packageGoFiles(t)
	for i, ex := range coincidentalCensusFigures {
		if _, ok := files[ex.File]; !ok {
			t.Errorf("coincidentalCensusFigures[%d] names %s, which is not a .go file in this "+
				"package — an exemption that stopped naming a real site must fail, not "+
				"quietly pass. Reason on file: %s", i, ex.File, ex.Reason)
			continue
		}
		if len(claimed[i]) == 0 {
			t.Errorf("coincidentalCensusFigures[%d] (%s, %q) suppresses nothing. Either the "+
				"comment was reworded or the census figure it collided with moved, so the "+
				"reason no longer describes anything. Delete the entry rather than leaving "+
				"it to rot. Reason on file: %s", i, ex.File, ex.Marker, ex.Reason)
		}
	}

	if len(unexplained) > 0 {
		var b strings.Builder
		for _, s := range unexplained {
			fmt.Fprintf(&b, "\n  %s", s)
		}
		t.Errorf("%d comment site(s) in this package restate a live census figure by hand:%s\n\n"+
			"Each is one of two things and both have a fix. If the number IS the figure, "+
			"name censusOfTheCommittedCatalog.<Field> instead of copying its value — that is "+
			"what #1503 machine-generated it for. If it is a coincidence, reword it or add a "+
			"censusFigureExemption naming the site and saying why.",
			len(unexplained), b.String())
	}
}

// corpusCensus is the synthetic census the corpus runs against.
//
// Synthetic, never censusOfTheCommittedCatalog, because a corpus pinned to the
// live figures would go stale the first time the catalog moves — this lint's
// own defect, reproduced inside its own evidence. The values are chosen to
// straddle censusFigureFloor in both directions and to make one value shared by
// two fields.
var corpusCensus = catalogCensus{
	Recordings:                4242,
	Zero:                      censusFigureFloor, // exactly on the floor: checked
	Fabricated:                7,                 // far below: skipped
	Divergent:                 7373,
	DivergentByCountsAndKinds: 8888,
	UnpairedSidecars:          censusFigureFloor - 1, // just below: skipped
	PairedButUngraded:         4242,                  // shares Recordings' value
}

// TestCensusFigureLintCorpus is the committed mutation evidence: one fixture per
// spelling, pinned to the verdict the detector must return.
//
// Roughly half the rows are want:none, and they carry as much of the value as
// the flagged ones. A detector that reported every digit run would satisfy every
// flagged row and read as excellent coverage — #1450's lesson, and the specific
// failure the ticket predicted ("the natural implementation flags every 0, 1
// and 2 in the package").
func TestCensusFigureLintCorpus(t *testing.T) {
	type want struct {
		value  int
		line   int
		fields []string
	}
	cases := []struct {
		file string
		// plants are fragments the fixture must still contain. A corpus that
		// quietly stops carrying its own test cases reads as a pass.
		plants []string
		want   []want
	}{
		{
			file:   "stale-figure.go.txt",
			plants: []string{"reached 4242 recordings"},
			want:   []want{{4242, 3, []string{"Recordings", "PairedButUngraded"}}},
		},
		{
			file: "not-a-figure.go.txt",
			plants: []string{
				"826 kind-matched", "#4242", "4242ms", "0.4242", "4242-7373",
				"codex/4242_basic-turn", "4242:00", "-count=4242", "p4242", "+4242",
			},
			want: nil,
		},
		{
			file:   "below-floor.go.txt",
			plants: []string{"Fabricated is 7 ", "UnpairedSidecars is 49"},
			want:   nil,
		},
		{
			file:   "at-floor.go.txt",
			plants: []string{"at 50,"},
			want:   []want{{50, 3, []string{"Zero"}}},
		},
		{
			file:   "table-row.go.txt",
			plants: []string{"as shipped", "3       7373"},
			want:   []want{{7373, 10, []string{"Divergent"}}},
		},
		{
			file:   "code-not-comment.go.txt",
			plants: []string{"const recordings = 4242", `"the walk reached 4242 recordings"`},
			want:   nil,
		},
		{
			file:   "two-on-one-line.go.txt",
			plants: []string{"4242 recordings of which 7373 diverge"},
			want: []want{
				{4242, 3, []string{"Recordings", "PairedButUngraded"}},
				{7373, 3, []string{"Divergent"}},
			},
		},
		{
			file:   "block-comment.go.txt",
			plants: []string{"4242 sits here"},
			want:   []want{{4242, 5, []string{"Recordings", "PairedButUngraded"}}},
		},
		{
			file:   "shared-value.go.txt",
			plants: []string{"4242 is both Recordings and PairedButUngraded"},
			want:   []want{{4242, 3, []string{"Recordings", "PairedButUngraded"}}},
		},
	}

	wanted, skipped := checkedCensusFigures(corpusCensus, censusFigureFloor)
	if got, want := len(skipped), 2; got != want {
		t.Fatalf("the corpus census is meant to place %d figures below censusFigureFloor "+
			"and places %d — the floor rows below assert nothing", want, got)
	}

	flagged := 0
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("testdata", "censuslint", tc.file)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading the fixture: %v", err)
			}
			for _, plant := range tc.plants {
				if !strings.Contains(string(src), plant) {
					t.Fatalf("the fixture no longer contains %q, so whatever it now asserts "+
						"is not the case it was written for", plant)
				}
			}
			got, err := scanCommentsForFigures(path, src, wanted)
			if err != nil {
				t.Fatalf("parsing the fixture: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %d finding(s), got %d: %v", len(tc.want), len(got), got)
			}
			for i, w := range tc.want {
				if got[i].Value != w.value {
					t.Errorf("finding %d: want value %d, got %d", i, w.value, got[i].Value)
				}
				if got[i].Line != w.line {
					t.Errorf("finding %d: want line %d, got %d", i, w.line, got[i].Line)
				}
				if strings.Join(got[i].Fields, ",") != strings.Join(w.fields, ",") {
					t.Errorf("finding %d: want fields %v, got %v", i, w.fields, got[i].Fields)
				}
			}
		})
		flagged += len(tc.want)
	}

	// Vacuity guard: a detector that reported nothing at all would satisfy
	// every want:none row above, which is most of them.
	if flagged == 0 {
		t.Fatal("no corpus case expects a finding, so a detector that reports nothing " +
			"passes the whole corpus")
	}
	if flagged == len(cases) {
		t.Fatal("every corpus case expects a finding, so a detector that reports " +
			"everything passes the whole corpus — the want:none rows are the evidence")
	}
}

// TestCensusFigureFloorIsCalibrated measures both walls the floor sits between
// and prints the collision-density table it was chosen from.
//
// It prints rather than pinning the figures in a doc comment, for this file's
// own reason: a number that documents behaviour but is not produced by it
// drifts silently and is then quoted with full confidence.
func TestCensusFigureFloorIsCalibrated(t *testing.T) {
	files := packageGoFiles(t)

	// collisions[v] is how many comment sites in this package carry a bare v.
	// Measured by asking the detector for EVERY value, which is the floor
	// lowered to nothing and widened to everything.
	collisions := map[int]int{}
	for name, src := range files {
		sites, err := scanCommentsForFigures(name, src, everyInteger{})
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, s := range sites {
			collisions[s.Value]++
		}
	}

	wanted, skipped := checkedCensusFigures(censusOfTheCommittedCatalog, censusFigureFloor)

	t.Log("census figures and whether censusFigureFloor lets them be checked:")
	for _, f := range censusOfTheCommittedCatalog.fields() {
		state := "checked"
		if f.value < censusFigureFloor {
			state = "SKIPPED (below the floor)"
		}
		t.Logf("  %-26s %6d  collides with %3d comment site(s)  %s",
			f.name, f.value, collisions[f.value], state)
	}

	t.Log("bare-integer collision density in this package's comments — occurrences " +
		"per candidate value, which is the expected false-positive cost of a census " +
		"figure landing anywhere in the bucket:")
	buckets := []struct{ lo, hi int }{
		{0, 9}, {10, 19}, {20, 49}, {50, 99}, {100, 199}, {200, 499}, {500, 999},
	}
	for _, b := range buckets {
		occurrences := 0
		for v, n := range collisions {
			if v >= b.lo && v <= b.hi {
				occurrences += n
			}
		}
		span := b.hi - b.lo + 1
		t.Logf("  value %4d-%-4d  %4d occurrence(s) over %4d candidate value(s)  %.2f per value",
			b.lo, b.hi, occurrences, span, float64(occurrences)/float64(span))
	}

	// Lower wall: the floor must be buying something. With it at zero the
	// check flags a large population that is entirely noise — no census figure
	// below the floor has ever been restated in this package's prose, because
	// a defect counter at 0 or 1 is not a number anyone quotes.
	atFloor := len(scanPackage(t, wanted))
	unfloored, _ := checkedCensusFigures(censusOfTheCommittedCatalog, 0)
	atZero := len(scanPackage(t, unfloored))
	t.Logf("flagged sites: %d with censusFigureFloor, %d with the floor at zero", atFloor, atZero)
	if atZero-atFloor < 10 {
		t.Errorf("dropping censusFigureFloor to zero adds only %d site(s), so the floor is "+
			"decorative — it is meant to be suppressing a large noise population, and if "+
			"it no longer is, delete it rather than keeping an unexplained constant",
			atZero-atFloor)
	}

	// Upper wall: the floor must not be skipping a figure that would have been
	// clean. Every field it skips has to be measurably noisy. Raising the floor
	// past the smallest checked figure trips this immediately, which is what
	// stops it from creeping upward until the check covers nothing.
	if len(skipped) == 0 {
		t.Error("censusFigureFloor skips no census figure at all, so the lower wall above " +
			"is the only thing holding it and it could be any value whatsoever")
	}
	const noisyEnough = 5
	for _, f := range skipped {
		if collisions[f.value] < noisyEnough {
			t.Errorf("censusFigureFloor (%d) skips %s = %d, but that value collides with only "+
				"%d comment site(s) — fewer than %d, so the skip costs coverage without "+
				"buying quiet. Lower the floor.",
				censusFigureFloor, f.name, f.value, collisions[f.value], noisyEnough)
		}
	}
}
