package processlifecycle

import (
	"testing"

	"irrlicht/core/internal/costreport"
)

// probecost_test.go binds every measured figure in this package's doc comments
// to the command that regenerates it. The generator itself is
// probecost_darwin_test.go; the reporter and its committed mutation evidence are
// core/internal/costreport (#1572).
//
// This file carries no build tag on purpose. Half the package is behind
// //go:build darwin, and the anchors below are matched against SOURCE TEXT
// rather than resolved through go/types, so the tripwire still grades
// osutil_darwin.go on a linux runner — where a type-aware rule would find
// nothing and pass, which is the gate-whose-absence-reads-as-a-pass shape this
// repo has paid for repeatedly.

// costFigureAnchors is the committed list of doc comments in this package that
// quote a measured figure. See costreport.Anchor for why the figure stays in the
// comment rather than being replaced by a pointer.
var costFigureAnchors = []costreport.Anchor{
	{
		File:   "shellout.go",
		Symbol: "const shelloutTimeout",
		Why:    "2s is a compromise between a probe that blocks discovery and one killed before it answers; what the probes actually cost is what makes it a compromise rather than a guess",
	},
	{
		File:   "osutil.go",
		Symbol: "type ancestryReads",
		Why:    "the per-exec `ps` cost is what the dedup saves, multiplied by 2 x depth",
	},
	{
		File:   "osutil.go",
		Symbol: "const clientHostBudget",
		Why:    "the tmux/lsof scan costs are the order-of-magnitude gap the one-constant decision is argued against",
	},
	{
		File:   "osutil_darwin.go",
		Symbol: "type bundleIDMemo",
		Why:    "the per-call plutil cost is what the memo saves — and the figure whose two incompatible values (#1524's 2.2ms, #1544's 9.7ms) #1572 was filed about",
	},
}

func TestEveryMeasuredFigureNamesItsGenerator(t *testing.T) {
	costreport.AssertFiguresNameTheirGenerator(t, costFigureAnchors)
}
