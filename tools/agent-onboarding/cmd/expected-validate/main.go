// expected-validate runs the spec-grounded expected.jsonl validator
// against one scenario directory and prints a JSON report.
//
// Used by tools/replay-fixtures.sh to surface drift between the spec
// and the daemon's actual behavior. Exit codes:
//
//	0  — validation passed (or nothing to validate: no expected.jsonl, or no
//	     recording at all — neither events.jsonl nor a transcript)
//	1  — validation failed; report on stdout shows which phases mismatched
//	2  — internal error: malformed expected.jsonl, OR a HALF-recorded cell
//	     (transcript present but events.jsonl missing — #496 RC6)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"irrlicht/tools/agent-onboarding/internal/validate"
)

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: expected-validate <cell-dir> [recording-name]")
		os.Exit(2)
	}
	scenarioDir := os.Args[1]
	var report *validate.ExpectedReport
	var err error
	if len(os.Args) == 3 {
		// Validate ONE explicit recording against the cell's current spec.
		recDir := filepath.Join(scenarioDir, "recordings", os.Args[2])
		report, err = validate.ValidateExpectedAgainst(
			filepath.Join(scenarioDir, "expected.jsonl"),
			filepath.Join(recDir, "events.jsonl"),
		)
	} else {
		// Validate the cell's NEWEST recording.
		report, err = validate.ValidateExpected(scenarioDir)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if report == nil {
		// Nothing to validate — either expected.jsonl is missing (no
		// spec declared yet) or there is no recording at all (neither
		// events.jsonl nor a transcript — an applicable:false cell whose
		// recording cannot be captured today). A transcript-without-events
		// cell is NOT skipped here; it returns an error above (#496 RC6).
		fmt.Println(`{"pass": true, "skipped": "nothing to validate (no expected.jsonl, or no recording at all)"}`)
		os.Exit(0)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if !report.Pass {
		os.Exit(1)
	}
	os.Exit(0)
}
