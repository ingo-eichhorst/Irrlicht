package main

import (
	"fmt"
	"io"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/hookcov"
	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/shard"
)

// runCoverage emits the derived coverage rollup. Since the committed
// agent-scenarios-coverage.json was retired (#524), this is computed in-memory
// from the assessments every time — there is no file to read or keep in sync.
// An empty overlay means generated_at falls back to the max assessed_at.
//
// --hooks switches to the hook-coverage report (#1363): a different question
// over the same catalog — not how much is recorded, but how much of what is
// recorded exercises the hook channel.
func runCoverage(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of coverage")
	var (
		hooks    = fs.Bool("hooks", false, "report per-adapter hook_received coverage instead of the rollup")
		asJSON   = fs.Bool("json", false, "emit JSON (the rollup is JSON either way)")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *hooks {
		return runCoverageHooks(*repoRoot, *asJSON, stdout, stderr)
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

// runCoverageHooks derives and renders the hook-coverage report. Exit stays 0
// even when there are gaps: this is a report, not a gate. `of validate` is the
// CI gate, and making a reporting command fail the build would be a policy
// change nobody asked for.
func runCoverageHooks(repoRoot string, asJSON bool, stdout, stderr io.Writer) int {
	catalogAdapters := shard.Agents(repoRoot)
	if len(catalogAdapters) == 0 {
		fmt.Fprintf(stderr, "of coverage --hooks: no onboarded adapters in %s\n", shard.File(repoRoot))
		return exitUsage
	}
	rep := hookcov.Coverage(repoRoot, catalogAdapters, hookcov.Declared())

	if asJSON {
		if err := writeJSON(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "of coverage --hooks: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	printHookCoverageText(stdout, rep)
	return exitOK
}

// hookStatusNote is the human-readable gloss per status. The gap line is
// deliberately the only one in capitals — it is the one a reader must not skim
// past.
func hookStatusNote(s hookcov.Status) string {
	switch s {
	case hookcov.StatusOK:
		return "ok"
	case hookcov.StatusGap:
		return "GAP — declares hooks, zero hook-bearing recordings"
	case hookcov.StatusIncidental:
		return "incidental — hook events present, adapter declares no hooks"
	default:
		return "—"
	}
}

const hookRowFormat = "%-14s %14s %6d %11d %11d  %s\n"

func printHookCoverageText(stdout io.Writer, rep hookcov.Report) {
	fmt.Fprintln(stdout, "hook coverage — recordings containing a hook_received event, per adapter")
	fmt.Fprintln(stdout, "scope: catalog cells only (replaydata/agents/<adapter>/scenarios); the non-catalog regressions/ tree is excluded")
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "%-14s %14s %6s %11s %11s  %s\n",
		"adapter", "declares-hooks", "cells", "recordings", "with-hooks", "status")
	for _, a := range rep.Adapters {
		fmt.Fprintf(stdout, hookRowFormat,
			a.Adapter, yesNo(a.DeclaresHooks), a.Cells, a.Recordings, a.WithHooks, hookStatusNote(a.Status))
	}
	fmt.Fprintf(stdout, "%-14s %14s %6d %11d %11d\n",
		"total", "", rep.Totals.Cells, rep.Totals.Recordings, rep.Totals.WithHooks)

	fmt.Fprintln(stdout)
	if gaps := rep.Gaps(); len(gaps) > 0 {
		fmt.Fprintf(stdout, "GAP: %d of %d adapters declare hooks but have zero hook-bearing recordings: %s\n",
			len(gaps), len(rep.Adapters), strings.Join(gaps, ", "))
	} else {
		fmt.Fprintln(stdout, "no gaps: every adapter that declares hooks has at least one hook-bearing recording")
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
