// expected-validate runs the spec-grounded expected.jsonl validator
// against one scenario directory and prints a JSON report.
//
// Used by tools/replay-fixtures.sh to surface drift between the spec
// and the daemon's actual behavior. Exit codes:
//
//	0  — validation passed (or no expected.jsonl present, nothing to validate)
//	1  — validation failed; report on stdout shows which phases mismatched
//	2  — internal error (missing events.jsonl, malformed expected.jsonl, etc.)
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"irrlicht/tools/agent-onboarding/internal/validate"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: expected-validate <scenario-dir>")
		os.Exit(2)
	}
	scenarioDir := os.Args[1]
	report, err := validate.ValidateExpected(scenarioDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if report == nil {
		// Nothing to validate — either expected.jsonl is missing (no
		// spec declared yet) or events.jsonl is missing (applicable:
		// false scenario whose recording cannot be captured today).
		fmt.Println(`{"pass": true, "skipped": "nothing to validate (no expected.jsonl or no events.jsonl)"}`)
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
