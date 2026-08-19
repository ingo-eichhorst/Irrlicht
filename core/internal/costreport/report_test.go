package costreport

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// figureRE matches a rendered FIGURE — the `median <n>ms` form Row.Render
// produces — and nothing else.
//
// Keying on the form rather than on "does this line contain a number" is not
// laxness, and the first draft got it wrong in the informative direction: a bare
// `[0-9]+\.[0-9]+ms` fired on the refusal that rejects a non-positive sample,
// because that refusal QUOTES the sample it rejected ("sample 1 of 1 measured
// -0.5000ms"). The quote is evidence and should stay; what must be absent from a
// refused row is a number a reader could carry off as the probe's cost.
var figureRE = regexp.MustCompile(`median [0-9]+\.[0-9]+ms`)

const testWidth = 20

// TestRowRefusesRatherThanPrintingZeros is the corpus #1572 asks for, in the
// form AGENTS.md prefers: committed beside the assertion rather than described
// in a merged PR body, one deliberately-unmeasurable input per shape, plus the
// vacuity guard without which a reporter that refused EVERYTHING would satisfy
// every row here and read as excellent coverage.
func TestRowRefusesRatherThanPrintingZeros(t *testing.T) {
	cases := []struct {
		what string
		row  Row
		// wantRefusal is a fragment of the reason the row must give. Empty means
		// the row must produce a FIGURE — the vacuity guard.
		wantRefusal string
	}{
		{
			what:        "nil samples: the probe was planned and nothing came back",
			row:         Measured("plutil.bundle_id", nil, "Finder.app"),
			wantRefusal: "collected 0 samples",
		},
		{
			what:        "an empty but non-nil slice is the same fact",
			row:         Measured("ps.proc_info", []float64{}, ""),
			wantRefusal: "collected 0 samples",
		},
		{
			what:        "a binary this machine does not have",
			row:         Refused("kitten.window", "no KITTY_LISTEN_ON in this environment"),
			wantRefusal: "no KITTY_LISTEN_ON",
		},
		{
			what:        "a zero-valued sample is a broken measurement, not a fast exec",
			row:         Measured("lsof.cwd", []float64{4.1, 0, 4.4}, ""),
			wantRefusal: "the measurement broke",
		},
		{
			what:        "a negative sample likewise",
			row:         Measured("lsof.writer", []float64{-0.5}, ""),
			wantRefusal: "the measurement broke",
		},
		{
			what:        "a refusal with no reason still says that much",
			row:         Refused("pgrep.discover", "   "),
			wantRefusal: "refused with no reason given",
		},
		{
			// The vacuity guard. Without it every assertion above is satisfied
			// by a Render that returns the refusal line unconditionally.
			what: "real samples produce a real figure",
			row:  Measured("ps.tty", []float64{3.0, 4.0, 5.0}, "self"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			line := tc.row.Render(testWidth)
			if tc.wantRefusal == "" {
				if strings.Contains(line, Marker) {
					t.Fatalf("a row built from real samples rendered as %s: %q", Marker, line)
				}
				if !figureRE.MatchString(line) {
					t.Fatalf("a row built from real samples rendered no figure: %q", line)
				}
				for _, want := range []string{"median 4.0ms", "min 3.0ms", "n=3"} {
					if !strings.Contains(line, want) {
						t.Errorf("figure is missing %q: %q", want, line)
					}
				}
				return
			}
			if !strings.Contains(line, Marker) {
				t.Errorf("row did not mark itself %s: %q", Marker, line)
			}
			if !strings.Contains(line, tc.wantRefusal) {
				t.Errorf("row did not name its reason (want a fragment %q): %q", tc.wantRefusal, line)
			}
			// The property, rather than the message: a row that cannot be
			// measured renders no number a reader could carry off as this
			// probe's cost.
			if m := figureRE.FindString(line); m != "" {
				t.Errorf("an unmeasurable row rendered the figure %q — this is the 0.0ms failure #1572 was filed about: %q", m, line)
			}
			for _, tok := range []string{"p99 ", "n="} {
				if strings.Contains(line, tok) {
					t.Errorf("an unmeasurable row rendered %q, which reads as part of a figure: %q", tok, line)
				}
			}
		})
	}
}

// rateAsShipped is the rendering #1572's own first generator produced, emitted
// verbatim and re-measured on every run so "integer division prints 0/commit" is
// a fact rather than a sentence in a merged PR body. It is the committed
// mutation for rateOf, and it doubles as that corpus's vacuity guard: a
// zeroShaped that reported nothing would leave every row below green.
//
// It was reproduced live before the fix, against this repository:
//
//	log --all --grep   median 200.2ms  (0 bytes out = 0/commit)
//	rev-parse HEAD     median  26.8ms  (41 bytes out = 0/commit)
//
// in a block that PASSED and was pasteable. The second line is the dangerous
// one: 41 is a real measurement, and a guard keyed on `bytes == 0` does not
// catch it.
func rateAsShipped(bytes, population int) string {
	return strconv.Itoa(bytes / population)
}

// TestRateRefusesRatherThanRoundingToZero is the fix's own corpus. Each row
// states what the SHIPPED rendering did and what this one must do instead, so a
// regression cannot be read as a style change.
func TestRateRefusesRatherThanRoundingToZero(t *testing.T) {
	cases := []struct {
		what       string
		bytes      int
		population int
		// wantShippedZero pins that the pre-fix rendering really was
		// zero-shaped here. Without it a "fixed" row and a row that was never
		// broken are indistinguishable, and the corpus would claim coverage it
		// does not have.
		wantShippedZero bool
		// wantRate is the exact figure this package must print, or "" when it
		// must refuse the rate by name.
		wantRate string
	}{
		{
			what:            "41 bytes over 3209 commits — the live reproduction, and the one the bytes==0 guard misses",
			bytes:           41,
			population:      3209,
			wantShippedZero: true,
			wantRate:        "0.0128",
		},
		{
			what:            "no output at all: the walk happened, the rate does not exist",
			bytes:           0,
			population:      3209,
			wantShippedZero: true,
			wantRate:        "",
		},
		{
			what:            "a population nobody counted is not a rate of zero either",
			bytes:           2611982,
			population:      0,
			wantShippedZero: false, // the shipped form panics here rather than printing a zero
			wantRate:        "",
		},
		{
			what:            "one byte under an absurd population still renders as a number, never as 0",
			bytes:           1,
			population:      1 << 40,
			wantShippedZero: true,
			wantRate:        "9.09e-13",
		},
		{
			// The vacuity guard: the case the shipped rendering got RIGHT must
			// still produce a figure, or "refuses more" would pass for "refuses
			// correctly".
			what:            "gitMaxOutput's own figure: 2,611,982 bytes over 1,386 commits",
			bytes:           2611982,
			population:      1386,
			wantShippedZero: false,
			wantRate:        "1885",
		},
	}

	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			if tc.population > 0 {
				gotShippedZero := zeroShaped(rateAsShipped(tc.bytes, tc.population))
				if gotShippedZero != tc.wantShippedZero {
					t.Fatalf("the pre-fix rendering of %d/%d is zero-shaped = %v, want %v — "+
						"this corpus row no longer reproduces the defect it exists to pin",
						tc.bytes, tc.population, gotShippedZero, tc.wantShippedZero)
				}
			}

			rate, ok := rateOf(tc.bytes, tc.population)
			if tc.wantRate == "" {
				if ok {
					t.Fatalf("rateOf(%d, %d) produced %q; it must refuse", tc.bytes, tc.population, rate)
				}
				return
			}
			if !ok {
				t.Fatalf("rateOf(%d, %d) refused a rate that exists", tc.bytes, tc.population)
			}
			if rate != tc.wantRate {
				t.Errorf("rateOf(%d, %d) = %q, want %q", tc.bytes, tc.population, rate, tc.wantRate)
			}
			if zeroShaped(rate) {
				t.Errorf("rateOf(%d, %d) rendered %q, which reads as zero", tc.bytes, tc.population, rate)
			}
		})
	}
}

// TestNoPositiveRateEverRendersAsZero is the property behind the table above.
// The table encodes what the author thought of; this sweeps the axis the defect
// actually lived on — the ratio — so a future format change that reintroduces
// rounding is caught by the axis rather than by someone adding the right row.
func TestNoPositiveRateEverRendersAsZero(t *testing.T) {
	populations := []int{1, 7, 1386, 3209, 100000, 1000000, 1 << 40}
	byteCounts := []int{1, 2, 41, 999, 2611982, 70659428}
	swept, shippedZeros := 0, 0
	for _, pop := range populations {
		for _, b := range byteCounts {
			swept++
			if zeroShaped(rateAsShipped(b, pop)) {
				shippedZeros++
			}
			rate, ok := rateOf(b, pop)
			if !ok {
				t.Errorf("rateOf(%d, %d) refused a rate that exists", b, pop)
				continue
			}
			if zeroShaped(rate) {
				t.Errorf("rateOf(%d, %d) rendered %q, which reads as zero", b, pop, rate)
			}
		}
	}
	// The sweep's own vacuity guard: it must actually have swept, and the axis
	// must actually contain the defect. A sweep over pairs the shipped rendering
	// got right would pass while proving nothing.
	if swept != len(populations)*len(byteCounts) {
		t.Fatalf("swept %d pairs, expected %d", swept, len(populations)*len(byteCounts))
	}
	if shippedZeros == 0 {
		t.Fatalf("none of the %d swept pairs is zero-shaped under the pre-fix rendering — "+
			"this sweep no longer covers the defect it was written for", swept)
	}
	t.Logf("swept %d (bytes, population) pairs; %d of them the pre-fix integer division rendered as zero", swept, shippedZeros)
}

// TestWithRateNamesWhatItCouldNotDo grades the rate as it reaches a reader —
// inside a rendered row — rather than only as a return value.
func TestWithRateNamesWhatItCouldNotDo(t *testing.T) {
	base := func() Row { return Measured("log --pretty=%B", []float64{80.0, 81.0, 82.0}, "this repo") }

	t.Run("a real rate is carried into the line", func(t *testing.T) {
		line := base().WithRate(2611982, 1386, "commits from HEAD").Render(testWidth)
		for _, want := range []string{"2,611,982 bytes", "/ 1,386 commits from HEAD", "= 1885 bytes each"} {
			if !strings.Contains(line, want) {
				t.Errorf("rendered line is missing %q: %q", want, line)
			}
		}
	})

	t.Run("a refused rate says NO RATE and prints no quotient", func(t *testing.T) {
		line := base().WithRate(0, 3209, "commits across all refs").Render(testWidth)
		if !strings.Contains(line, "NO RATE") {
			t.Errorf("a rate that could not be computed did not say so: %q", line)
		}
		if !strings.Contains(line, "cost of the WALK") {
			t.Errorf("a refused rate did not say what the row DOES report: %q", line)
		}
		// The property: the duration survives (the walk really was measured)
		// while nothing zero-shaped is attached to it.
		if !figureRE.MatchString(line) {
			t.Errorf("refusing the rate threw away the duration, which WAS measured: %q", line)
		}
		if strings.Contains(line, "= 0") || strings.Contains(line, "0 bytes each") {
			t.Errorf("a refused rate still rendered a zero: %q", line)
		}
	})

	t.Run("a rate cannot be attached to a row that already refused", func(t *testing.T) {
		line := Refused("log --pretty=%B", "git is not on this machine").WithRate(41, 3209, "commits from HEAD").Render(testWidth)
		if !strings.Contains(line, Marker) || strings.Contains(line, "bytes each") {
			t.Errorf("a refused row grew a rate: %q", line)
		}
	})
}

// TestRenderRefusesWhenNothingWasMeasured grades the block rather than the row.
// Splitting it is the point: a reporter can refuse every row correctly and still
// hand back a block that reads as a finished measurement.
func TestRenderRefusesWhenNothingWasMeasured(t *testing.T) {
	good := Measured("ps.proc_info", []float64{6.0, 6.6, 7.0}, "self")
	bad := Refused("tmux.list_clients", "no tmux socket in this environment")

	t.Run("no rows at all", func(t *testing.T) {
		if _, err := Render("empty", nil, nil); err == nil {
			t.Fatal("a report with no rows was accepted — a generator whose plan produced nothing must refuse")
		} else if !strings.Contains(err.Error(), "no rows at all") {
			t.Errorf("refusal does not name the shape: %v", err)
		}
	})

	t.Run("every row refused", func(t *testing.T) {
		_, err := Render("all refused", nil, []Row{bad, Refused("kitten.window", "no kitty")})
		if err == nil {
			t.Fatal("a report in which nothing was measured was accepted")
		}
		if !strings.Contains(err.Error(), "measured NOTHING") {
			t.Errorf("refusal does not name the shape: %v", err)
		}
		// Both names, so the operator can see whether this is one missing binary
		// or a plan that stopped measuring.
		for _, want := range []string{"tmux.list_clients", "kitten.window"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal does not name %s: %v", want, err)
			}
		}
	})

	t.Run("a partial run succeeds and says it is partial", func(t *testing.T) {
		block, err := Render("partial", []string{"conditions line"}, []Row{good, bad})
		if err != nil {
			t.Fatalf("a run that measured something must not refuse: %v", err)
		}
		if !strings.Contains(block, "INCOMPLETE: 1 of 2") {
			t.Errorf("block does not declare itself incomplete, so it can be pasted as complete:\n%s", block)
		}
		if !strings.Contains(block, "tmux.list_clients") || !strings.Contains(block, "no tmux socket") {
			t.Errorf("block does not carry the refused row's reason:\n%s", block)
		}
		if !strings.Contains(block, "conditions line") {
			t.Errorf("block dropped the conditions, which are half of what makes a figure readable:\n%s", block)
		}
	})

	t.Run("a complete run declares nothing incomplete", func(t *testing.T) {
		// The vacuity guard for the footer: without it, a renderer that always
		// printed INCOMPLETE would pass the case above.
		block, err := Render("complete", nil, []Row{good})
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		if strings.Contains(block, Marker) {
			t.Errorf("a fully measured block claims to be incomplete:\n%s", block)
		}
	})
}

func TestByteCountGroupsThousands(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{fmt.Sprint(0), "0"}, {fmt.Sprint(41), "41"}, {fmt.Sprint(1386), "1,386"},
		{fmt.Sprint(2611982), "2,611,982"}, {fmt.Sprint(-1000), "-1,000"},
	} {
		n, _ := strconv.Atoi(tc.in)
		if got := byteCount(n); got != tc.want {
			t.Errorf("byteCount(%d) = %q, want %q", n, got, tc.want)
		}
	}
}
