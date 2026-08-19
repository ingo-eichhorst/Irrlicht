package git

import (
	"testing"

	"irrlicht/core/internal/costreport"
)

// gitcost_test.go binds the three measured tables in adapter.go — gitTimeout's,
// gitHistoryTimeout's and gitMaxOutput's — to the command that regenerates them.
// The generator is gitcost_measure_test.go; the reporter and its committed
// mutation evidence are core/internal/costreport (#1572).

// costFigureAnchors is the committed list of doc comments in this adapter that
// quote a measured figure. All three are the stated justification for a
// constant, which is why the figures stay in place rather than being replaced by
// a pointer: gitHistoryTimeout = 30s is argued from the SHAPE of a curve, and a
// reader deciding whether 30s is still right needs that curve in front of them.
var costFigureAnchors = []costreport.Anchor{
	{
		File:   "adapter.go",
		Symbol: "const gitTimeout",
		Why:    "5s is ~130x the heaviest fixed-cost read, and `tag --contains`'s membership in that profile is argued from a measured ~1us/commit",
	},
	{
		File:   "adapter.go",
		Symbol: "const gitHistoryTimeout",
		Why:    "30s is chosen against the measured 3,209 / 100,000 / 1,000,000-commit curve — #1553's own headline figure was 2.5x wrong before that curve existed",
	},
	{
		File:   "adapter.go",
		Symbol: "const gitMaxOutput",
		Why:    "64 MiB is argued from bytes-per-commit, a figure #1553 got ~2.3x low by dividing one byte count by the wrong population",
	},
}

func TestEveryMeasuredFigureNamesItsGenerator(t *testing.T) {
	costreport.AssertFiguresNameTheirGenerator(t, costFigureAnchors)
}
