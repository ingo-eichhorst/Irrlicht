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
	"io"
	"os"
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

type expectedValidationRequest struct {
	ScenarioDir   string
	RecordingName string
	Profile       matrix.ExecutionProfile
}

func main() { os.Exit(runExpectedValidate(os.Args[1:], os.Stdout, os.Stderr)) }

func runExpectedValidate(args []string, stdout, stderr io.Writer) int {
	request, err := parseExpectedValidationRequest(args, stderr)
	if err != nil {
		return 2
	}
	report, err := validateExpectedRequest(request)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	return writeExpectedReport(stdout, report)
}

func parseExpectedValidationRequest(args []string, stderr io.Writer) (expectedValidationRequest, error) {
	flags := flag.NewFlagSet("expected-validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profileValue := flags.String("profile", string(matrix.ProfileCLILocal), "execution profile")
	if err := flags.Parse(args); err != nil {
		return expectedValidationRequest{}, err
	}
	switch flags.NArg() {
	case 1, 2:
	default:
		fmt.Fprintln(stderr, "usage: expected-validate [--profile cli-local|desktop-local] <cell-dir> [recording-name]")
		return expectedValidationRequest{}, fmt.Errorf("invalid argument count")
	}
	profile, err := matrix.ParseExecutionProfile(*profileValue)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return expectedValidationRequest{}, err
	}
	request := expectedValidationRequest{ScenarioDir: flags.Arg(0), Profile: profile}
	if flags.NArg() == 2 {
		request.RecordingName = flags.Arg(1)
	}
	return request, nil
}

func validateExpectedRequest(request expectedValidationRequest) (*validate.ExpectedReport, error) {
	if request.RecordingName == "" {
		return validate.ValidateExpectedForProfile(request.ScenarioDir, request.Profile)
	}
	recDir := filepath.Join(request.ScenarioDir, "recordings", request.RecordingName)
	return validate.ValidateExpectedAgainst(
		filepath.Join(request.ScenarioDir, "expected.jsonl"),
		filepath.Join(recDir, "events.jsonl"),
	)
}

func writeExpectedReport(stdout io.Writer, report *validate.ExpectedReport) int {
	if report == nil {
		// Nothing to validate — either expected.jsonl is missing (no
		// spec declared yet) or there is no recording at all (neither
		// events.jsonl nor a transcript — an applicable:false cell whose
		// recording cannot be captured today). A transcript-without-events
		// cell is NOT skipped here; it returns an error above (#496 RC6).
		fmt.Fprintln(stdout, `{"pass": true, "skipped": "nothing to validate (no expected.jsonl, or no recording at all)"}`)
		return 0
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if !report.Pass {
		return 1
	}
	return 0
}
