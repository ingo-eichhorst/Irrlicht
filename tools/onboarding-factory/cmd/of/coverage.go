package main

import (
	"fmt"
	"io"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// runCoverage emits the derived coverage rollup. Since the committed
// agent-scenarios-coverage.json was retired (#524), this is computed in-memory
// from the assessments every time — there is no file to read or keep in sync.
// An empty overlay means generated_at falls back to the max assessed_at.
func runCoverage(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of coverage")
	var (
		asJSON   = fs.Bool("json", false, "emit JSON (the rollup is JSON either way)")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	_ = asJSON // the rollup is JSON; the flag exists for CLI symmetry

	m, err := matrix.LoadRepo(*repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "of coverage: %v\n", err)
		return exitUsage
	}
	b, err := matrix.MarshalRollup(m.BuildRollup(matrix.RollupOverlay{}))
	if err != nil {
		fmt.Fprintf(stderr, "of coverage: marshal: %v\n", err)
		return exitUsage
	}
	stdout.Write(b)
	fmt.Fprintln(stdout)
	return exitOK
}
