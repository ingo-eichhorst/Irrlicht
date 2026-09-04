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
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

func main() {
	flags := flag.NewFlagSet("expected-validate", flag.ContinueOnError)
	profileValue := flags.String("profile", string(matrix.ProfileCLILocal), "execution profile")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 1 && flags.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: expected-validate [--profile cli-local|desktop-local] <cell-dir> [recording-name]")
		os.Exit(2)
	}
	profile, err := matrix.ParseExecutionProfile(*profileValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	scenarioDir := flags.Arg(0)
	var report *validate.ExpectedReport
	if flags.NArg() == 2 {
		// Validate ONE explicit recording against the cell's current spec.
		recDir := filepath.Join(scenarioDir, "recordings", flags.Arg(1))
		report, err = validate.ValidateExpectedAgainst(
			filepath.Join(scenarioDir, "expected.jsonl"),
			filepath.Join(recDir, "events.jsonl"),
		)
	} else {
		// Validate the cell's newest recording within the selected profile.
		report, err = validate.ValidateExpectedForProfile(scenarioDir, profile)
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
