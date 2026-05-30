package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// runRecord dispatches the record sub-verbs:
//
//	of record run         --agent a --scenario s [--attach] [--dry-run]
//	of record prereq-check --agent a
//	of record verify      --agent a --scenario s [--json]   (alias of `of verify`)
//
// `run` is a THIN WRAPPER (per the roadmap): it resolves the agent's driver
// (now living in replaydata/agents/<agent>/) + the orchestration script, prints
// the recording prerequisites, and execs run-cell.sh. The live capture needs a
// dev `irrlichd --record` + the agent CLI under tmux — it is NOT exercised by
// the test suite; --dry-run resolves + prints the plan without executing.
func runRecord(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: of record run|prereq-check|verify ...")
		return exitUsage
	}
	switch args[0] {
	case "run":
		return runRecordRun(args[1:], stdout, stderr)
	case "prereq-check":
		return runRecordPrereq(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr) // same engine as `of verify`
	default:
		fmt.Fprintln(stderr, "of record: verb must be run, prereq-check, or verify")
		return exitUsage
	}
}

// resolveDriver returns the agent's driver script, preferring the interactive
// driver over the headless one. ok is false when neither exists.
func resolveDriver(repoRoot, agent string) (string, bool) {
	base := filepath.Join(repoRoot, "replaydata", "agents", agent)
	for _, name := range []string{"driver-interactive.sh", "driver.sh"} {
		p := filepath.Join(base, name)
		if fileExists(p) {
			return p, true
		}
	}
	return "", false
}

// resolveRunCell finds the orchestration script. It lives in the factory
// (relocated out of the retired ir:onboard-agent skill in #528); ok is false
// when it is absent.
func resolveRunCell(repoRoot string) (string, bool) {
	p := filepath.Join(repoRoot, "tools", "onboarding-factory", "scripts", "run-cell.sh")
	if fileExists(p) {
		return p, true
	}
	return "", false
}

// readPrereqs returns the agent's recording prerequisites from its
// metadata.json. Missing file → nil (agents predating `of agent add` carry
// none yet); never an error.
func readPrereqs(repoRoot, agent string) []string {
	b, err := os.ReadFile(filepath.Join(repoRoot, "replaydata", "agents", agent, "metadata.json"))
	if err != nil {
		return nil
	}
	var am struct {
		Prerequisites []string `json:"prerequisites"`
	}
	_ = json.Unmarshal(b, &am)
	return am.Prerequisites
}

func runRecordPrereq(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of record prereq-check")
	var (
		agent    = fs.String("agent", "", "agent id")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *agent == "" {
		fmt.Fprintln(stderr, "of record prereq-check: --agent is required")
		return exitUsage
	}
	prereqs := readPrereqs(*repoRoot, *agent)
	if len(prereqs) == 0 {
		fmt.Fprintf(stdout, "%s: no recording prerequisites declared\n", *agent)
		return exitOK
	}
	fmt.Fprintf(stdout, "%s recording prerequisites (verify before recording):\n", *agent)
	for _, p := range prereqs {
		fmt.Fprintf(stdout, "  - %s\n", p)
	}
	return exitOK
}

func runRecordRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of record run")
	var (
		agent    = fs.String("agent", "", "agent id")
		scenario = fs.String("scenario", "", "scenario name")
		attach   = fs.Bool("attach", false, "attach to an already-running dev daemon (--record)")
		dryRun   = fs.Bool("dry-run", false, "resolve + print the plan without executing")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *agent == "" || *scenario == "" {
		fmt.Fprintln(stderr, "of record run: --agent and --scenario are required")
		return exitUsage
	}
	if _, ok := shard.Load(*repoRoot, *scenario); !ok {
		fmt.Fprintf(stderr, "of record run: scenario %q not in the catalog\n", *scenario)
		return exitFail
	}
	driver, ok := resolveDriver(*repoRoot, *agent)
	if !ok {
		fmt.Fprintf(stderr, "of record run: no driver for %q (expected replaydata/agents/%s/driver-interactive.sh or driver.sh)\n", *agent, *agent)
		return exitFail
	}
	runCell, ok := resolveRunCell(*repoRoot)
	if !ok {
		fmt.Fprintf(stderr, "of record run: run-cell.sh not found at tools/onboarding-factory/scripts/run-cell.sh\n")
		return exitFail
	}

	// Surface prerequisites — a human acts on these before a live capture.
	for _, p := range readPrereqs(*repoRoot, *agent) {
		fmt.Fprintf(stderr, "prereq: %s\n", p)
	}

	cmdArgs := []string{}
	if *attach {
		cmdArgs = append(cmdArgs, "--attach")
	}
	cmdArgs = append(cmdArgs, *agent, *scenario)

	if *dryRun {
		fmt.Fprintf(stdout, "driver:   %s\n", driver)
		fmt.Fprintf(stdout, "run-cell: %s\n", runCell)
		fmt.Fprintf(stdout, "command:  %s %v\n", runCell, cmdArgs)
		return exitOK
	}

	cmd := exec.Command(runCell, cmdArgs...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Dir = *repoRoot
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "of record run: %v\n", err)
		return exitFail
	}
	return exitOK
}
