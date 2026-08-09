package main

import (
	"fmt"
	"io"
	"strconv"
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
		asJSON   = fs.Bool("json", false, "emit JSON (selects JSON vs text under --hooks; the rollup is JSON either way)")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	*repoRoot = absRoot(*repoRoot)

	if *hooks {
		return runCoverageHooks(*repoRoot, *asJSON, stdout, stderr)
	}

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

// One column-width spec for the whole table: header, rows and the totals line
// all format through hookRowFormat with %s cells, so the widths cannot drift
// between them. printHookRow converts the counts.
const hookRowFormat = "%-14s %14s %6s %11s %11s  %s\n"

func printHookCoverageText(stdout io.Writer, rep hookcov.Report) {
	fmt.Fprintln(stdout, "hook coverage — recordings containing a hook_received event, per adapter")
	fmt.Fprintln(stdout, "scope: cells on disk under replaydata/agents/<adapter>/scenarios; the non-catalog regressions/ tree is excluded")
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, hookRowFormat,
		"adapter", "declares-hooks", "cells", "recordings", "with-hooks", "status")
	for _, a := range rep.Adapters {
		printHookRow(stdout, a.Adapter, yesNo(a.DeclaresHooks), a.Cells, a.Recordings, a.WithHooks, hookStatusNote(a.Status))
	}
	printHookRow(stdout, "total", "", rep.Totals.Cells, rep.Totals.Recordings, rep.Totals.WithHooks, "")

	fmt.Fprintln(stdout)
	if gaps := rep.Gaps(); len(gaps) > 0 {
		// Denominator is the hooks-declaring adapters, not all of them: "1 of
		// 11" invites the reading that 11 adapters declare hooks.
		fmt.Fprintf(stdout, "GAP: %d of %d hooks-declaring adapters have zero hook-bearing recordings: %s\n",
			len(gaps), rep.Declaring(), strings.Join(gaps, ", "))
	} else {
		fmt.Fprintln(stdout, "no gaps: every adapter that declares hooks has at least one hook-bearing recording")
	}
}

// printHookRow renders one table line. Trailing whitespace is trimmed so the
// totals row — which has no status cell — does not ship padding.
func printHookRow(stdout io.Writer, adapter, declares string, cells, recordings, withHooks int, status string) {
	line := fmt.Sprintf(hookRowFormat, adapter, declares,
		strconv.Itoa(cells), strconv.Itoa(recordings), strconv.Itoa(withHooks), status)
	fmt.Fprintln(stdout, strings.TrimRight(line, " \n"))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
