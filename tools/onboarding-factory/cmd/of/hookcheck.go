package main

import (
	"fmt"
	"io"

	"irrlicht/tools/onboarding-factory/internal/hookcov"
)

// runHookCheck answers, for ONE staged recording, the question
// `of coverage --hooks` answers for the whole committed corpus: does <agent>
// declare a hooks permission, and does THIS events.jsonl carry a
// hook_received event attributable to it?
//
// promote-recording.sh is the caller (#1754): it is the last gate before a
// staged capture becomes committed truth, and a hooks-declaring adapter's
// hook-free recording reaching that gate unremarked is exactly the failure
// mode #1735 took three attempts to diagnose — a complete, healthy-looking
// recording (driver_exit_reason=ok, completeness=complete) whose hook channel
// never fired, with nothing anywhere saying so. Always emits JSON: the only
// caller is a script that parses it with jq, not a human reading a table.
func runHookCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of hookcheck")
	var (
		agent  = fs.String("agent", "", "adapter slug (as used under replaydata/agents/<agent>)")
		events = fs.String("events", "", "path to the staged (or committed) events.jsonl")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *agent == "" || *events == "" {
		fmt.Fprintln(stderr, "of hookcheck: --agent and --events are required")
		return exitUsage
	}

	res := hookCheckResult{
		Agent:         *agent,
		DeclaresHooks: hookcov.Declared()[*agent],
		HasHookEvent:  hookcov.HasOwnHookEvent(*events, *agent),
	}
	if err := writeJSON(stdout, res); err != nil {
		fmt.Fprintf(stderr, "of hookcheck: encode: %v\n", err)
		return exitUsage
	}
	return exitOK
}

// hookCheckResult is the whole answer: nothing here needs the corpus counts
// hookcov.AdapterCoverage carries (cells/recordings/status) — this is a
// yes/no about one file, not a rollup.
type hookCheckResult struct {
	Agent         string `json:"agent"`
	DeclaresHooks bool   `json:"declares_hooks"`
	HasHookEvent  bool   `json:"has_hook_event"`
}
