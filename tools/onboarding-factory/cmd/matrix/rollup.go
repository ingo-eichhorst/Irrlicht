package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// runRollup derives agent-scenarios-coverage.json from the assessments and
// either writes it (default) or verifies the committed file matches (--check,
// for CI). Editorial notes and the legend block are preserved from the existing
// file so regeneration never clobbers maintainer prose.
func runRollup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("matrix rollup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "verify the committed rollup matches the derived output; exit 1 on drift")
	repoRoot := fs.String("repo-root", ".", "repository root containing replaydata/ and .claude/")
	scenarios := fs.String("scenarios", "", "override path to scenarios.json")
	agentsRoot := fs.String("agents-root", "", "override path to replaydata/agents")
	out := fs.String("out", "", "override path to agent-scenarios-coverage.json")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	scenariosPath := *scenarios
	if scenariosPath == "" {
		scenariosPath = filepath.Join(*repoRoot, "replaydata", "agents", "scenarios.json")
	}
	rootPath := *agentsRoot
	if rootPath == "" {
		rootPath = filepath.Join(*repoRoot, "replaydata", "agents")
	}
	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(filepath.Dir(scenariosPath), "agent-scenarios-coverage.json")
	}

	m, err := matrix.Load(matrix.Config{ScenariosPath: scenariosPath, AgentsRoot: rootPath})
	if err != nil {
		fmt.Fprintf(stderr, "matrix rollup: %v\n", err)
		return exitUsage
	}

	overlay := matrix.ReadOverlay(outPath)
	doc := m.BuildRollup(overlay)
	generated, err := matrix.MarshalRollup(doc)
	if err != nil {
		fmt.Fprintf(stderr, "matrix rollup: marshal: %v\n", err)
		return exitUsage
	}

	if *check {
		committed, err := os.ReadFile(outPath)
		if err != nil {
			fmt.Fprintf(stderr, "matrix rollup --check: read %s: %v\n", outPath, err)
			return exitUsage
		}
		if bytes.Equal(committed, generated) {
			fmt.Fprintf(stderr, "matrix rollup: %s is in sync with the assessments\n", filepath.Base(outPath))
			return exitOK
		}
		fmt.Fprintf(stderr, "matrix rollup: %s is STALE — an assessment changed without regenerating the rollup.\n", filepath.Base(outPath))
		fmt.Fprintln(stderr, "  run `make rollup` (or `matrix rollup`) and commit the result.")
		return exitFail
	}

	if err := os.WriteFile(outPath, generated, 0o644); err != nil {
		fmt.Fprintf(stderr, "matrix rollup: write %s: %v\n", outPath, err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "wrote %s (%d catalog rows × %d agents)\n", outPath, len(doc.Scenarios), len(doc.Agents))
	return exitOK
}
