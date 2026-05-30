package main

import (
	"fmt"
	"io"

	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// runVerify is the go-test-style verify for one (agent, scenario) cell: replay
// the newest recording and check BOTH the lifecycle-state phases (expected.jsonl
// definitions) AND the metric vector (model/cost/tokens — hard asserts from the
// spec's observations block + a soft-diff vs the prior recording). Exit 1 when a
// state phase fails (unless known_failing) or a hard metric assertion fails;
// metric drifts are reported but never fail. (`of record verify` in P6 calls the
// same engine after capturing a fresh recording.)
func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of verify")
	var (
		agent    = fs.String("agent", "", "agent id")
		scenario = fs.String("scenario", "", "scenario name")
		folder   = fs.String("folder", "", "override on-disk folder (default: <dashed-id>_<name>)")
		asJSON   = fs.Bool("json", false, "emit the combined report as JSON")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *agent == "" || *scenario == "" {
		fmt.Fprintln(stderr, "of verify: --agent and --scenario are required")
		return exitUsage
	}
	fold := *folder
	if fold == "" {
		if sh, ok := shard.Load(*repoRoot, *scenario); ok {
			// Prefer the agent's existing folder (variant cells live under a
			// non-canonical name), so verify reads the same folder of record/
			// write land in — never a phantom canonical folder.
			fold = shard.AgentFolderForScenario(*repoRoot, *agent, sh.Name)
		} else {
			fmt.Fprintf(stderr, "of verify: scenario %q not in the catalog\n", *scenario)
			return exitFail
		}
	}
	cellDir := shard.AgentCellDir(*repoRoot, *agent, fold)

	state, err := validate.ValidateExpected(cellDir)
	if err != nil {
		fmt.Fprintf(stderr, "of verify: state validation: %v\n", err)
		return exitUsage
	}
	obs, err := validate.ValidateObservations(cellDir)
	if err != nil {
		fmt.Fprintf(stderr, "of verify: observation validation: %v\n", err)
		return exitUsage
	}

	stateOK := state == nil || state.Pass || state.Meta.KnownFailing
	obsOK := obs == nil || obs.Pass

	if *asJSON {
		_ = writeJSON(stdout, map[string]any{
			"agent": *agent, "scenario": *scenario,
			"state_pass": stateOK, "observations_pass": obsOK,
			"state": state, "observations": obs,
		})
	} else {
		printVerifyText(stdout, *agent, *scenario, state, obs)
	}
	if !stateOK || !obsOK {
		return exitFail
	}
	return exitOK
}

func printVerifyText(stdout io.Writer, agent, scenario string, state *validate.ExpectedReport, obs *validate.ObservationReport) {
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
}
