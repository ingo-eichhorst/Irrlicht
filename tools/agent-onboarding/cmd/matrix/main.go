// Command matrix is the single query endpoint for the agent-onboarding
// scenario × adapter matrix. It loads the canonical model once
// (internal/matrix) and answers the questions the bash gates and shell scripts
// used to each reconstruct with their own jq filters and disposition loops:
//
//	matrix query --gate completeness --agent <a>   # every applicable cell terminal?
//	matrix query --gate consistency                # assessment ⟺ scenarios agree?
//	matrix query --cells --agent <a>               # per-cell JSON for one agent
//
// The completeness/consistency modes reproduce the bash gates' human output and
// exit codes verbatim, so scripts/lib/{completeness,consistency}-gate.sh become
// thin wrappers around this binary.
//
// Exit codes (matching the sibling cmd tools):
//
//	0  — gate passed / query succeeded
//	1  — gate failed (non-terminal cells / contradictions)
//	2  — usage or configuration error (bad flags, missing capabilities.json)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"irrlicht/tools/agent-onboarding/internal/matrix"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "query" {
		fmt.Fprintln(stderr, "usage: matrix query --gate completeness|consistency [--agent <a>] [--cells] [--repo-root .] [--scenarios p] [--agents-root r]")
		return exitUsage
	}
	fs := flag.NewFlagSet("matrix query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	gate := fs.String("gate", "", "gate mode: completeness | consistency")
	cells := fs.Bool("cells", false, "emit per-cell JSON for --agent")
	agent := fs.String("agent", "", "agent slug (required for completeness / cells)")
	repoRoot := fs.String("repo-root", ".", "repository root containing replaydata/ and .claude/")
	scenarios := fs.String("scenarios", "", "override path to scenarios.json")
	agentsRoot := fs.String("agents-root", "", "override path to replaydata/agents")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}

	cfg := matrix.Config{
		ScenariosPath: *scenarios,
		AgentsRoot:    *agentsRoot,
	}
	if cfg.ScenariosPath == "" {
		cfg.ScenariosPath = filepath.Join(*repoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	}
	if cfg.AgentsRoot == "" {
		cfg.AgentsRoot = filepath.Join(*repoRoot, "replaydata", "agents")
	}

	m, err := matrix.Load(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "matrix: %v\n", err)
		return exitUsage
	}

	switch {
	case *cells:
		if *agent == "" {
			fmt.Fprintln(stderr, "matrix: --cells requires --agent")
			return exitUsage
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(m.ApplicableCells(*agent)); err != nil {
			fmt.Fprintf(stderr, "matrix: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	case *gate == "completeness":
		return gateCompleteness(m, *agent, stdout, stderr)
	case *gate == "consistency":
		return gateConsistency(m, stderr)
	default:
		fmt.Fprintln(stderr, "matrix: specify --gate completeness|consistency or --cells")
		return exitUsage
	}
}

// gateCompleteness reproduces completeness-gate.sh's CLI: a per-cell table on
// stdout, GAP lines + the failure summary on stderr, exit 0/1/2.
func gateCompleteness(m *matrix.Matrix, agent string, stdout, stderr io.Writer) int {
	if agent == "" {
		fmt.Fprintln(stderr, "matrix: --gate completeness requires --agent")
		return exitUsage
	}
	if !m.HasAgent(agent) {
		fmt.Fprintf(stderr, "completeness-gate: no capabilities.json for %q\n", agent)
		return exitUsage
	}
	fmt.Fprintf(stdout, "== completeness gate: %s ==\n", agent)
	nonTerminal := 0
	for _, cs := range m.ApplicableCells(agent) {
		switch cs.Disposition {
		case matrix.DispRecorded, matrix.DispApplicableFalse, matrix.DispDriverGap:
			fmt.Fprintf(stdout, "  ok   %-32s %s\n", cs.CoverageID, cs.Disposition)
		case matrix.DispUnassessed:
			fmt.Fprintf(stderr, "  GAP  %-32s %s → assess %s %s\n", cs.CoverageID, cs.Disposition, agent, cs.CoverageID)
			nonTerminal++
		case matrix.DispAssessedNotRecord:
			fmt.Fprintf(stderr, "  GAP  %-32s %s → implement %s %s\n", cs.CoverageID, cs.Disposition, agent, cs.CoverageID)
			nonTerminal++
		}
	}
	fmt.Fprintln(stdout, "")
	if nonTerminal == 0 {
		fmt.Fprintf(stdout, "completeness-gate: %s COMPLETE — every applicable cell is terminal\n", agent)
		return exitOK
	}
	fmt.Fprintf(stderr, "completeness-gate: %s INCOMPLETE — %d cell(s) did not reach a terminal verdict (see above)\n", agent, nonTerminal)
	return exitFail
}

// gateConsistency reproduces consistency-gate.sh's CLI: the banner + per-error
// lines on stderr, exit 0/1.
func gateConsistency(m *matrix.Matrix, stderr io.Writer) int {
	fmt.Fprintln(stderr, "== assessment ⟺ scenarios consistency gate ==")
	dis := m.Disagreements()
	if len(dis) == 0 {
		fmt.Fprintln(stderr, "  every un-recorded cell's assessment verdict agrees with its scenarios.json applicable flag")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "consistency-gate: assessment and scenarios are consistent")
		return exitOK
	}
	for _, d := range dis {
		fmt.Fprintf(stderr, "  ERROR: %s\n", d.Message)
	}
	fmt.Fprintln(stderr, "")
	fmt.Fprintln(stderr, "consistency-gate: assessment ⟺ scenarios DISAGREE (see ERRORs above) — a cell's verdict and its matrix applicable flag tell different stories")
	return exitFail
}
