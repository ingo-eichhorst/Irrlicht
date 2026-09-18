package main

import (
	"fmt"
	"io"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// runVerify is the go-test-style verify for one (agent, scenario) cell: replay
// the newest recording and check the lifecycle-state phases, metric vector,
// and declarative transcript and event assertions from expected.jsonl. Exit 1
// when a state phase fails (unless known_failing), a hard metric assertion
// fails, or asserted content does not match. Metric drifts remain advisory.
// (`of record verify` in P6 calls the same engine after capture.)
func runVerify(args []string, stdout, stderr io.Writer) int {
	request, err := parseVerifyRequest(args)
	if err != nil {
		writeCommandError(stderr, "of verify", err)
		return exitUsage
	}
	cellDir, err := verifyCellDir(request)
	if err != nil {
		fmt.Fprintf(stderr, "of verify: %v\n", err)
		return exitFail
	}
	return verifyCell(request, cellDir, stdout, stderr)
}

type verifyRequest struct {
	Agent, Scenario, Folder, RepoRoot string
	Profile                           matrix.ExecutionProfile
	JSON                              bool
}

func parseVerifyRequest(args []string) (verifyRequest, error) {
	fs := newFlagSet("of verify")
	var (
		agent    = fs.String("agent", "", "agent id")
		scenario = fs.String("scenario", "", "scenario name")
		folder   = fs.String("folder", "", "override on-disk folder (default: <dashed-id>_<name>)")
		asJSON   = fs.Bool("json", false, "emit the combined report as JSON")
		repoRoot = fs.String("repo-root", ".", "repository root")
		profile  = fs.String("profile", string(matrix.ProfileCLILocal), "execution profile (cli-local or desktop-local)")
	)
	if err := fs.Parse(args); err != nil {
		return verifyRequest{}, flagParseError{cause: err}
	}
	if *agent == "" {
		return verifyRequest{}, fmt.Errorf("--agent and --scenario are required")
	}
	if *scenario == "" {
		return verifyRequest{}, fmt.Errorf("--agent and --scenario are required")
	}
	executionProfile, err := matrix.ParseExecutionProfile(*profile)
	if err != nil {
		return verifyRequest{}, err
	}
	return verifyRequest{
		Agent: *agent, Scenario: *scenario, Folder: *folder, RepoRoot: *repoRoot,
		Profile: executionProfile, JSON: *asJSON,
	}, nil
}

func verifyCellDir(request verifyRequest) (string, error) {
	folder := request.Folder
	if folder == "" {
		sh, ok := shard.Load(request.RepoRoot, request.Scenario)
		if !ok {
			return "", fmt.Errorf("scenario %q not in the catalog", request.Scenario)
		}
		// Prefer the agent's existing folder, because variant cells can use a
		// non-canonical name.
		folder = shard.AgentFolderForScenario(request.RepoRoot, request.Agent, sh.Name)
	}
	return shard.AgentCellDir(request.RepoRoot, request.Agent, folder), nil
}

func verifyCell(request verifyRequest, cellDir string, stdout, stderr io.Writer) int {
	state, err := validate.ValidateExpectedForProfile(cellDir, request.Profile)
	if err != nil {
		fmt.Fprintf(stderr, "of verify: state validation: %v\n", err)
		return exitUsage
	}
	obs, err := validate.ValidateObservationsForProfile(cellDir, request.Profile)
	if err != nil {
		fmt.Fprintf(stderr, "of verify: observation validation: %v\n", err)
		return exitUsage
	}
	transcript, err := validate.ValidateTranscriptForProfile(cellDir, request.Profile)
	if err != nil {
		fmt.Fprintf(stderr, "of verify: transcript validation: %v\n", err)
		return exitUsage
	}
	events, err := validate.ValidateEventsForProfile(cellDir, request.Profile)
	if err != nil {
		fmt.Fprintf(stderr, "of verify: event validation: %v\n", err)
		return exitUsage
	}

	stateOK := state == nil || state.Pass || state.Meta.KnownFailing
	obsOK := obs == nil || obs.Pass
	transcriptOK := transcript == nil || transcript.Pass
	// An event assertion can waive only its own known defect. Other event and
	// transcript regressions must still fail a known-failing lifecycle cell.
	eventsOK := events.ExpectedPass()

	if request.JSON {
		_ = writeJSON(stdout, map[string]any{
			"agent": request.Agent, "scenario": request.Scenario,
			"state_pass": stateOK, "observations_pass": obsOK, "transcript_pass": transcriptOK, "events_pass": eventsOK,
			"state": state, "observations": obs, "transcript": transcript, "events": events,
		})
	} else {
		printVerifyText(stdout, request.Agent, request.Scenario, state, obs, transcript, events)
	}
	if !stateOK || !obsOK || !transcriptOK || !eventsOK {
		return exitFail
	}
	return exitOK
}

func printVerifyText(stdout io.Writer, agent, scenario string, state *validate.ExpectedReport, obs *validate.ObservationReport, transcript, events *validate.RecordReport) {
	fmt.Fprintf(stdout, "verify %s / %s\n", agent, scenario)
	switch {
	case state == nil:
		fmt.Fprintln(stdout, "  state:        (no spec / no recording)")
	case state.Pass:
		fmt.Fprintf(stdout, "  state:        PASS — %s\n", state.Summary)
	case state.Meta.KnownFailing:
		fmt.Fprintf(stdout, "  state:        known_failing — %s\n", state.Summary)
	default:
		fmt.Fprintf(stdout, "  state:        FAIL — %s\n", state.Summary)
	}
	switch {
	case obs == nil || obs.Skipped:
		note := "no golden"
		if obs != nil && obs.Note != "" {
			note = obs.Note
		}
		fmt.Fprintf(stdout, "  observations: skipped (%s)\n", note)
	default:
		verdict := "PASS"
		if !obs.Pass {
			verdict = "FAIL"
		}
		fmt.Fprintf(stdout, "  observations: %s — %d assert(s), %d drift(s)\n", verdict, len(obs.Asserts), len(obs.Drifts))
		for _, a := range obs.Asserts {
			mark := "✓"
			if !a.OK {
				mark = "✗"
			}
			fmt.Fprintf(stdout, "    %s %s: want %s got %s\n", mark, a.Field, a.Expected, a.Actual)
		}
		for _, d := range obs.Drifts {
			fmt.Fprintf(stdout, "    ~ %s: %s → %s (drift vs prior)\n", d.Field, d.Prior, d.Current)
		}
	}
	if transcript != nil {
		printRecordReport(stdout, "transcript", transcript, false)
	}
	if events != nil {
		printRecordReport(stdout, "events", events, events.ExpectedPass())
	}
}

func printRecordReport(stdout io.Writer, label string, report *validate.RecordReport, expectedPass bool) {
	verdict := "PASS"
	if !report.Pass {
		verdict = "FAIL"
		if expectedPass {
			verdict = "known_failing"
		}
	}
	fmt.Fprintf(stdout, "  %-12s %s — %d assert(s)\n", label+":", verdict, len(report.Asserts))
	for _, assertion := range report.Asserts {
		mark := "✓"
		if !assertion.OK {
			mark = "✗"
			if assertion.KnownFailing {
				mark = "~"
			}
		}
		fmt.Fprintf(stdout, "    %s %s: want %s; got %s\n", mark, assertion.Name, assertion.Expected, assertion.Actual)
	}
}
