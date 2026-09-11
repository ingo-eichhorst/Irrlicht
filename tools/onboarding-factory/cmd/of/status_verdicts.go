package main

// `of status --profile desktop-local --verdicts` reports the Claude Desktop
// campaign's outcome from execution-results.json, the artifact that records it.
//
// The existing profile view cannot: it derives each cell's display state from
// the cell's ASSESSMENT axes, which were measured for the CLI, plus whether a
// desktop recording exists. A cell with an explicit, terminal not-runnable
// Desktop verdict therefore shows as "pending-record" — the same token a cell
// nobody has reached yet gets. This flag is additive and changes no existing
// number; it prints the verdicts and then says, in one line, how far the
// display state is from them, so the gap is visible rather than inferred.

import (
	"fmt"
	"io"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/desktopresults"
	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// verdictsProfileError rejects --verdicts outside the profile it describes.
// execution-results.json carries desktop-local results only, so under any other
// profile the flag would print a table about a profile the user did not ask for.
func verdictsProfileError(profile matrix.ExecutionProfile) error {
	if profile == matrix.ProfileDesktopLocal {
		return nil
	}
	return fmt.Errorf("--verdicts reads %s, which records %s results only; pass --profile %s",
		desktopresults.FileName, matrix.ProfileDesktopLocal, matrix.ProfileDesktopLocal)
}

// verdictReport pairs the census with the display states the same cells carry,
// so the two can be reported side by side.
type verdictReport struct {
	Census desktopresults.VerdictCensus `json:"census"`
	// PendingDisplayState is how many cells the profile view calls
	// "pending-record" while the contract holds a terminal verdict for them.
	// Zero means the two agree.
	PendingDisplayState int `json:"pending_display_state"`
}

func runStatusVerdicts(request statusRequest, stdout, stderr io.Writer) int {
	if err := verdictsProfileError(request.Profile); err != nil {
		fmt.Fprintf(stderr, "of status: %v\n", err)
		return exitUsage
	}
	if request.Agent == "" {
		fmt.Fprintln(stderr, "of status: --verdicts requires --agent")
		return exitUsage
	}
	census, err := desktopresults.CountVerdicts(request.RepoRoot, request.Agent)
	if err != nil {
		fmt.Fprintf(stderr, "of status: %v\n", err)
		return exitUsage
	}
	// A census over zero cells is a broken walk, not a clean campaign.
	if census.Cells == 0 {
		fmt.Fprintf(stderr, "of status: no %s cell found under %s — this census cannot run\n",
			request.Agent, request.RepoRoot)
		return exitUsage
	}

	pending, err := countPendingDisplayStates(request)
	if err != nil {
		fmt.Fprintf(stderr, "of status: %v\n", err)
		return exitUsage
	}
	report := verdictReport{Census: census, PendingDisplayState: pending}

	if request.JSON {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "of status: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	printVerdictReport(stdout, report)
	return exitOK
}

// countPendingDisplayStates counts the cells the profile view calls
// pending-record. It re-reads the matrix rather than trusting a remembered
// number, so the comparison is against what `of status` actually prints today.
func countPendingDisplayStates(request statusRequest) (int, error) {
	_, view, err := statusViewForRequest(request)
	if err != nil {
		return 0, err
	}
	pending := 0
	for _, scenario := range view.Scenarios {
		cell, ok := scenario.Cells[request.Agent]
		if ok && cell.DisplayState == matrix.StatePendingRecord {
			pending++
		}
	}
	return pending, nil
}

func printVerdictReport(stdout io.Writer, report verdictReport) {
	census := report.Census
	fmt.Fprintf(stdout, "Claude Desktop Local verdicts — %s, from %s\n\n",
		census.Agent, desktopresults.FileName)
	for _, outcome := range desktopresults.Outcomes() {
		fmt.Fprintf(stdout, "  %-18s %3d\n", outcome, census.ByOutcome[string(outcome)])
	}
	fmt.Fprintf(stdout, "  %-18s %3d  of %d cell(s)\n", "decided", census.WithVerdict, census.Cells)
	if len(census.WithoutVerdict) > 0 {
		fmt.Fprintf(stdout, "  %-18s %3d  %s\n", "no verdict",
			len(census.WithoutVerdict), strings.Join(census.WithoutVerdict, ", "))
	}
	fmt.Fprintf(stdout, "\nnote: `of status --profile %s` calls %d of these cells \"pending-record\".\n",
		matrix.ProfileDesktopLocal, report.PendingDisplayState)
	fmt.Fprintln(stdout, "That view derives its display state from the cell's CLI assessment plus whether a")
	fmt.Fprintln(stdout, "Desktop recording exists, so a terminal not-runnable verdict is invisible to it.")
	fmt.Fprintln(stdout, "The counts above are the campaign's actual outcome.")
}
