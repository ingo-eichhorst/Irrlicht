// Command matrix is the single query endpoint for the agent-onboarding
// scenario × adapter matrix. It loads the canonical model once
// (internal/matrix) and answers the questions the bash gates and shell scripts
// used to each reconstruct with their own jq filters and disposition loops:
//
//	matrix query --gate completeness --agent <a>   # every applicable cell terminal?
//	matrix query --cells --agent <a>               # per-cell JSON for one agent
//
// The completeness mode reproduces the bash gate's human output and exit codes
// verbatim, so scripts/lib/completeness-gate.sh is a thin wrapper around this
// binary.
//
// The consistency gate (assessment ⟺ scenarios.json applicable-flag agreement)
// was retired with the #510 shard migration: a shard is the single source for a
// cell, so there is no second file for its verdict to disagree with — the whole
// "files drifted apart" failure class the gate guarded is gone by construction.
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

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const usage = `usage:
  matrix query --gate completeness --agent <a> [--repo-root .] [--scenarios p] [--agents-root r]
  matrix query --cells --agent <a> [--repo-root .] [--scenarios p] [--agents-root r]
  matrix rollup [--check] [--repo-root .] [--scenarios p] [--agents-root r] [--out p]`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// flagSet reports whether a flag was explicitly passed on the command line
// (as opposed to sitting at its default), so we can tell "no --repo-root" from
// "--repo-root ." — only the former should let --agents-root derive the root.
func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "query":
		// handled below
	case "rollup":
		return runRollup(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	fs := flag.NewFlagSet("matrix query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	gate := fs.String("gate", "", "gate mode: completeness")
	cells := fs.Bool("cells", false, "emit per-cell JSON for --agent")
	agent := fs.String("agent", "", "agent slug (required for completeness / cells)")
	repoRoot := fs.String("repo-root", ".", "repository root containing replaydata/ and .claude/")
	agentsRoot := fs.String("agents-root", "", "override path to replaydata/agents")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}

	cfg := matrix.Config{
		RepoRoot:   *repoRoot,
		AgentsRoot: *agentsRoot,
	}
	if cfg.AgentsRoot == "" {
		cfg.AgentsRoot = filepath.Join(*repoRoot, "replaydata", "agents")
	}
	// The shard model reads replaydata/scenarios/ under RepoRoot, but the
	// gate scripts (completeness-gate.sh) drive this CLI with absolute
	// --agents-root and no --repo-root — so a default RepoRoot of "." would
	// look for shards under the caller's CWD and find none. When --agents-root
	// is given without an explicit --repo-root, derive the repo root from it
	// (replaydata/agents → repo root) so the shards resolve next to the agents
	// tree the caller actually pointed at.
	if *agentsRoot != "" && !flagSet(fs, "repo-root") {
		cfg.RepoRoot = filepath.Dir(filepath.Dir(*agentsRoot))
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
	default:
		fmt.Fprintln(stderr, "matrix: specify --gate completeness or --cells")
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
		fmt.Fprintf(stderr, "completeness-gate: %q is not an onboarded adapter (not in replaydata/agents/scenarios.json meta.min_versions)\n", agent)
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
